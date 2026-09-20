package matrixas

import (
	"fmt"
	"strings"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"github.com/leat/mautrix-go-wechat/internal/wechat/textemoji"
)

func matrixEventTypeFromRemote(m *remoteapi.InboundMessage) event.Type {
	if m.Type == remoteapi.MsgEmoji && m.Media != nil && len(m.Media.Data) > 0 {
		return event.EventSticker
	}
	return event.EventMessage
}

func matrixContentFromRemote(m *remoteapi.InboundMessage) *event.MessageEventContent {
	body := m.Text
	if body == "" {
		body = fmt.Sprintf("[%s]", remoteTypeName(m.Type))
	}
	switch m.Type {
	case remoteapi.MsgText:
		return &event.MessageEventContent{MsgType: event.MsgText, Body: textemoji.ToShortcodes(body)}
	case remoteapi.MsgImage:
		if m.Media != nil && len(m.Media.Data) > 0 {
			return &event.MessageEventContent{MsgType: event.MsgImage, Body: body}
		}
		return &event.MessageEventContent{MsgType: event.MsgText, Body: body}
	case remoteapi.MsgVideo:
		if m.Media != nil && len(m.Media.Data) > 0 {
			return &event.MessageEventContent{MsgType: event.MsgVideo, Body: body}
		}
		return &event.MessageEventContent{MsgType: event.MsgText, Body: body}
	case remoteapi.MsgAudio:
		if m.Media != nil && len(m.Media.Data) > 0 {
			return &event.MessageEventContent{MsgType: event.MsgAudio, Body: body}
		}
		return &event.MessageEventContent{MsgType: event.MsgText, Body: body}
	case remoteapi.MsgFile:
		if m.Media != nil && len(m.Media.Data) > 0 {
			return &event.MessageEventContent{MsgType: event.MsgFile, Body: body}
		}
		return &event.MessageEventContent{MsgType: event.MsgText, Body: body}
	case remoteapi.MsgLocation:
		if geoURI := m.Location.GeoURI(); geoURI != "" {
			return &event.MessageEventContent{MsgType: event.MsgLocation, Body: body, GeoURI: geoURI}
		}
		return &event.MessageEventContent{MsgType: event.MsgText, Body: body}
	case remoteapi.MsgLink, remoteapi.MsgApp, remoteapi.MsgContactCard:
		return &event.MessageEventContent{MsgType: event.MsgText, Body: body}
	case remoteapi.MsgEmoji:
		if m.Media != nil && len(m.Media.Data) > 0 {
			// m.sticker events don't have a msgtype in the Matrix spec. Cinny is
			// tolerant of msgtype=m.sticker here, but Element may ignore those
			// events, so leave MsgType empty and let the event type carry sticker.
			return &event.MessageEventContent{Body: body}
		}
		return &event.MessageEventContent{MsgType: event.MsgText, Body: body}
	default:
		return &event.MessageEventContent{MsgType: event.MsgNotice, Body: body}
	}
}

func shouldUploadRemoteMedia(m *remoteapi.InboundMessage, eventType event.Type, content *event.MessageEventContent) bool {
	return m != nil && content != nil && m.Media != nil && len(m.Media.Data) > 0 && (content.MsgType.IsMedia() || eventType == event.EventSticker)
}

func wechatStickerImageFromRemoteEmoji(m *remoteapi.InboundMessage, mxcURL id.ContentURIString) (wechatStickerImage, bool) {
	if m == nil || m.Type != remoteapi.MsgEmoji || mxcURL == "" {
		return wechatStickerImage{}, false
	}
	key := remoteExtraString(m.Extra, "file_md5")
	if key == "" {
		key = remoteExtraString(m.Extra, "emoji_md5")
	}
	if key == "" {
		key = string(m.ID)
	}
	shortcode := normalizeStickerShortcode("wechat_" + key)
	if shortcode == "" {
		return wechatStickerImage{}, false
	}
	body := strings.TrimSpace(m.Text)
	if body == "" {
		body = shortcode
	}
	return wechatStickerImage{Shortcode: shortcode, Body: body, URL: mxcURL}, true
}

func remoteExtraString(extra map[string]any, key string) string {
	if extra == nil {
		return ""
	}
	v := extra[key]
	if v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

func messageIDAliases(m *remoteapi.InboundMessage) []remoteapi.MessageID {
	if m == nil {
		return nil
	}
	aliases := make([]remoteapi.MessageID, 0, 2)
	seen := map[remoteapi.MessageID]bool{}
	add := func(msg remoteapi.MessageID) {
		if msg == "" || seen[msg] {
			return
		}
		seen[msg] = true
		aliases = append(aliases, msg)
	}
	add(m.ID)
	if m.Extra != nil {
		if raw := remoteExtraString(m.Extra, "raw_msg_id"); raw != "" {
			add(remoteapi.MessageID(raw))
		}
	}
	return aliases
}

func stickerPackTargetRooms(space, portal id.RoomID) []id.RoomID {
	if space == "" {
		return nil
	}
	return []id.RoomID{space}
}

func stickerPackCleanupRooms(space, portal id.RoomID) []id.RoomID {
	if portal == "" || portal == space {
		return nil
	}
	return []id.RoomID{portal}
}

func remoteTypeName(t remoteapi.MessageType) string {
	switch t {
	case remoteapi.MsgText:
		return "text"
	case remoteapi.MsgImage:
		return "image"
	case remoteapi.MsgVideo:
		return "video"
	case remoteapi.MsgAudio:
		return "audio"
	case remoteapi.MsgFile:
		return "file"
	case remoteapi.MsgLocation:
		return "location"
	case remoteapi.MsgContactCard:
		return "contact"
	case remoteapi.MsgLink:
		return "link"
	case remoteapi.MsgEmoji:
		return "emoji"
	case remoteapi.MsgApp:
		return "app"
	default:
		return "message"
	}
}
