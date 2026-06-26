package matrixas

import (
	"testing"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/leat/mautrix-go-wechat/internal/config"
)

func TestAddImagePackRoomSourceAddsStateKey(t *testing.T) {
	content := event.ImagePackRoomsEventContent{}
	addImagePackRoomSource(&content, id.RoomID("!room:example.com"), "wechat.wxid_self")
	keys := content.Rooms[id.RoomID("!room:example.com")]
	if _, ok := keys["wechat.wxid_self"]; !ok {
		t.Fatalf("expected state key to be registered, got %#v", content.Rooms)
	}
}

func TestImagePackAccountDataTypesIncludesStableAndUnstable(t *testing.T) {
	types := imagePackAccountDataTypes()
	if len(types) != 2 || types[0] != "m.image_pack.rooms" || types[1] != "im.ponies.emote_rooms" {
		t.Fatalf("unexpected account data types: %#v", types)
	}
}

func TestImagePackStateEventTypesIncludesStableAndUnstable(t *testing.T) {
	types := imagePackStateEventTypes()
	if len(types) != 2 || types[0].Type != "m.room.image_pack" || types[1].Type != "im.ponies.room_emotes" {
		t.Fatalf("unexpected state event types: %#v", types)
	}
}

func TestPersonalStickerAccessTokenForUser(t *testing.T) {
	s := &Server{cfg: &config.Config{Bridge: config.Bridge{PersonalStickerUserID: "@leat:example.com", PersonalStickerAccessToken: "token"}}}
	if got := s.personalStickerAccessTokenForUser("@leat:example.com"); got != "token" {
		t.Fatalf("expected configured token, got %q", got)
	}
	if got := s.personalStickerAccessTokenForUser("@other:example.com"); got != "" {
		t.Fatalf("expected no token for other user, got %q", got)
	}
}

func TestAddImagePackRoomSourcePreservesExisting(t *testing.T) {
	content := event.ImagePackRoomsEventContent{Rooms: map[id.RoomID]map[string]struct{}{
		id.RoomID("!existing:example.com"): {"old": {}},
	}}
	addImagePackRoomSource(&content, id.RoomID("!room:example.com"), "wechat.wxid_self")
	if _, ok := content.Rooms[id.RoomID("!existing:example.com")]["old"]; !ok {
		t.Fatalf("expected existing entry to be preserved, got %#v", content.Rooms)
	}
	if _, ok := content.Rooms[id.RoomID("!room:example.com")]["wechat.wxid_self"]; !ok {
		t.Fatalf("expected new entry, got %#v", content.Rooms)
	}
}
