package matrixas

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/leat/mautrix-go-wechat/internal/config"
	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/id"
)

type cachedOfficialAPI struct{ namingAPI }

func (cachedOfficialAPI) GetChat(context.Context, remoteapi.ChatID) (remoteapi.Chat, error) {
	return remoteapi.Chat{}, errors.New("temporary directory failure")
}

func TestOfficialPortalCreationResolvesCachedNameAndPolicyBeforeInvite(t *testing.T) {
	for _, mode := range []string{officialAccountModeInvite, officialAccountModeMute, officialAccountModeReject} {
		t.Run(mode, func(t *testing.T) {
			var mu sync.Mutex
			var portalRequests []mautrix.ReqCreateRoom
			created := 0
			homeserver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/createRoom") {
					var req mautrix.ReqCreateRoom
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						t.Error(err)
					}
					mu.Lock()
					created++
					n := created
					if !strings.Contains(req.Name, "Space") && (req.Name == string(officialRegressionChat) || strings.Contains(req.Name, officialRegressionName)) {
						portalRequests = append(portalRequests, req)
					}
					mu.Unlock()
					_ = json.NewEncoder(w).Encode(map[string]string{"room_id": fmt.Sprintf("!created-%d:example.com", n)})
					return
				}
				if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/state") {
					_, _ = w.Write([]byte(`[]`))
					return
				}
				_, _ = w.Write([]byte(`{"event_id":"$event"}`))
			}))
			t.Cleanup(homeserver.Close)
			as := appservice.Create()
			as.Registration = &appservice.Registration{SenderLocalpart: "wechatbot", AppToken: "test-token"}
			as.HomeserverDomain = "example.com"
			if err := as.SetHomeserverURL(homeserver.URL); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			accounts := newAccountStore(filepath.Join(dir, "accounts.json"))
			if err := accounts.put(savedAccount{ID: "wxid_self"}); err != nil {
				t.Fatal(err)
			}
			ct := remoteapi.Contact{ID: remoteapi.UserID(officialRegressionChat), Name: officialRegressionName, Extra: map[string]any{"official_account": true}}
			s := &Server{
				cfg: &config.Config{Homeserver: config.Homeserver{Domain: "example.com"}, Appservice: config.Appservice{ID: "wechat"}, Bridge: config.Bridge{OfficialAccountMode: mode}},
				as:  as, api: cachedOfficialAPI{namingAPI{contacts: map[remoteapi.UserID]remoteapi.Contact{ct.ID: ct}}},
				accounts: accounts, portals: newPortalStore(filepath.Join(dir, "portals.json")), contacts: newContactStore(filepath.Join(dir, "contacts.json")), rooms: map[id.RoomID]bool{},
			}
			s.portals.setOwner("@owner:example.com")
			s.contacts.put("wxid_self", ct)
			rec, err := s.ensurePortal(t.Context(), officialRegressionChat)
			mu.Lock()
			defer mu.Unlock()
			if mode == officialAccountModeReject {
				if !errors.Is(err, errOfficialAccountRejected) || created != 0 {
					t.Fatalf("reject policy bypassed by directory error: created=%d err=%v", created, err)
				}
				return
			}
			if err != nil || len(portalRequests) != 1 {
				t.Fatalf("portal creation: requests=%d err=%v", len(portalRequests), err)
			}
			req := portalRequests[0]
			if req.Name != "公众号 · "+officialRegressionName || !strings.Contains(req.Topic, "official account") || rec.Name != req.Name {
				t.Fatalf("identity not resolved before invite: name=%q topic=%q record=%q", req.Name, req.Topic, rec.Name)
			}
			if mode == officialAccountModeInvite && (len(req.Invite) != 1 || req.Invite[0] != "@owner:example.com") {
				t.Fatalf("missing invitation: %#v", req.Invite)
			}
			if mode == officialAccountModeMute && len(req.Invite) != 0 {
				t.Fatalf("mute policy bypassed: %#v", req.Invite)
			}
		})
	}
}

type identityFixture struct {
	mu                                        sync.Mutex
	s                                         *Server
	rec                                       portalRecord
	matrixName                                string
	failName, failTopic, omitAck              bool
	nameWrites, topicWrites, invites, creates int
}

