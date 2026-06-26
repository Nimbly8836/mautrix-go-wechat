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
