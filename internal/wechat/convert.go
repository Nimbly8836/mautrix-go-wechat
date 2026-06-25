package wechat

import (
	"context"
	"encoding/xml"
	"html"
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
	if m.MsgType == wxEmoji {
		c.attachEmojiMedia(ctx, &base, content)
	}

	return remoteapi.Event{Kind: remoteapi.EventMessage, Message: &base}, true
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
		base.Media = &remoteapi.Media{MimeType: "image/jpeg"}
		if base.Text == "" || strings.HasPrefix(base.Text, "<msg") {
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
