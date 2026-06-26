package wechat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"log"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

// WeChat MsgType values (subset). See protocol docs.
const (
	wxText         = 1
	wxImage        = 3
	wxVoice        = 34
	wxVideo        = 43
	wxEmoji        = 47
	wxLocation     = 48
	wxApp          = 49 // links, files, mini-programs, quotes (parsed from XML)
	wxStatusNotify = 51
	wxRevoke       = 10002
)

func shouldDropLocal(m localMessage) bool {
	content := stripLastMessageOps(firstNonEmpty(m.ContentText, m.Content, m.PushContent))
	if m.MsgType == wxStatusNotify || content == "" {
		return true
	}
	if strings.HasPrefix(m.Talker, "@placeholder") || m.Talker == "" {
		return true
	}
	return false
}

func (c *Client) convertLocal(ctx context.Context, m localMessage) (remoteapi.Event, bool) {
	content := stripLastMessageOps(firstNonEmpty(m.ContentText, m.Content, m.PushContent))
	if content == "" {
		return remoteapi.Event{}, false
	}
	chat := m.Talker
	if chat == "" {
		if m.IsGroup {
			chat = m.FromUserName
		} else if m.IsSendBySelf {
			chat = m.ToUserName
		} else {
			chat = m.FromUserName
		}
	}
	sender := m.SenderUserName
	if sender == "" {
		if m.IsSendBySelf {
			sender = c.selfWxid()
		} else {
			sender = chat
		}
	}
	base := remoteapi.InboundMessage{
		ID:        remoteapi.MessageID(itoa(m.NewMsgID)),
		Chat:      remoteapi.ChatID(chat),
		Sender:    remoteapi.UserID(sender),
		Text:      content,
		Timestamp: time.Unix(m.CreateTime, 0),
		Extra: map[string]any{
			"msg_type":      m.MsgType,
			"msg_type_name": m.MsgTypeName,
			"msg_source":    m.MsgSource,
			"raw_msg_id":    m.MsgID,
			"msg_seq":       m.MsgSeq,
			"is_local":      true,
		},
	}
	applyMessageType(&base, m.MsgType, content, m.PushContent)
	if m.MsgType == wxImage {
		c.attachImageMedia(ctx, &base)
	}
	if base.Type == remoteapi.MsgFile {
		c.attachFileMedia(ctx, &base, fileMessageDownloadReq{Talker: chat, SenderUserName: sender, MsgID: m.MsgID, NewMsgID: m.NewMsgID, MsgSeq: m.MsgSeq, MsgType: m.MsgType, SourceXml: content, Xml: content})
	}
	if m.MsgType == wxEmoji {
		c.attachEmojiMedia(ctx, &base, content)
	}
	return remoteapi.Event{Kind: remoteapi.EventMessage, Message: &base}, true
}

