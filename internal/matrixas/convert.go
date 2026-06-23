package matrixas

import (
	"fmt"

	"maunium.net/go/mautrix/event"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

func matrixContentFromRemote(m *remoteapi.InboundMessage) *event.MessageEventContent {
	body := m.Text
	if body == "" {
		body = fmt.Sprintf("[%s]", remoteTypeName(m.Type))
	}
	switch m.Type {
	case remoteapi.MsgText:
		return &event.MessageEventContent{MsgType: event.MsgText, Body: body}
	case remoteapi.MsgImage:
		return &event.MessageEventContent{MsgType: event.MsgImage, Body: body}
	case remoteapi.MsgVideo:
		return &event.MessageEventContent{MsgType: event.MsgVideo, Body: body}
	case remoteapi.MsgAudio:
		return &event.MessageEventContent{MsgType: event.MsgAudio, Body: body}
	case remoteapi.MsgFile:
		return &event.MessageEventContent{MsgType: event.MsgFile, Body: body}
	case remoteapi.MsgLocation:
		return &event.MessageEventContent{MsgType: event.MsgLocation, Body: body}
	default:
		return &event.MessageEventContent{MsgType: event.MsgNotice, Body: body}
	}
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
