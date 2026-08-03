package matrixas

import (
	"encoding/json"
	"testing"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

func TestMatrixContentFromRemoteEmojiWithMediaUsesSpecStickerContent(t *testing.T) {
	msg := &remoteapi.InboundMessage{
		Type:  remoteapi.MsgEmoji,
		Text:  "[emoji]",
		Media: &remoteapi.Media{Data: []byte("gif")},
	}
	content := matrixContentFromRemote(msg)
	if content.MsgType != "" {
		t.Fatalf("expected m.sticker event content to omit msgtype, got %s", content.MsgType)
	}
	if eventType := matrixEventTypeFromRemote(msg); eventType != event.EventSticker {
		t.Fatalf("expected m.sticker event type, got %s", eventType.Type)
	}
	if !shouldUploadRemoteMedia(msg, event.EventSticker, content) {
		t.Fatalf("expected m.sticker event with media to be uploaded")
	}
	raw, err := json.Marshal(content)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if _, ok := obj["msgtype"]; ok {
		t.Fatalf("expected sticker content JSON to omit msgtype, got %s", raw)
	}
}

func TestWechatStickerImageFromRemoteEmojiUsesStableMD5Shortcode(t *testing.T) {
	sticker, ok := wechatStickerImageFromRemoteEmoji(&remoteapi.InboundMessage{
		ID:   "12345",
		Type: remoteapi.MsgEmoji,
		Text: "[smile]",
		Extra: map[string]any{
			"file_md5": "ABCDEF123456",
		},
	}, id.ContentURIString("mxc://example.com/media"))
	if !ok {
		t.Fatalf("expected emoji to produce sticker image")
	}
	if sticker.Shortcode != "wechat_abcdef123456" {
		t.Fatalf("unexpected shortcode: %q", sticker.Shortcode)
	}
	if sticker.Body != "[smile]" {
		t.Fatalf("unexpected body: %q", sticker.Body)
	}
	if sticker.URL != "mxc://example.com/media" {
		t.Fatalf("unexpected URL: %q", sticker.URL)
	}
}

func TestWechatStickerImageFromRemoteEmojiRejectsNonEmoji(t *testing.T) {
	_, ok := wechatStickerImageFromRemoteEmoji(&remoteapi.InboundMessage{Type: remoteapi.MsgImage}, id.ContentURIString("mxc://example.com/media"))
	if ok {
		t.Fatalf("expected non-emoji message to be ignored")
	}
}

func TestStickerPackTargetRoomsUsesOnlyAccountSpace(t *testing.T) {
	targets := stickerPackTargetRooms(id.RoomID("!space:example.com"), id.RoomID("!portal:example.com"))
	if len(targets) != 1 || targets[0] != "!space:example.com" {
		t.Fatalf("expected only account space target, got %#v", targets)
	}
}

func TestStickerPackTargetRoomsDeduplicatesPortalSpace(t *testing.T) {
	targets := stickerPackTargetRooms(id.RoomID("!same:example.com"), id.RoomID("!same:example.com"))
	if len(targets) != 1 || targets[0] != "!same:example.com" {
		t.Fatalf("unexpected targets: %#v", targets)
	}
}

func TestStickerPackCleanupRoomsUsesOnlyPortalRoom(t *testing.T) {
	targets := stickerPackCleanupRooms(id.RoomID("!space:example.com"), id.RoomID("!portal:example.com"))
	if len(targets) != 1 || targets[0] != "!portal:example.com" {
		t.Fatalf("expected only portal cleanup target, got %#v", targets)
	}
}

func TestStickerPackCleanupRoomsSkipsAccountSpace(t *testing.T) {
	targets := stickerPackCleanupRooms(id.RoomID("!same:example.com"), id.RoomID("!same:example.com"))
	if len(targets) != 0 {
		t.Fatalf("expected no cleanup target for account space, got %#v", targets)
	}
}

func TestMessageIDAliasesIncludesRawMessageID(t *testing.T) {
	aliases := messageIDAliases(&remoteapi.InboundMessage{
		ID: "new-456",
		Extra: map[string]any{
			"raw_msg_id": int64(123),
		},
	})
	if len(aliases) != 2 || aliases[0] != "new-456" || aliases[1] != "123" {
		t.Fatalf("unexpected aliases: %#v", aliases)
	}
}

func TestMatrixContentFromRemoteTextMapsWechatEmojiAliasesAndPreservesWhitespace(t *testing.T) {
	content := matrixContentFromRemote(&remoteapi.InboundMessage{
		Type: remoteapi.MsgText,
		Text: "  before [Smile][微笑] after  ",
	})
	want := "  before :wechat_smile::wechat_smile: after  "
	if content.Body != want {
		t.Fatalf("unexpected text body: got %q want %q", content.Body, want)
	}
}

func TestMatrixContentFromRemoteDoesNotMapWechatEmojiAliasesOutsideText(t *testing.T) {
	const body = "prefix [Smile] :wechat_smile: suffix"
	tests := []struct {
		name  string
		msg   *remoteapi.InboundMessage
		label string
	}{
		{name: "MsgType47Emoji", msg: &remoteapi.InboundMessage{Type: remoteapi.MsgEmoji, Text: body}, label: "emoji body"},
		{name: "ImageCaption", msg: &remoteapi.InboundMessage{Type: remoteapi.MsgImage, Text: body, Media: &remoteapi.Media{Data: []byte("image")}}, label: "image caption"},
		{name: "VideoCaption", msg: &remoteapi.InboundMessage{Type: remoteapi.MsgVideo, Text: body, Media: &remoteapi.Media{Data: []byte("video")}}, label: "video caption"},
		{name: "AudioCaption", msg: &remoteapi.InboundMessage{Type: remoteapi.MsgAudio, Text: body, Media: &remoteapi.Media{Data: []byte("audio")}}, label: "audio caption"},
		{name: "FileCaption", msg: &remoteapi.InboundMessage{Type: remoteapi.MsgFile, Text: body, Media: &remoteapi.Media{Data: []byte("file")}}, label: "file caption"},
		{name: "AppXMLNotice", msg: &remoteapi.InboundMessage{Type: remoteapi.MsgApp, Text: "<msg>[Smile]</msg>"}, label: "app/XML notice"},
		{name: "SystemNotice", msg: &remoteapi.InboundMessage{Type: remoteapi.MessageType(99), Text: body}, label: "system notice"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matrixContentFromRemote(tt.msg).Body; got != tt.msg.Text {
				t.Fatalf("%s changed: got %q want %q", tt.label, got, tt.msg.Text)
			}
		})
	}
}