// convertInbound maps a raw WeChat message into a neutral remoteapi.Event.
// Returns ok=false for messages the bridge should drop (system noise).
func (c *Client) convertInbound(ctx context.Context, m rawMessage) (remoteapi.Event, bool) {
	from := m.FromUserName.Str
	to := m.ToUserName.Str
	content := stripLastMessageOps(firstNonEmpty(m.ContentText, m.Content.Str, m.PushContent))
	if shouldDropWebhookNoise(m, content) {
		return remoteapi.Event{}, false
	}
	messageID := m.NewMsgId
	if messageID == 0 {
		messageID = m.NewMsgID
	}
	rawID := m.MsgId
	if rawID == 0 {
		rawID = m.MsgID
	}

	// Determine chat + real sender. Callback pushes may use the local-message
	// shape (Talker/SenderUserName/ContentText), while /Msg/Sync uses raw
	// FromUserName/ToUserName plus group sender prefixes in Content.
	chat := firstNonEmpty(m.Talker, from)
	sender := firstNonEmpty(m.SenderUserName, from)
	if chat == "" {
		if m.IsGroup {
			chat = from
		} else if m.IsSendBySelf {
			chat = to
		} else {
			chat = from
		}
	}
	if sender == "" {
		if m.IsSendBySelf {
			sender = c.selfWxid()
		} else {
			sender = chat
		}
	}
	if strings.HasSuffix(chat, "@chatroom") && m.SenderUserName == "" {
		if idx := strings.Index(content, ":\n"); idx > 0 {
			sender = content[:idx]
			content = content[idx+2:]
		} else if idx := strings.Index(content, ":"); idx > 0 && strings.HasPrefix(content[:idx], "wxid_") {
			sender = content[:idx]
			content = strings.TrimSpace(content[idx+1:])
		}
	} else if from != "" && from == c.selfWxid() && m.Talker == "" {
		// Outgoing echo: the conversation is the recipient.
		chat = to
		sender = from
	}

	base := remoteapi.InboundMessage{
		ID:        remoteapi.MessageID(itoa(messageID)),
		Chat:      remoteapi.ChatID(chat),
		Sender:    remoteapi.UserID(sender),
		Text:      content,
		Timestamp: time.Unix(m.CreateTime, 0),
		Extra: map[string]any{
			"msg_type":   m.MsgType,
			"msg_source": m.MsgSource,
			"raw_msg_id": rawID,
		},
	}

	if m.MsgType == wxRevoke {
		rev := parseRevokeMessage(content)
		if rev.Chat != "" {
			chat = rev.Chat
		}
		if rev.By != "" {
			sender = rev.By
		}
		return remoteapi.Event{
			Kind: remoteapi.EventRedaction,
			Redaction: &remoteapi.Redaction{
				Chat:      remoteapi.ChatID(chat),
				By:        remoteapi.UserID(sender),
				Target:    remoteapi.MessageID(firstNonEmpty(rev.MsgID, rev.OldMsgID)),
				Text:      rev.Text,
				Timestamp: time.Unix(m.CreateTime, 0),
			},
		}, true
	}
	applyMessageType(&base, m.MsgType, content, m.PushContent)
	if m.MsgType == wxImage {
		c.attachImageMedia(ctx, &base)
	}
	if base.Type == remoteapi.MsgFile {
		c.attachFileMedia(ctx, &base, fileMessageDownloadReq{Talker: chat, SenderUserName: sender, MsgID: rawID, NewMsgID: messageID, MsgSeq: m.MsgSeq, MsgType: m.MsgType, SourceXml: content, Xml: content})
	}
	if m.MsgType == wxEmoji {
		c.attachEmojiMedia(ctx, &base, content)
	}

	return remoteapi.Event{Kind: remoteapi.EventMessage, Message: &base}, true
}

func (c *Client) attachFileMedia(ctx context.Context, base *remoteapi.InboundMessage, req fileMessageDownloadReq) {
	if c == nil || c.t == nil || base == nil {
		return
	}
	payload := parseFileAttachmentPayload(base.Text, req.SourceXml, req.SenderUserName, req.Talker)
	data, mimeType, err := c.downloadFile(ctx, payload)
	if err != nil {
		log.Printf("failed to download WeChat file %s: %v", base.ID, err)
		return
	}
	fileName := payload.Title
	if fileName == "" {
		fileName = fileNameFromFileMessage(base.Text, req.SourceXml)
	}
	if mimeType == "" {
		mimeType = mimeTypeForFile(fileName, data)
	}
	base.Media = &remoteapi.Media{Data: data, MimeType: mimeType, FileName: fileName, Size: int64(len(data))}
}

type fileAttachmentPayload struct {
	Title    string
	AppID    string
	AttachID string
	TotalLen int64
	FileExt  string
	UserName string
}

