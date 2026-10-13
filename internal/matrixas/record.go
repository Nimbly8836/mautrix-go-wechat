package matrixas

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// Leave room for the event envelope, signatures and relations below Matrix's
// 64 KiB event limit. JSON escaping can substantially expand the event.
const recordContentBudget = 48 * 1024

// Rendering state belongs to this delivery, never to the shared inbound record.
type recordAttachment struct {
	url     id.ContentURIString
	status  string
	mime    string
	msgType event.MessageType
}

type recordChild struct {
	key     string
	content *event.MessageEventContent
}

func recordHTML(text string) string {
	return strings.ReplaceAll(html.EscapeString(strings.ToValidUTF8(text, "\uFFFD")), "\n", "<br>")
}

func recordFilename(media *remoteapi.Media) string {
	if media.FileName != "" {
		return strings.ToValidUTF8(media.FileName, "\uFFFD")
	}
	return "attachment"
}

func recordMediaType(media *remoteapi.Media) event.MessageType {
	// Never display active SVG/HTML content as an inline image. A supplied
	// raster MIME alone isn't evidence that the bytes are a safe raster image.
	switch http.DetectContentType(media.Data) {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return event.MsgImage
	}
	if strings.HasPrefix(media.MimeType, "video/") {
		return event.MsgVideo
	}
	return event.MsgFile
}

func recordMediaLabel(media *remoteapi.Media) string {
	switch recordMediaType(media) {
	case event.MsgImage:
		return "图片"
	case event.MsgVideo:
		return "视频"
	default:
		return "文件"
	}
}

func recordItemLabel(item *remoteapi.RecordItem, index int) string {
	return fmt.Sprintf("#%d %s", index, strings.TrimSpace(item.SenderName+" "+item.Time))
}

func renderRecord(record *remoteapi.ForwardedRecord, attachments map[*remoteapi.RecordItem]recordAttachment) *event.MessageEventContent {
	var plain, formatted strings.Builder
	active := make(map[*remoteapi.ForwardedRecord]bool)
	index := 0
	var render func(*remoteapi.ForwardedRecord)
	render = func(r *remoteapi.ForwardedRecord) {
		if r == nil {
			return
		}
		if active[r] {
			plain.WriteString("[循环引用的聊天记录]")
			formatted.WriteString("[循环引用的聊天记录]")
			return
		}
		active[r] = true
		defer delete(active, r)
		title := "[合并聊天记录] " + r.Title
		plain.WriteString(title)
		formatted.WriteString("<p><strong>" + recordHTML(title) + "</strong></p>")
		for i := range r.Items {
			item := &r.Items[i]
			index++
			label := recordItemLabel(item, index)
			plain.WriteString("\n\n" + label + "\n" + item.Text)
			formatted.WriteString("<blockquote><p><strong>" + recordHTML(label) + "</strong><br>" + recordHTML(item.Text) + "</p>")
			attachment := attachments[item]
			if item.Media != nil {
				status := attachment.status
				if attachment.url != "" {
					status = "下方原生媒体，回复本记录"
				} else if status == "" {
					status = "附件下载不可用"
					if len(item.Media.Data) > 0 {
						status = "附件待桥接"
					}
				}
				text := "[" + recordMediaLabel(item.Media) + "] " + recordFilename(item.Media) + "（" + status + "）"
				plain.WriteString("\n" + text)
				formatted.WriteString("<p>" + recordHTML(text) + "</p>")
			}
			if item.MediaStatus != "" {
				plain.WriteString("\n（" + item.MediaStatus + "）")
				formatted.WriteString("<p>" + recordHTML("（"+item.MediaStatus+"）") + "</p>")
			}
			if item.Record != nil {
				plain.WriteString("\n")
				render(item.Record)
			}
			formatted.WriteString("</blockquote>")
		}
	}
	render(record)
	return &event.MessageEventContent{MsgType: event.MsgText, Body: strings.ToValidUTF8(plain.String(), "\uFFFD"), Format: event.FormatHTML, FormattedBody: formatted.String()}
}

