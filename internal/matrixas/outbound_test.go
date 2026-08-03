package matrixas

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/leat/mautrix-go-wechat/internal/config"
	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

type portalMessageAPI struct {
	remoteapi.PuppetAPI
	sent []remoteapi.OutgoingMessage
}

func (a *portalMessageAPI) SendMessage(_ context.Context, _ remoteapi.ChatID, msg remoteapi.OutgoingMessage) (remoteapi.MessageID, error) {
	a.sent = append(a.sent, msg)
	return "sent", nil
}

func newPortalMessageTestServer(t *testing.T, api *portalMessageAPI) (*Server, portalRecord) {
	t.Helper()
	homeserver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"room_id":"!portal:example.com","event_id":"$notice"}`))
	}))
	t.Cleanup(homeserver.Close)
	as := appservice.Create()
	as.Registration = &appservice.Registration{SenderLocalpart: "wechatbot", AppToken: "test-token"}
	as.HomeserverDomain = "example.com"
	if err := as.SetHomeserverURL(homeserver.URL); err != nil {
		t.Fatalf("set homeserver URL: %v", err)
	}
	rec := portalRecord{ChatID: "wxid_friend", RoomID: "!portal:example.com"}
	return &Server{
		cfg: &config.Config{},
		as:  as,
		api: api,
		portals: &portalStore{data: portalState{
			Portals: map[string]portalRecord{string(rec.ChatID): rec},
			Pending: map[string]pendingPortal{},
		}},
	}, rec
}

func portalTextEvent(rec portalRecord, body string) *event.Event {
	return &event.Event{
		Sender: id.UserID("@alice:example.com"),
		Type:   event.EventMessage,
		RoomID: rec.RoomID,
		Content: event.Content{Parsed: &event.MessageEventContent{
			MsgType: event.MsgText,
			Body:    body,
		}},
	}
}

func TestPortalMessageMapsWechatEmojiAfterCommandDetectionAndPreservesWhitespace(t *testing.T) {
	api := &portalMessageAPI{}
	s, rec := newPortalMessageTestServer(t, api)

	s.handleMatrixEvent(context.Background(), portalTextEvent(rec, "  before :wechat_smile: after  "))

	if len(api.sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(api.sent))
	}
	want := "  before [微笑] after  "
	if api.sent[0].Type != remoteapi.MsgText || api.sent[0].Text != want {
		t.Fatalf("unexpected outbound message: got %#v want text %q", api.sent[0], want)
	}
}

func TestPortalMessageCommandWithEmojiNeverLeaksToWechat(t *testing.T) {
	api := &portalMessageAPI{}
	s, rec := newPortalMessageTestServer(t, api)
	s.handleMatrixEvent(context.Background(), portalTextEvent(rec, "  !wx help :wechat_smile:  "))

	if len(api.sent) != 0 {
		t.Fatalf("portal command leaked to WeChat: %#v", api.sent)
	}
}

func TestPortalMessageWhitespaceOnlyDoesNotSend(t *testing.T) {
	api := &portalMessageAPI{}
	s, rec := newPortalMessageTestServer(t, api)

	s.handleMatrixEvent(context.Background(), portalTextEvent(rec, " \t\n "))

	if len(api.sent) != 0 {
		t.Fatalf("whitespace-only portal message was sent: %#v", api.sent)
	}
}

func TestPortalMediaToRemoteMessageDoesNotMapWechatEmojiInCaptions(t *testing.T) {
	const caption = ":wechat_smile:"
	tests := []struct {
		name      string
		data      []byte
		mimeType  string
		fileName  string
		msgType   event.MessageType
		eventType event.Type
	}{
		{name: "Image", data: []byte("image"), mimeType: "image/jpeg", fileName: "image.jpg", msgType: event.MsgImage, eventType: event.EventMessage},
		{name: "Video", data: []byte("video"), mimeType: "video/mp4", fileName: "video.mp4", msgType: event.MsgVideo, eventType: event.EventMessage},
		{name: "Audio", data: []byte("audio"), mimeType: "audio/wav", fileName: "audio.wav", msgType: event.MsgAudio, eventType: event.EventMessage},
		{name: "File", data: []byte("file"), mimeType: "application/octet-stream", fileName: "file.bin", msgType: event.MsgFile, eventType: event.EventMessage},
		{name: "Emoji", data: []byte("GIF89a..."), mimeType: "image/gif", fileName: "emoji.gif", msgType: event.CapMsgSticker, eventType: event.EventSticker},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg, err := portalMediaToRemoteMessage(tt.data, tt.mimeType, tt.fileName, caption, tt.msgType, tt.eventType)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if msg.Text != caption {
				t.Fatalf("media caption changed: got %q want %q", msg.Text, caption)
			}
		})
	}
}

func TestPortalMediaToRemoteMessageSendsMatrixImageAsWechatImage(t *testing.T) {
	data := []byte{0xff, 0xd8, 0xff, 0xdb}
	msg, err := portalMediaToRemoteMessage(data, "image/jpeg", "photo.jpg", "photo", event.MsgImage, event.EventMessage)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.Type != remoteapi.MsgImage {
		t.Fatalf("expected Matrix image to become WeChat image, got %v", msg.Type)
	}
	if msg.Media == nil || string(msg.Media.Data) != string(data) || msg.Media.MimeType != "image/jpeg" || msg.Media.FileName != "photo.jpg" {
		t.Fatalf("unexpected media: %#v", msg.Media)
	}
}

func TestPortalMediaToRemoteMessageSendsMatrixAudioAsWechatAudio(t *testing.T) {
	data := []byte("wav-bytes")
	msg, err := portalMediaToRemoteMessage(data, "audio/wav", "voice.wav", "voice", event.MsgAudio, event.EventMessage)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.Type != remoteapi.MsgAudio {
		t.Fatalf("expected Matrix audio to become WeChat audio, got %v", msg.Type)
	}
	if msg.Media == nil || string(msg.Media.Data) != string(data) || msg.Media.MimeType != "audio/wav" || msg.Media.FileName != "voice.wav" {
		t.Fatalf("unexpected media: %#v", msg.Media)
	}
}

func TestPortalMediaToRemoteMessageSendsMatrixVideoAsWechatVideo(t *testing.T) {
	data := []byte{0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p'}
	msg, err := portalMediaToRemoteMessage(data, "video/mp4", "clip.mp4", "clip", event.MsgVideo, event.EventMessage)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.Type != remoteapi.MsgVideo {
		t.Fatalf("expected Matrix video to become WeChat video, got %v", msg.Type)
	}
	if msg.Media == nil || string(msg.Media.Data) != string(data) || msg.Media.MimeType != "video/mp4" || msg.Media.FileName != "clip.mp4" {
		t.Fatalf("unexpected media: %#v", msg.Media)
	}
}

func TestPortalMediaToRemoteMessageSendsMatrixFileAsWechatFile(t *testing.T) {
	data := []byte("zip-bytes")
	msg, err := portalMediaToRemoteMessage(data, "application/zip", "archive.zip", "archive.zip", event.MsgFile, event.EventMessage)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.Type != remoteapi.MsgFile {
		t.Fatalf("expected Matrix file to become WeChat file, got %v", msg.Type)
	}
	if msg.Media == nil || string(msg.Media.Data) != string(data) || msg.Media.MimeType != "application/zip" || msg.Media.FileName != "archive.zip" {
		t.Fatalf("unexpected media: %#v", msg.Media)
	}
}

func TestPortalMediaToRemoteMessageRequiresGIFOnlyForStickers(t *testing.T) {
	_, err := portalMediaToRemoteMessage([]byte{0xff, 0xd8}, "image/jpeg", "sticker.jpg", "sticker", event.CapMsgSticker, event.EventSticker)
	if err == nil {
		t.Fatalf("expected non-GIF sticker to be rejected")
	}

	gif := []byte("GIF89a...")
	msg, err := portalMediaToRemoteMessage(gif, "image/gif", "sticker.gif", "", event.CapMsgSticker, event.EventSticker)
	if err != nil {
		t.Fatalf("unexpected GIF sticker error: %v", err)
	}
	if msg.Type != remoteapi.MsgEmoji {
		t.Fatalf("expected Matrix sticker to become WeChat emoji, got %v", msg.Type)
	}
	if msg.Text != "[emoji]" {
		t.Fatalf("expected default emoji body, got %q", msg.Text)
	}
}
