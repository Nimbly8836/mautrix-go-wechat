package matrixas

import (
	"testing"

	"maunium.net/go/mautrix/id"
)

func TestDuplicateAccountFolderChildrenFindsExtraNamedFolders(t *testing.T) {
	folders := map[string]id.RoomID{
		spaceFolderContacts: id.RoomID("!contacts-current:example.com"),
		spaceFolderOfficial: id.RoomID("!official-current:example.com"),
		spaceFolderGroups:   id.RoomID("!groups-current:example.com"),
	}
	childNames := map[id.RoomID]string{
		id.RoomID("!contacts-current:example.com"): "联系人",
		id.RoomID("!contacts-old:example.com"):     "联系人",
		id.RoomID("!portal:example.com"):           "Some WeChat Group",
		id.RoomID("!groups-current:example.com"):   "群聊",
		id.RoomID("!groups-old:example.com"):       "群聊",
	}
	childSpaces := map[id.RoomID]bool{
		id.RoomID("!contacts-current:example.com"): true,
		id.RoomID("!contacts-old:example.com"):     true,
		id.RoomID("!portal:example.com"):           false,
		id.RoomID("!groups-current:example.com"):   true,
		id.RoomID("!groups-old:example.com"):       true,
	}

	duplicates := duplicateAccountFolderChildren(folders, childNames, childSpaces)

	if len(duplicates) != 2 || duplicates[0] != "!contacts-old:example.com" || duplicates[1] != "!groups-old:example.com" {
		t.Fatalf("unexpected duplicate folder children: %#v", duplicates)
	}
}

func TestDuplicateAccountFolderChildrenDoesNotRemoveCurrentFolders(t *testing.T) {
	folders := map[string]id.RoomID{
		spaceFolderContacts: id.RoomID("!contacts-current:example.com"),
	}
	childNames := map[id.RoomID]string{
		id.RoomID("!contacts-current:example.com"): "联系人",
	}
	childSpaces := map[id.RoomID]bool{
		id.RoomID("!contacts-current:example.com"): true,
	}

	duplicates := duplicateAccountFolderChildren(folders, childNames, childSpaces)

	if len(duplicates) != 0 {
		t.Fatalf("expected no duplicate folder children, got %#v", duplicates)
	}
}

func TestDuplicateAccountFolderChildrenDoesNotRemovePortalNamedLikeFolder(t *testing.T) {
	folders := map[string]id.RoomID{
		spaceFolderGroups: id.RoomID("!groups-current:example.com"),
	}
	childNames := map[id.RoomID]string{
		id.RoomID("!groups-current:example.com"): "群聊",
		id.RoomID("!portal:example.com"):         "群聊",
	}
	childSpaces := map[id.RoomID]bool{
		id.RoomID("!groups-current:example.com"): true,
		id.RoomID("!portal:example.com"):         false,
	}

	duplicates := duplicateAccountFolderChildren(folders, childNames, childSpaces)

	if len(duplicates) != 0 {
		t.Fatalf("expected portal named like folder to be preserved, got %#v", duplicates)
	}
}
