package matrixas

import (
	"context"
	"testing"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

func TestAccountStickerWritersPreserveCanonicalBuiltinCompatibilityPrecedence(t *testing.T) {
	const (
		account      = remoteapi.UserID("wxid_a")
		space        = id.RoomID("!space:example.org")
		portal       = id.RoomID("!portal:example.org")
		canonicalMXC = "mxc://canonical.example/smile"
		manualMXC    = "mxc://account.example/smile"
	)
	newServer := func(t *testing.T) (*Server, *fakeRawImagePackState) {
		t.Helper()
		state := newFakeRawImagePackState()
		state.set(space, event.StateImagePack, wechatStickerPackStateKey(account), `{"images":{"account_only":{"url":"mxc://account.example/only"}}}`)
		state.set(space, event.StateUnstableImagePack, "", `{"images":{}}`)
		state.set(portal, event.StateUnstableImagePack, "", `{"images":{}}`)
		s := builtinEmojiPackTestServer(builtinEmojiTestData(
			builtinEmojiRecord{Shortcode: "wechat_smile", MXC: canonicalMXC},
			builtinEmojiRecord{Shortcode: "wechat_grimace", MXC: "mxc://canonical.example/grimace"},
		), state)
		s.accounts.data.Accounts[string(account)] = savedAccount{ID: account, SpaceRoom: space.String()}
		s.portals.data.Portals["chat"] = portalRecord{ChatID: "chat", RoomID: portal}
		result, _ := s.reconcileBuiltinEmojiPacks(context.Background())
		if result.Status != builtinPackStatusSuccess {
			t.Fatalf("global reconciliation = %#v", result)
		}
		return s, state
	}
	assertCanonicalCompatibility := func(t *testing.T, state *fakeRawImagePackState, room id.RoomID) {
		t.Helper()
		images := parsePackImages(state.get(room, event.StateUnstableImagePack, ""))
		if got := images["wechat_smile"].URL; got != canonicalMXC {
			t.Fatalf("compatibility smile in %s = %q, want canonical %q", room, got, canonicalMXC)
		}
	}

	t.Run("manual account add", func(t *testing.T) {
		s, state := newServer(t)
		if _, err := s.addStickerToAccountSpace(context.Background(), account, space, wechatStickerImage{Shortcode: "wechat_smile", URL: manualMXC}); err != nil {
			t.Fatal(err)
		}
		assertCanonicalCompatibility(t, state, space)
		stable := parsePackImages(state.get(space, event.StateImagePack, wechatStickerPackStateKey(account)))
		if got := stable["wechat_smile"].URL; got != canonicalMXC {
			t.Fatalf("account stable smile = %q, want canonical %q", got, canonicalMXC)
		}
	})

	t.Run("Msg47 portal add", func(t *testing.T) {
		s, state := newServer(t)
		s.addStickerToElementPortal(context.Background(), account, portal, wechatStickerImage{Shortcode: "wechat_smile", URL: manualMXC})
		assertCanonicalCompatibility(t, state, portal)
	})

	t.Run("repair does not import global registry into account stable", func(t *testing.T) {
		s, state := newServer(t)
		if got := s.repairStickerPacksForAccount(context.Background(), account, space, ""); got != 1 {
			t.Fatalf("repair count = %d, want only the account-specific image", got)
		}
		stable := parsePackImages(state.get(space, event.StateImagePack, wechatStickerPackStateKey(account)))
		if _, ok := stable["wechat_smile"]; ok {
			t.Fatal("global wechat_smile leaked from shared compatibility into account stable")
		}
		if _, ok := stable["wechat_grimace"]; ok {
			t.Fatal("global wechat_grimace leaked from shared compatibility into account stable")
		}
		if stable["account_only"].URL == "" {
			t.Fatal("account-specific image was lost during repair")
		}
		assertCanonicalCompatibility(t, state, space)
	})

	t.Run("mirror after global reconciliation", func(t *testing.T) {
		s, state := newServer(t)
		state.set(space, event.StateImagePack, wechatStickerPackStateKey(account), `{"images":{"wechat_smile":{"url":"`+manualMXC+`"},"account_only":{"url":"mxc://account.example/only"}}}`)
		s.mirrorStickerPackToElementPortal(context.Background(), account, space, portal)
		assertCanonicalCompatibility(t, state, portal)
	})

	t.Run("portal attachment after global reconciliation", func(t *testing.T) {
		s, state := newServer(t)
		state.set(space, event.StateImagePack, wechatStickerPackStateKey(account), `{"images":{"wechat_smile":{"url":"`+manualMXC+`"},"account_only":{"url":"mxc://account.example/only"}}}`)
		s.finishPortalAccountSpaceAttachment(context.Background(), account, space, portalRecord{ChatID: "chat", RoomID: portal})
		assertCanonicalCompatibility(t, state, portal)
	})
}
