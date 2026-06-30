package matrixas

import (
	"testing"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func TestBuildWechatStickerPackContentMarksImagesAsStickersAndEmoticons(t *testing.T) {
	content := buildWechatStickerPackContent(remoteapi.UserID("wxid_self"), []wechatStickerImage{
		{Shortcode: "smile", Body: "Smile", URL: id.ContentURIString("mxc://example.com/smile")},
	})

	if content.Metadata.DisplayName != "WeChat wxid_self stickers" {
		t.Fatalf("unexpected display name: %q", content.Metadata.DisplayName)
	}
	if len(content.Metadata.Usage) != 2 || content.Metadata.Usage[0] != event.ImagePackUsageSticker || content.Metadata.Usage[1] != event.ImagePackUsageEmoji {
		t.Fatalf("expected sticker and emoticon usage, got %#v", content.Metadata.Usage)
	}
	img := content.Images["smile"]
	if img == nil {
		t.Fatalf("expected smile image in pack")
	}
	if img.URL != id.ContentURIString("mxc://example.com/smile") || img.Body != "Smile" {
		t.Fatalf("unexpected image: %#v", img)
	}
}

func TestMergeImagePackContentAddsMissingImages(t *testing.T) {
	dst := buildWechatStickerPackContent(remoteapi.UserID("wxid_self"), []wechatStickerImage{{Shortcode: "old", URL: "mxc://example.com/old"}})
	src := buildWechatStickerPackContent(remoteapi.UserID("wxid_self"), []wechatStickerImage{
		{Shortcode: "old", URL: "mxc://example.com/old2"},
		{Shortcode: "new", URL: "mxc://example.com/new"},
	})
	added := mergeImagePackContent(dst, src)
	if added != 1 {
		t.Fatalf("expected one added image, got %d", added)
	}
	if dst.Images["old"].URL != "mxc://example.com/old" {
		t.Fatalf("expected existing image to be preserved")
	}
	if dst.Images["new"].URL != "mxc://example.com/new" {
		t.Fatalf("expected new image to be merged")
	}
}

func TestWechatStickerStateKeyIsStableAndNamespaced(t *testing.T) {
	stateKey := wechatStickerPackStateKey(remoteapi.UserID("wxid_self"))
	if stateKey != "wechat.wxid_self" {
		t.Fatalf("unexpected state key: %q", stateKey)
	}
}

func TestParseStickerAddArgsRequiresShortcodeAndURL(t *testing.T) {
	sticker, err := parseStickerAddArgs("smile mxc://example.com/smile Big Smile")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if sticker.Shortcode != "smile" || sticker.URL != id.ContentURIString("mxc://example.com/smile") || sticker.Body != "Big Smile" {
		t.Fatalf("unexpected sticker: %#v", sticker)
	}

	if _, err := parseStickerAddArgs("smile"); err == nil {
		t.Fatalf("expected error for missing URL")
	}
}
