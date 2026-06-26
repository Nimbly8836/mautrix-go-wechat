package matrixas

import (
	"testing"

	"maunium.net/go/mautrix/event"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

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