func TestMatrixContentFromRemoteVideoWithoutMediaFallsBackToText(t *testing.T) {
	content := matrixContentFromRemote(&remoteapi.InboundMessage{
		Type: remoteapi.MsgVideo,
		Text: "[video]",
	})
	if content.MsgType != "m.text" {
		t.Fatalf("expected video without media to be text, got %s", content.MsgType)
	}
}

func TestMatrixContentFromRemoteVideoWithMediaUsesVideo(t *testing.T) {
	content := matrixContentFromRemote(&remoteapi.InboundMessage{
		Type:  remoteapi.MsgVideo,
		Text:  "clip.mp4",
		Media: &remoteapi.Media{Data: []byte("mp4")},
	})
	if content.MsgType != "m.video" {
		t.Fatalf("expected video with media to be m.video, got %s", content.MsgType)
	}
}

func TestMatrixContentFromRemoteFileWithoutMediaFallsBackToText(t *testing.T) {
	content := matrixContentFromRemote(&remoteapi.InboundMessage{
		Type: remoteapi.MsgFile,
		Text: "xcodemap-idea-1.26.2.final.signed.zip\nfile type: zip",
	})
	if content.MsgType != "m.text" {
		t.Fatalf("expected file without media to be text, got %s", content.MsgType)
	}
}

func TestMatrixContentFromRemoteFileWithMediaUsesFile(t *testing.T) {
	content := matrixContentFromRemote(&remoteapi.InboundMessage{
		Type:  remoteapi.MsgFile,
		Text:  "archive.zip",
		Media: &remoteapi.Media{Data: []byte("zip")},
	})
	if content.MsgType != "m.file" {
		t.Fatalf("expected file with media to be m.file, got %s", content.MsgType)
	}
}