func newIdentityFixture(t *testing.T) *identityFixture {
	t.Helper()
	f := &identityFixture{rec: portalRecord{ChatID: officialRegressionChat, RoomID: "!existing:example.com", Name: string(officialRegressionChat)}, matrixName: string(officialRegressionChat)}
	homeserver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		f.mu.Lock()
		defer f.mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/createRoom") {
			f.creates++
		}
		if strings.HasSuffix(r.URL.Path, "/invite") {
			f.invites++
		}
		if strings.Contains(r.URL.Path, "/state/m.room.name") {
			if r.Method == http.MethodGet {
				_ = json.NewEncoder(w).Encode(map[string]string{"name": f.matrixName})
				return
			}
			f.nameWrites++
			if f.failName {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"errcode":"M_FORBIDDEN","error":"test failure"}`))
				return
			}
			var body struct {
				Name string `json:"name"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			f.matrixName = body.Name
		}
		if r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/state/m.room.topic") {
			f.topicWrites++
			if f.failTopic {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"errcode":"M_FORBIDDEN","error":"test failure"}`))
				return
			}
		}
		if f.omitAck {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		_, _ = w.Write([]byte(`{"event_id":"$event"}`))
	}))
	t.Cleanup(homeserver.Close)
	as := appservice.Create()
	as.Registration = &appservice.Registration{SenderLocalpart: "wechatbot", AppToken: "test-token"}
	as.HomeserverDomain = "example.com"
	if err := as.SetHomeserverURL(homeserver.URL); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	ct := remoteapi.Contact{ID: remoteapi.UserID(officialRegressionChat), Name: officialRegressionName, Extra: map[string]any{"official_account": true}}
	f.s = &Server{as: as, api: namingAPI{contacts: map[remoteapi.UserID]remoteapi.Contact{ct.ID: ct}}, portals: newPortalStore(filepath.Join(dir, "portals.json")), contacts: newContactStore(filepath.Join(dir, "contacts.json"))}
	f.s.contacts.put("wxid_self", ct)
	f.s.portals.set(f.rec)
	return f
}

func TestOfficialPortalIdentityRetriesUnconfirmedStateWithoutReinvite(t *testing.T) {
	for _, stage := range []string{"name", "topic", "missing-ack"} {
		t.Run(stage, func(t *testing.T) {
			f := newIdentityFixture(t)
			f.failName, f.failTopic, f.omitAck = stage == "name", stage == "topic", stage == "missing-ack"
			f.s.refreshOfficialPortalIdentity(t.Context(), "wxid_self", f.rec)
			if stored, _ := f.s.portals.get(f.rec.ChatID); stored.Name != f.rec.Name {
				t.Fatal("unconfirmed identity was persisted")
			}
			f.mu.Lock()
			f.failName, f.failTopic, f.omitAck = false, false, false
			f.mu.Unlock()
			f.s.refreshOfficialPortalIdentity(t.Context(), "wxid_self", f.rec)
			f.mu.Lock()
			defer f.mu.Unlock()
			if stored, _ := f.s.portals.get(f.rec.ChatID); stored.Name != "公众号 · "+officialRegressionName {
				t.Fatal("repair was not retried")
			}
			if f.invites != 0 || f.creates != 0 {
				t.Fatal("identity repair created a room or invitation")
			}
		})
	}
}

func TestOfficialPortalIdentityConcurrentMessagesRepairOnce(t *testing.T) {
	f := newIdentityFixture(t)
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() { f.s.refreshOfficialPortalIdentity(t.Context(), "wxid_self", f.rec) })
	}
	wg.Wait()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.nameWrites != 1 || f.topicWrites != 1 || f.invites != 0 || f.creates != 0 {
		t.Fatalf("unexpected repeated repair: name=%d topic=%d invites=%d creates=%d", f.nameWrites, f.topicWrites, f.invites, f.creates)
	}
}

func TestOfficialPortalIdentityPreservesManualRoomNameAndScope(t *testing.T) {
	for _, scenario := range []string{"manual-name", "other-account", "other-room", "group", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			f := newIdentityFixture(t)
			rec, account, ctx := f.rec, remoteapi.UserID("wxid_self"), t.Context()
			switch scenario {
			case "manual-name":
				f.matrixName = "我的公众号阅读"
			case "other-account":
				account = "wxid_other"
			case "other-room":
				rec.RoomID = "!other:example.com"
			case "group":
				rec.IsGroup = true
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			f.s.refreshOfficialPortalIdentity(ctx, account, rec)
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.nameWrites != 0 || f.topicWrites != 0 || f.invites != 0 || f.creates != 0 {
				t.Fatal("unrelated or manually named room was modified")
			}
		})
	}
}

func TestPortalRoomNameDoesNotMaskResolvedChatNameWithRawContactID(t *testing.T) {
	s := &Server{api: namingAPI{}}
	if name := s.portalRoomName(t.Context(), remoteapi.Chat{ID: officialRegressionChat, Name: officialRegressionName, Extra: map[string]any{"official_account": true}}); name != "公众号 · "+officialRegressionName {
		t.Fatalf("resolved name masked by raw contact ID: %q", name)
	}
}