func parseFileAttachmentPayload(text, sourceXML, sender, talker string) fileAttachmentPayload {
	payload := fileAttachmentPayload{
		Title:    fileNameFromFileMessage(text, sourceXML),
		AppID:    firstNonEmpty(xmlAttr(sourceXML, "appid"), xmlTag(sourceXML, "appid")),
		AttachID: firstNonEmpty(xmlTag(sourceXML, "attachid"), xmlTag(sourceXML, "cdnattachid")),
		TotalLen: int64Value(xmlTag(sourceXML, "totallen")),
		FileExt:  xmlTag(sourceXML, "fileext"),
		UserName: firstNonEmpty(xmlTag(sourceXML, "fromusername"), xmlTag(sourceXML, "fromuser"), sender, talker),
	}
	return payload
}

func (c *Client) downloadFile(ctx context.Context, payload fileAttachmentPayload) ([]byte, string, error) {
	c.mu.RLock()
	wxid := c.wxid
	c.mu.RUnlock()
	if wxid == "" {
		return nil, "", fmt.Errorf("missing wxid")
	}
	if payload.AttachID == "" || payload.TotalLen <= 0 {
		return nil, "", fmt.Errorf("missing file attach_id or total length")
	}
	const chunkSize int64 = 65536
	var out []byte
	for start := int64(0); start < payload.TotalLen; start += chunkSize {
		n := chunkSize
		if remaining := payload.TotalLen - start; remaining < n {
			n = remaining
		}
		req := downloadFileReq{
			Wxid:     wxid,
			AppID:    payload.AppID,
			AttachId: payload.AttachID,
			DataLen:  payload.TotalLen,
			UserName: payload.UserName,
			Section:  downloadSectionReq{StartPos: start, DataLen: n},
		}
		env, err := c.t.postEnvelope(ctx, "/Tools/DownloadFile", req, nil)
		if err != nil {
			return nil, "", err
		}
		chunk, _, err := mediaBytesFromEnvelope(env)
		if err != nil {
			return nil, "", err
		}
		out = append(out, chunk...)
	}
	return out, mimeTypeForFile(payload.Title, out), nil
}

func fileNameFromFileMessage(text, sourceXML string) string {
	if title := strings.TrimSpace(xmlTag(sourceXML, "title")); title != "" {
		return title
	}
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	if line != "" {
		return line
	}
	return "wechat-file"
}

func mimeTypeForFile(fileName string, data []byte) string {
	if idx := strings.LastIndex(fileName, "."); idx >= 0 {
		if mt := mime.TypeByExtension(fileName[idx:]); mt != "" {
			return mt
		}
	}
	if len(data) > 0 {
		return http.DetectContentType(data)
	}
	return "application/octet-stream"
}

func (c *Client) attachImageMedia(ctx context.Context, base *remoteapi.InboundMessage) {
	if c == nil || c.t == nil || base == nil || base.Media == nil {
		return
	}
	aesKey := extraString(base.Extra, "image_aeskey")
	fileNo := extraString(base.Extra, "image_file_no")
	if aesKey == "" || fileNo == "" {
		return
	}
	data, mimeType, err := c.downloadCDNImage(ctx, aesKey, fileNo)
	if err != nil {
		log.Printf("failed to download WeChat image %s file_no=%s: %v", base.ID, fileNo, err)
		return
	}
	base.Media.Data = data
	base.Media.Size = int64(len(data))
	if mimeType != "" {
		base.Media.MimeType = mimeType
	}
}

func (c *Client) downloadCDNImage(ctx context.Context, aesKey, fileNo string) ([]byte, string, error) {
	c.mu.RLock()
	wxid := c.wxid
	c.mu.RUnlock()
	if wxid == "" {
		return nil, "", fmt.Errorf("missing wxid")
	}
	env, err := c.t.postEnvelope(ctx, "/Tools/CdnDownloadImage", cdnDownloadImageReq{Wxid: wxid, FileAesKey: aesKey, FileNo: fileNo}, nil)
	if err != nil {
		return nil, "", err
	}
	return mediaBytesFromEnvelope(env)
}