// Upload only downloaded bytes, once per asset within this delivery. Backend
// URLs may carry credentials and are never used as public download URLs.
func (s *Server) uploadRecordMedia(ctx context.Context, record *remoteapi.ForwardedRecord) (*event.MessageEventContent, []recordChild, string) {
	attachments := make(map[*remoteapi.RecordItem]recordAttachment)
	assets := make(map[*remoteapi.Media]recordAttachment)
	active := make(map[*remoteapi.ForwardedRecord]bool)
	var children []recordChild
	index, failedUploads, unavailableDownloads := 0, 0, 0
	var upload func(*remoteapi.ForwardedRecord)
	upload = func(r *remoteapi.ForwardedRecord) {
		if r == nil || active[r] {
			return
		}
		active[r] = true
		defer delete(active, r)
		for i := range r.Items {
			item := &r.Items[i]
			index++
			if media := item.Media; media != nil && len(media.Data) > 0 {
				attachment, exists := assets[media]
				if !exists {
					mime := media.MimeType
					if mime == "" {
						mime = http.DetectContentType(media.Data)
					}
					attachment = recordAttachment{status: "附件上传失败", mime: mime, msgType: recordMediaType(media)}
					uploadCtx, cancel := s.matrixOperationContext(ctx)
					resp, err := s.as.BotIntent().UploadBytesWithName(uploadCtx, media.Data, mime, recordShortText(recordFilename(media), 512))
					cancel()
					if err == nil && resp != nil {
						if uri, ok := validMXC(resp.ContentURI.String()); ok && len(uri) <= 2048 {
							attachment.url, attachment.status = uri, ""
						}
					}
					assets[media] = attachment
				}
				attachments[item] = attachment
				if attachment.url != "" {
					label := recordItemLabel(item, index) + " [" + recordMediaLabel(media) + "] " + recordFilename(media)
					children = append(children, recordChild{key: fmt.Sprintf("item-%d", index), content: &event.MessageEventContent{
						MsgType: attachment.msgType, Body: recordShortText(label, 2048),
						FileName: recordShortText(recordFilename(media), 512), URL: attachment.url,
						Info: &event.FileInfo{MimeType: attachment.mime, Size: len(media.Data)},
					}})
				} else {
					failedUploads++
				}
			} else if item.Media != nil || item.MediaStatus != "" {
				unavailableDownloads++
			}
			upload(item.Record)
		}
	}
	upload(record)
	status := ""
	if unavailableDownloads > 0 || failedUploads > 0 {
		status = fmt.Sprintf("附件下载不可用：%d；附件上传失败：%d。", unavailableDownloads, failedUploads)
	}
	return renderRecord(record, attachments), children, status
}

func recordContentTooLarge(content *event.MessageEventContent) bool {
	data, err := json.Marshal(content)
	return err != nil || len(data) > recordContentBudget
}

// A long transcript is a real m.file child, not an MXC anchor in the preview.
func (s *Server) limitRecordContent(ctx context.Context, content *event.MessageEventContent, status string) *recordChild {
	if !recordContentTooLarge(content) {
		return nil
	}
	footer := "完整记录附件上传失败；这里只显示部分内容。"
	data := []byte(content.Body)
	uploadCtx, cancel := s.matrixOperationContext(ctx)
	resp, err := s.as.BotIntent().UploadBytesWithName(uploadCtx, data, "text/plain; charset=utf-8", "forwarded-record.txt")
	cancel()
	var child *recordChild
	if err == nil && resp != nil {
		if uri, ok := validMXC(resp.ContentURI.String()); ok && len(uri) <= 2048 {
			child = &recordChild{key: "transcript", content: &event.MessageEventContent{
				MsgType: event.MsgFile, Body: "完整合并聊天记录：forwarded-record.txt", FileName: "forwarded-record.txt", URL: uri,
				Info: &event.FileInfo{MimeType: "text/plain; charset=utf-8", Size: len(data)},
			}}
			footer = "完整记录：forwarded-record.txt（下方原生文件，回复本记录）。"
		}
	}
	previewRecordContent(content, footer+status)
	return child
}

