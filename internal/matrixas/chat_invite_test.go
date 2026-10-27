package matrixas

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/leat/mautrix-go-wechat/internal/config"
	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/event"
)

type inviteTestAPI struct{ namingAPI }

func (inviteTestAPI) Self(context.Context) (remoteapi.UserID, error) { return "wxid_self", nil }

func TestBridgeChatRoomInviteSendsOneClickableActionAndDeduplicatesURL(t *testing.T) {
	var mu sync.Mutex
	pollStarts := 0
	homeserver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, string(event.EventUnstablePollStart.Type)) {
			mu.Lock()
			pollStarts++
			mu.Unlock()
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"event_id": "$event"})
	}))
	t.Cleanup(homeserver.Close)
	as := appservice.Create()
	as.Registration = &appservice.Registration{SenderLocalpart: "wechatbot", AppToken: "test-token"}
	as.HomeserverDomain = "example.com"
	if err := as.SetHomeserverURL(homeserver.URL); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	s := &Server{
		cfg:      &config.Config{Homeserver: config.Homeserver{Domain: "example.com"}, Bridge: config.Bridge{Permissions: map[string]string{"@owner:example.com": "user"}}},
		as:       as,
		api:      inviteTestAPI{},
		portals:  newPortalStore(filepath.Join(dir, "portals.json")),
		contacts: newContactStore(filepath.Join(dir, "contacts.json")),
	}
	s.portals.setOwner("@owner:example.com")
	invite := &remoteapi.InboundMessage{Account: "wxid_self", Chat: "wxid_inviter", ChatInvite: &remoteapi.ChatRoomInvite{URL: "https://support.weixin.qq.com/cgi-bin/mmsupport-bin/addchatroombyinvite?ticket=synthetic", Inviter: "Jonathan", ChatName: "NGA线上交流2.8"}}
	rec := portalRecord{ChatID: invite.Chat, RoomID: "!portal:example.com"}
	s.bridgeChatRoomInvite(t.Context(), "wxid_self", rec, invite)
	s.bridgeChatRoomInvite(t.Context(), "wxid_self", rec, invite)
	mu.Lock()
	defer mu.Unlock()
	if pollStarts != 1 {
		t.Fatalf("poll action count = %d, want one for duplicate invitation URL", pollStarts)
	}
	if len(s.chatInvites) != 1 {
		t.Fatalf("pending invite count = %d, want one", len(s.chatInvites))
	}
}