func mediaBytesFromEnvelope(env *Envelope) ([]byte, string, error) {
	if env == nil {
		return nil, "", fmt.Errorf("empty response")
	}
	if strings.TrimSpace(env.Data62) != "" {
		return decodeMediaString(env.Data62)
	}
	return mediaBytesFromJSON(env.Data)
}

func mediaBytesFromJSON(raw json.RawMessage) ([]byte, string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, "", fmt.Errorf("response has no media data")
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return decodeMediaString(s)
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err == nil {
		for _, key := range []string{"Base64", "Data", "ImageBase64", "FileData", "Buffer", "Content"} {
			if v, ok := obj[key].(string); ok && strings.TrimSpace(v) != "" {
				return decodeMediaString(v)
			}
		}
	}
	return nil, "", fmt.Errorf("unsupported media response shape")
}

func decodeMediaString(s string) ([]byte, string, error) {
	s = strings.TrimSpace(s)
	mimeType := ""
	if idx := strings.Index(s, ","); strings.HasPrefix(s, "data:") && idx > 0 {
		meta := s[len("data:"):idx]
		if semi := strings.Index(meta, ";"); semi >= 0 {
			mimeType = meta[:semi]
		} else {
			mimeType = meta
		}
		s = s[idx+1:]
	}
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, "", err
	}
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}
	return data, mimeType, nil
}

