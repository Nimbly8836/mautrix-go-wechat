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

func TestImagePackAccountDataTargetsUseEmptyStateKeyForElementLegacy(t *testing.T) {
	targets := imagePackAccountDataTargets("wechat.wxid_self")
	if len(targets) != 2 {
		t.Fatalf("unexpected target count: %#v", targets)
	}
	if targets[0].EventType != "m.image_pack.rooms" || targets[0].StateKey != "wechat.wxid_self" {
		t.Fatalf("unexpected stable target: %#v", targets[0])
	}
	if targets[1].EventType != "im.ponies.emote_rooms" || targets[1].StateKey != "" {
		t.Fatalf("unexpected legacy Element target: %#v", targets[1])
	}
}

func TestImagePackStateEventTargetsUseEmptyStateKeyForElementLegacy(t *testing.T) {
	targets := imagePackStateEventTargets("wechat.wxid_self")
	if len(targets) != 2 {
		t.Fatalf("unexpected target count: %#v", targets)
	}
	if targets[0].EventType.Type != "m.room.image_pack" || targets[0].StateKey != "wechat.wxid_self" {
		t.Fatalf("unexpected stable target: %#v", targets[0])
	}
	if targets[1].EventType.Type != "im.ponies.room_emotes" || targets[1].StateKey != "" {
		t.Fatalf("unexpected legacy Element target: %#v", targets[1])
	}
}

func TestImagePackPortalMirrorUsesOnlyElementLegacyTarget(t *testing.T) {
	targets := imagePackPortalMirrorTargets()
	if len(targets) != 1 || targets[0].EventType.Type != "im.ponies.room_emotes" || targets[0].StateKey != "" {
		t.Fatalf("unexpected portal mirror targets: %#v", targets)
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

func TestSetSingleImagePackRoomSourceRemovesDuplicatePackSources(t *testing.T) {
	content := event.ImagePackRoomsEventContent{Rooms: map[id.RoomID]map[string]struct{}{
		id.RoomID("!space:example.com"):  {"other": {}},
		id.RoomID("!portal:example.com"): {"wechat.wxid_self": {}},
	}}
	setSingleImagePackRoomSource(&content, id.RoomID("!space:example.com"), "wechat.wxid_self")
	if _, ok := content.Rooms[id.RoomID("!portal:example.com")]["wechat.wxid_self"]; ok {
		t.Fatalf("expected duplicate portal source to be removed, got %#v", content.Rooms)
	}
	if _, ok := content.Rooms[id.RoomID("!space:example.com")]["wechat.wxid_self"]; !ok {
		t.Fatalf("expected root space source, got %#v", content.Rooms)
	}
	if _, ok := content.Rooms[id.RoomID("!space:example.com")]["other"]; !ok {
		t.Fatalf("expected unrelated source to be preserved, got %#v", content.Rooms)
	}
}

func TestSetSingleImagePackRoomSourceRemovesAliasKeys(t *testing.T) {
	content := event.ImagePackRoomsEventContent{Rooms: map[id.RoomID]map[string]struct{}{
		id.RoomID("!space:example.com"):  {"wechat.wxid_self": {}},
		id.RoomID("!portal:example.com"): {"wechat.wxid_self": {}, "": {}},
	}}
	setSingleImagePackRoomSourceAliases(&content, id.RoomID("!space:example.com"), "", "wechat.wxid_self", "")
	if _, ok := content.Rooms[id.RoomID("!portal:example.com")]; ok {
		t.Fatalf("expected old portal aliases to be removed, got %#v", content.Rooms)
	}
	keys := content.Rooms[id.RoomID("!space:example.com")]
	if _, ok := keys[""]; !ok {
		t.Fatalf("expected empty Element state key source, got %#v", content.Rooms)
	}
	if _, ok := keys["wechat.wxid_self"]; ok {
		t.Fatalf("expected legacy wechat state key source to be removed, got %#v", content.Rooms)
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
