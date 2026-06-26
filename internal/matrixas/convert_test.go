package matrixas

import (
	"testing"

	"maunium.net/go/mautrix/id"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

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

func TestStickerPackTargetRoomsIncludesSpaceAndPortal(t *testing.T) {
	targets := stickerPackTargetRooms(id.RoomID("!space:example.com"), id.RoomID("!portal:example.com"))
	if len(targets) != 2 || targets[0] != "!space:example.com" || targets[1] != "!portal:example.com" {
		t.Fatalf("unexpected targets: %#v", targets)
	}
}

func TestStickerPackTargetRoomsDeduplicatesPortalSpace(t *testing.T) {
	targets := stickerPackTargetRooms(id.RoomID("!same:example.com"), id.RoomID("!same:example.com"))
	if len(targets) != 1 || targets[0] != "!same:example.com" {
		t.Fatalf("unexpected targets: %#v", targets)
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
