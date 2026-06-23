package wechat

import (
	"strings"
	"time"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

// WeChat MsgType values (subset). See protocol docs.
const (
	wxText     = 1
	wxImage    = 3
	wxVoice    = 34
	wxVideo    = 43
	wxEmoji    = 47
	wxLocation = 48
	wxApp      = 49 // links, files, mini-programs, quotes (parsed from XML)
	wxRevoke   = 10002
)

// convertInbound maps a raw WeChat message into a neutral remoteapi.Event.
// Returns ok=false for messages the bridge should drop (system noise).
func (c *Client) convertInbound(m rawMessage) (remoteapi.Event, bool) {
	from := m.FromUserName.Str
	to := m.ToUserName.Str
	content := m.Content.Str

	// Determine chat + real sender. In group chats WeChat prefixes the body
	// with "<wxid>:\n". The chat is the @chatroom; sender is extracted.
	chat := from
	sender := from
	if strings.HasSuffix(from, "@chatroom") {
		chat = from
		if idx := strings.Index(content, ":\n"); idx > 0 {
			sender = content[:idx]
			content = content[idx+2:]
		}
	} else if isSelf := from == c.selfWxid(); isSelf {
		// Outgoing echo: the conversation is the recipient.
		chat = to
		sender = from
	}

	base := remoteapi.InboundMessage{
		ID:        remoteapi.MessageID(itoa(m.NewMsgId)),
		Chat:      remoteapi.ChatID(chat),
		Sender:    remoteapi.UserID(sender),
		Text:      content,
		Timestamp: time.Unix(m.CreateTime, 0),
		Extra: map[string]any{
			"msg_type":   m.MsgType,
			"msg_source": m.MsgSource,
			"raw_msg_id": m.MsgId,
		},
	}

	switch m.MsgType {
	case wxText:
		base.Type = remoteapi.MsgText
	case wxImage:
		base.Type = remoteapi.MsgImage
		base.Media = &remoteapi.Media{MimeType: "image/jpeg"}
	case wxVoice:
		base.Type = remoteapi.MsgAudio
		base.Media = &remoteapi.Media{MimeType: "audio/amr"}
	case wxVideo:
		base.Type = remoteapi.MsgVideo
		base.Media = &remoteapi.Media{MimeType: "video/mp4"}
	case wxEmoji:
		base.Type = remoteapi.MsgEmoji
	case wxLocation:
		base.Type = remoteapi.MsgLocation
	case wxApp:
		base.Type = remoteapi.MsgApp // caller parses XML in content
	case wxRevoke:
		// A recall notification, not a message.
		return remoteapi.Event{
			Kind: remoteapi.EventRedaction,
			Redaction: &remoteapi.Redaction{
				Chat:      remoteapi.ChatID(chat),
				By:        remoteapi.UserID(sender),
				Timestamp: time.Unix(m.CreateTime, 0),
				// Target id is embedded in the revoke XML; parse in caller.
			},
		}, true
	default:
		base.Type = remoteapi.MsgApp
	}

	return remoteapi.Event{Kind: remoteapi.EventMessage, Message: &base}, true
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
