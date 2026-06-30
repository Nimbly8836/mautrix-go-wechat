package matrixas

import (
	"path/filepath"
	"testing"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"maunium.net/go/mautrix/id"
)

func TestContactStorePuppetProfileCachePersistsAvatarMXC(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contacts.json")
	store := newContactStore(path)
	store.setPuppetProfileCache(remoteapi.UserID("wxid_self"), remoteapi.UserID("wxid_friend"), puppetProfileCache{
		DisplayName:     "Friend",
		AvatarSourceURL: "https://example.com/avatar.jpg",
		AvatarMXC:       id.ContentURIString("mxc://example.com/avatar"),
	})

	reloaded := newContactStore(path)
	cache, ok := reloaded.getPuppetProfileCache(remoteapi.UserID("wxid_self"), remoteapi.UserID("wxid_friend"))
	if !ok {
		t.Fatalf("expected cached puppet profile")
	}
	if cache.DisplayName != "Friend" || cache.AvatarSourceURL != "https://example.com/avatar.jpg" || cache.AvatarMXC != "mxc://example.com/avatar" {
		t.Fatalf("unexpected cache: %#v", cache)
	}
}

func TestSamePuppetProfileCache(t *testing.T) {
	cache := puppetProfileCache{DisplayName: "Friend", AvatarSourceURL: "https://example.com/a.jpg", AvatarMXC: "mxc://example.com/a"}
	if !samePuppetProfileCache(cache, puppetProfileCache{DisplayName: "Friend", AvatarSourceURL: "https://example.com/a.jpg", AvatarMXC: "mxc://example.com/a"}) {
		t.Fatalf("expected identical profile cache to match")
	}
	if samePuppetProfileCache(cache, puppetProfileCache{DisplayName: "Other", AvatarSourceURL: "https://example.com/a.jpg", AvatarMXC: "mxc://example.com/a"}) {
		t.Fatalf("expected changed display name to differ")
	}
	if samePuppetProfileCache(cache, puppetProfileCache{DisplayName: "Friend", AvatarSourceURL: "https://example.com/b.jpg", AvatarMXC: "mxc://example.com/a"}) {
		t.Fatalf("expected changed avatar source to differ")
	}
}

func TestContactStorePutListPreservesPuppetProfileCache(t *testing.T) {
	store := newContactStore(filepath.Join(t.TempDir(), "contacts.json"))
	account := remoteapi.UserID("wxid_self")
	user := remoteapi.UserID("wxid_friend")
	store.setPuppetProfileCache(account, user, puppetProfileCache{AvatarSourceURL: "old", AvatarMXC: "mxc://example.com/old"})

	store.putList(account, []remoteapi.Contact{{ID: user, Name: "Friend", AvatarURL: "new"}})

	cache, ok := store.getPuppetProfileCache(account, user)
	if !ok || cache.AvatarSourceURL != "old" || cache.AvatarMXC != "mxc://example.com/old" {
		t.Fatalf("expected puppet profile cache to be preserved, got %#v ok=%v", cache, ok)
	}
}
