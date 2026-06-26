package matrixas

import (
	"testing"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func TestAddImagePackRoomSourceAddsStateKey(t *testing.T) {
	content := event.ImagePackRoomsEventContent{}
	addImagePackRoomSource(&content, id.RoomID("!room:example.com"), "wechat.wxid_self")
	keys := content.Rooms[id.RoomID("!room:example.com")]
	if _, ok := keys["wechat.wxid_self"]; !ok {
		t.Fatalf("expected state key to be registered, got %#v", content.Rooms)
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