func recordShortText(text string, maxBytes int) string {
	text = strings.ToValidUTF8(text, "\uFFFD")
	if len(text) <= maxBytes {
		return text
	}
	length := maxBytes - len("…")
	for length > 0 && !utf8.ValidString(text[:length]) {
		length--
	}
	return text[:length] + "…"
}

func previewRecordContent(content *event.MessageEventContent, footer string) {
	full := content.Body
	length := min(len(full), 4096)
	for {
		for length > 0 && !utf8.ValidString(full[:length]) {
			length--
		}
		preview := full[:length] + "\n\n[记录较长，以上仅为预览]"
		content.Body = preview + "\n" + footer
		content.Format = event.FormatHTML
		content.FormattedBody = "<p>" + recordHTML(preview) + "</p><p>" + recordHTML(footer) + "</p>"
		if !recordContentTooLarge(content) || length == 0 {
			return
		}
		length /= 2
	}
}

func recordChildTransactionID(account remoteapi.UserID, rec portalRecord, message *remoteapi.InboundMessage, rootID id.EventID, key string) string {
	// JSON encoding avoids ambiguous delimiters in untrusted opaque IDs.
	data, _ := json.Marshal([]string{string(account), string(rec.ChatID), string(rec.RoomID), string(message.ID), string(rootID), key})
	sum := sha256.Sum256(data)
	return fmt.Sprintf("wechat-record-%x", sum)
}

func (s *Server) bridgeRecordToPortal(ctx context.Context, account remoteapi.UserID, rec portalRecord, m *remoteapi.InboundMessage, client *mautrix.Client) {
	content, children, status := s.uploadRecordMedia(ctx, m.Record)
	s.attachInboundReply(account, rec, m, content)
	if transcript := s.limitRecordContent(ctx, content, status); transcript != nil {
		children = append(children, *transcript)
	}
	rootCtx, cancel := s.matrixOperationContext(ctx)
	resp, err := client.SendMessageEvent(rootCtx, rec.RoomID, event.EventMessage, content, mautrix.ReqSendEvent{Timestamp: m.Timestamp.UnixMilli()})
	cancel()
	if err != nil || resp == nil || resp.EventID == "" {
		log.Printf("failed to confirm merged record root in %s", rec.RoomID)
		s.sendPortalNotice(ctx, rec.RoomID, "桥接合并聊天记录失败（未确认根消息）。")
		return
	}
	rootID := resp.EventID
	s.rememberMessageReference(account, rec, m, rootID, client)
	for _, msgID := range messageIDAliases(m) {
		s.rememberMessageEvent(m.Chat, msgID, rootID)
	}
	s.sendRecordChildren(ctx, account, rec, m, client, rootID, children)
}

func (s *Server) sendRecordChildren(ctx context.Context, account remoteapi.UserID, rec portalRecord, m *remoteapi.InboundMessage, client *mautrix.Client, rootID id.EventID, children []recordChild) {
	var relatedIDs []id.EventID
	send := func(child recordChild) bool {
		child.content.RelatesTo = (&event.RelatesTo{}).SetReplyTo(rootID)
		sendCtx, cancel := s.matrixOperationContext(ctx)
		resp, err := client.SendMessageEvent(sendCtx, rec.RoomID, event.EventMessage, child.content, mautrix.ReqSendEvent{
			TransactionID: recordChildTransactionID(account, rec, m, rootID, child.key),
			Timestamp:     m.Timestamp.UnixMilli(),
		})
		cancel()
		if err != nil || resp == nil || resp.EventID == "" {
			return false
		}
		relatedIDs = append(relatedIDs, resp.EventID)
		// Persist after EACH confirmed child, including a failure notice, so a
		// restart midway through the bundle doesn't orphan earlier attachments.
		s.rememberMessageReference(account, rec, m, rootID, client, relatedIDs...)
		return true
	}
	for _, child := range children {
		if !send(child) {
			notice := recordChild{key: child.key + "-failure", content: &event.MessageEventContent{
				MsgType: event.MsgNotice, Body: "原生附件发送未确认：" + child.content.Body + "（其他附件继续发送）",
			}}
			if !send(notice) {
				log.Printf("failed to confirm merged record attachment and failure notice in %s (%s)", rec.RoomID, child.key)
			}
		}
	}
}
