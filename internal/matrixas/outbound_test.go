package matrixas

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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

func (a *portalMessageAPI) Self(context.Context) (remoteapi.UserID, error) {
	return "wxid_self", nil
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

func TestPortalMessageIgnoredCommandPrefixes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		body     string
		prefixes []string
		msgType  event.MessageType
		reply    bool
		blocked  bool
	}{
		{name: "default forwards commands", body: "!da_bot action send-jielong-test"},
		{name: "configured command", body: "!da_bot action send-jielong-test", prefixes: []string{"!da_bot"}, blocked: true},
		{name: "prefix alone", body: "!da_bot", prefixes: []string{"!da_bot"}, blocked: true},
		{name: "literal prefix not whole word", body: "!da_bot_extra run", prefixes: []string{"!da_bot"}, blocked: true},
		{name: "leading whitespace", body: " \t\n!da_bot run", prefixes: []string{"!da_bot"}, blocked: true},
		{name: "prefix whitespace", body: "!da_bot run", prefixes: []string{"  !da_bot  "}, blocked: true},
		{name: "multiple prefixes", body: "/tool run", prefixes: []string{"!da_bot", "/tool"}, blocked: true},
		{name: "all bang commands", body: "!another run", prefixes: []string{"!"}, blocked: true},
		{name: "notice", body: "!da_bot run", prefixes: []string{"!da_bot"}, msgType: event.MsgNotice, blocked: true},
		{name: "case sensitive", body: "!DA_BOT run", prefixes: []string{"!da_bot"}},
		{name: "not a regex", body: "!da_bot run", prefixes: []string{"!.*"}},
		{name: "other command", body: "!another run", prefixes: []string{"!da_bot"}},
		{name: "ordinary text unchanged", body: "  ordinary text  ", prefixes: []string{"!da_bot"}},
		{name: "mention in text", body: "收到消息：!da_bot run", prefixes: []string{"!da_bot"}},
		{name: "empty prefixes ignored", body: "ordinary text", prefixes: []string{"", " \t\n"}},
		{name: "reply command", body: "> <@friend:example.com> original\n\n!da_bot run", prefixes: []string{"!da_bot"}, reply: true, blocked: true},
		{name: "quoted command is not the reply", body: "> <@friend:example.com> !da_bot run\n\nordinary reply", prefixes: []string{"!da_bot"}, reply: true},
		{name: "quote without reply relation", body: "> quoted example\n\n!da_bot run", prefixes: []string{"!da_bot"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &portalMessageAPI{}
			s, rec := newPortalMessageTestServer(t, api)
			s.cfg.Bridge.IgnoredCommandPrefixes = tc.prefixes
			// Ignoring commands must be silent: no notices or extra Matrix calls.
			var requests atomic.Int64
			homeserver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"event_id":"$notice"}`))
			}))
			t.Cleanup(homeserver.Close)
			if err := s.as.SetHomeserverURL(homeserver.URL); err != nil {
				t.Fatal(err)
			}
			evt := portalTextEvent(rec, tc.body)
			msg := evt.Content.AsMessage()
			if tc.msgType != "" {
				msg.MsgType = tc.msgType
			}
			if tc.reply {
				msg.RelatesTo = &event.RelatesTo{InReplyTo: &event.InReplyTo{EventID: "$original"}}
			}
			s.handleMatrixEvent(context.Background(), evt)
			if tc.blocked {
				if len(api.sent) != 0 {
					t.Fatalf("ignored command leaked to WeChat: %#v", api.sent)
				}
			} else if len(api.sent) != 1 || api.sent[0].Type != remoteapi.MsgText || api.sent[0].Text != tc.body {
				t.Fatalf("ordinary forwarding changed: %#v", api.sent)
			}
			if requests.Load() != 0 {
				t.Fatalf("unexpected Matrix requests: %d", requests.Load())
			}
			if msg.Body != tc.body {
				t.Fatalf("filter mutated original body: %q", msg.Body)
			}
		})
	}
}

func TestPortalMessageIgnoreFilterPreservesLocalCommandsAndPermissions(t *testing.T) {
	for _, tc := range []struct {
		name   string
		prefix string
		body   string
		deny   bool
		want   string
	}{
		{name: "bridge command", body: "!wx info", want: "WeChat chat:"},
		{name: "custom bridge prefix", prefix: "!custom", body: "!custom info", want: "WeChat chat:"},
		{name: "permission gate", body: "!da_bot run", deny: true, want: "你没有使用此 bridge 的权限"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &portalMessageAPI{}
			s, rec := newPortalMessageTestServer(t, api)
			s.cfg.Bridge.CommandPrefix = tc.prefix
			s.cfg.Bridge.IgnoredCommandPrefixes = []string{"!"}
			if tc.deny {
				s.cfg.Bridge.Permissions = map[string]string{"@alice:example.com": "relay"}
			}
			messages := capturePortalMatrixMessages(t, s)
			s.handleMatrixEvent(context.Background(), portalTextEvent(rec, tc.body))
			if len(api.sent) != 0 {
				t.Fatalf("command leaked to WeChat: %#v", api.sent)
			}
			select {
			case msg := <-messages:
				if msg.MsgType != event.MsgNotice || !strings.Contains(msg.Body, tc.want) {
					t.Fatalf("unexpected notice: %#v", msg)
				}
			default:
				t.Fatal("filter swallowed a bridge command or permission notice")
			}
		})
	}
}

func TestPortalMessageIgnoreFilterDoesNotAffectSelfSentInbound(t *testing.T) {
	api := &portalMessageAPI{}
	s, rec := newPortalMessageTestServer(t, api)
	s.cfg.Bridge.IgnoredCommandPrefixes = []string{"!da_bot"}
	messages := capturePortalMatrixMessages(t, s)
	s.OnMessage(context.Background(), &remoteapi.InboundMessage{
		ID: "inbound-1", Chat: rec.ChatID, Sender: "wxid_self",
		Type: remoteapi.MsgText, Text: "!da_bot from WeChat",
	})
	select {
	case msg := <-messages:
		if msg.MsgType != event.MsgText || msg.Body != "!da_bot from WeChat" {
			t.Fatalf("inbound message changed: %#v", msg)
		}
	default:
		t.Fatal("self-sent inbound message was filtered")
	}
	if len(api.sent) != 0 {
		t.Fatalf("inbound message echoed back to WeChat: %#v", api.sent)
	}
}

func capturePortalMatrixMessages(t *testing.T, s *Server) <-chan event.MessageEventContent {
	t.Helper()
	messages := make(chan event.MessageEventContent, 8)
	homeserver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/send/m.room.message/") {
			var msg event.MessageEventContent
			if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
				t.Error(err)
			}
			select {
			case messages <- msg:
			default:
				t.Error("unexpected extra Matrix messages")
			}
		}
		_, _ = w.Write([]byte(`{"event_id":"$captured"}`))
	}))
	t.Cleanup(homeserver.Close)
	if err := s.as.SetHomeserverURL(homeserver.URL); err != nil {
		t.Fatal(err)
	}
	return messages
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