func extraString(extra map[string]any, key string) string {
	if extra == nil {
		return ""
	}
	v := extra[key]
	if v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

func shouldDropWebhookNoise(m rawMessage, content string) bool {
	return m.MsgType == wxStatusNotify || content == ""
}

func stripLastMessageOps(content string) string {
	for strings.Contains(content, "<name>lastMessage</name>") {
		start := strings.LastIndex(content[:strings.Index(content, "<name>lastMessage</name>")], "<msg>")
		if start < 0 {
			break
		}
		endRel := strings.Index(content[start:], "</msg>")
		if endRel < 0 {
			break
		}
		end := start + endRel + len("</msg>")
		content = content[:start] + content[end:]
	}
	return strings.TrimSpace(content)
}

func applyMessageType(base *remoteapi.InboundMessage, msgType int, content, push string) {
	switch msgType {
	case wxText:
		base.Type = remoteapi.MsgText
	case wxImage:
		base.Type = remoteapi.MsgImage
		base.Media = &remoteapi.Media{MimeType: "image/jpeg", FileName: string(base.ID) + ".jpg"}
		if isXMLMessage(base.Text) {
			if aesKey := xmlAttr(base.Text, "aeskey"); aesKey != "" {
				base.Extra["image_aeskey"] = aesKey
			}
			if fileNo := firstNonEmpty(xmlAttr(base.Text, "cdnmidimgurl"), xmlAttr(base.Text, "cdnthumburl")); fileNo != "" {
				base.Extra["image_file_no"] = fileNo
			}
		}
		if base.Text == "" || isXMLMessage(base.Text) {
			base.Text = firstNonEmpty(push, "[image]")
		}
	case wxVoice:
		base.Type = remoteapi.MsgAudio
		base.Media = &remoteapi.Media{MimeType: "audio/amr"}
		base.Text = firstNonEmpty(push, "[voice]")
	case wxVideo:
		base.Type = remoteapi.MsgVideo
		base.Media = &remoteapi.Media{MimeType: "video/mp4"}
		base.Text = firstNonEmpty(push, "[video]")
	case wxEmoji:
		base.Type = remoteapi.MsgEmoji
		base.Text = firstNonEmpty(base.Text, push, "[emoji]")
	case wxLocation:
		base.Type = remoteapi.MsgLocation
	case wxApp:
		base.Type, base.Text = parseAppMessage(content, push)
		if base.Text == "" {
			base.Text = "[app]"
		}
	default:
		base.Type = remoteapi.MsgApp
		base.Text = firstNonEmpty(base.Text, push, "[message]")
	}
}

func parseAppMessage(content, push string) (remoteapi.MessageType, string) {
	// WeChat app messages are usually XML: <msg><appmsg><title>...</title>...
	// If the API only gives PushContent, fall back to that instead of showing
	// a useless [app] placeholder.
	if !strings.Contains(content, "<appmsg") && !strings.Contains(content, "<msg") {
		return remoteapi.MsgApp, strings.TrimSpace(firstNonEmpty(content, push))
	}
	title := xmlTag(content, "title")
	des := xmlTag(content, "des")
	url := xmlTag(content, "url")
	fileExt := xmlTag(content, "fileext")
	appType := xmlTag(content, "type")

	var typ remoteapi.MessageType
	switch appType {
	case "5", "33", "36": // link / mini program-ish
		typ = remoteapi.MsgLink
	case "6":
		typ = remoteapi.MsgFile
	case "19":
		typ = remoteapi.MsgApp // merged forward / chat history
	default:
		typ = remoteapi.MsgApp
	}

	parts := make([]string, 0, 4)
	if title != "" {
		parts = append(parts, title)
	}
	if des != "" && des != title {
		parts = append(parts, des)
	}
	if fileExt != "" && typ == remoteapi.MsgFile {
		parts = append(parts, "file type: "+fileExt)
	}
	if url != "" {
		parts = append(parts, url)
	}
	if len(parts) == 0 {
		fallback := strings.TrimSpace(push)
		if fallback == "" || strings.Contains(fallback, "<appmsg") {
			fallback = "[app]"
		}
		return typ, fallback
	}
	return typ, strings.Join(parts, "\n")
}

func isXMLMessage(content string) bool {
	content = strings.TrimSpace(content)
	return strings.HasPrefix(content, "<msg") || strings.HasPrefix(content, "<?xml")
}

func xmlAttr(raw, attr string) string {
	for _, quote := range []string{"\"", "'"} {
		needle := attr + "=" + quote
		start := strings.Index(raw, needle)
		if start < 0 {
			continue
		}
		start += len(needle)
		end := strings.Index(raw[start:], quote)
		if end < 0 {
			return ""
		}
		return strings.TrimSpace(html.UnescapeString(raw[start : start+end]))
	}
	return ""
}

type revokeInfo struct {
	Chat     string
	By       string
	MsgID    string
	OldMsgID string
	Text     string
}

func parseRevokeMessage(raw string) revokeInfo {
	dec := xml.NewDecoder(strings.NewReader(raw))
	var info revokeInfo
	var current string
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			current = strings.ToLower(t.Name.Local)
		case xml.CharData:
			value := strings.TrimSpace(html.UnescapeString(string(t)))
			if value == "" {
				continue
			}
			switch current {
			case "session":
				info.Chat = value
			case "fromusername":
				info.By = value
			case "msgid":
				info.MsgID = value
			case "oldmsgid":
				info.OldMsgID = value
			case "replacemsg":
				info.Text = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(value, "<![CDATA["), "]]>"))
			}
		case xml.EndElement:
			current = ""
		}
	}
	return info
}

func xmlTag(raw, tag string) string {
	start := strings.Index(raw, "<"+tag+">")
	endTag := "</" + tag + ">"
	if start < 0 {
		return ""
	}
	start += len(tag) + 2
	end := strings.Index(raw[start:], endTag)
	if end < 0 {
		return ""
	}
	value := strings.TrimSpace(html.UnescapeString(raw[start : start+end]))
	value = strings.TrimPrefix(value, "<![CDATA[")
	value = strings.TrimSuffix(value, "]]>")
	return strings.TrimSpace(value)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func (c *Client) selfWxid() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.wxid
}

func itoa(v int64) string {
	const digits = "0123456789"
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = digits[v%10]
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
