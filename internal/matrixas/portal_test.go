package matrixas

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/id"

	"github.com/leat/mautrix-go-wechat/internal/config"
	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

type namingAPI struct {
	remoteapi.PuppetAPI
	contacts map[remoteapi.UserID]remoteapi.Contact
}

func (a namingAPI) Self(ctx context.Context) (remoteapi.UserID, error) { return "wxid_self", nil }
func (a namingAPI) ListContacts(ctx context.Context) ([]remoteapi.Contact, error) {
	out := make([]remoteapi.Contact, 0, len(a.contacts))
	for _, c := range a.contacts {
		out = append(out, c)
	}
	return out, nil
}
func (a namingAPI) GetContact(ctx context.Context, id remoteapi.UserID) (remoteapi.Contact, error) {
	if c, ok := a.contacts[id]; ok {
		return c, nil
	}
	return remoteapi.Contact{ID: id}, nil
}

type portalCreationAPI struct {
	remoteapi.PuppetAPI
}

func (portalCreationAPI) Self(context.Context) (remoteapi.UserID, error) {
	return "wxid_self", nil
}

func (portalCreationAPI) GetChat(_ context.Context, chatID remoteapi.ChatID) (remoteapi.Chat, error) {
	return remoteapi.Chat{ID: chatID, Name: "Test group", IsGroup: true}, nil
}

func TestEnsurePortalReturnsCreatedPortalWhenSpaceAttachmentPersistenceFails(t *testing.T) {
	accounts := newAccountStore(filepath.Join(t.TempDir(), "accounts.json"))
	if err := accounts.put(savedAccount{ID: "wxid_self", Protocol: "wechat", Session: []byte("session")}); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("persist account Space failed")
	accounts.persist = func(string, []byte, fs.FileMode) error { return injected }

	created := 0
	homeserver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/_matrix/client/v3/createRoom" {
			created++
			_, _ = w.Write([]byte(`{"room_id":"!created-` + string(rune('0'+created)) + `:example.com"}`))
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
	s := &Server{
		cfg:      &config.Config{Homeserver: config.Homeserver{Domain: "example.com"}},
		as:       as,
		api:      portalCreationAPI{},
		accounts: accounts,
		portals:  newPortalStore(filepath.Join(t.TempDir(), "portals.json")),
		contacts: newContactStore(filepath.Join(t.TempDir(), "contacts.json")),
		rooms:    map[id.RoomID]bool{},
	}

	rec, err := s.ensurePortal(context.Background(), "group@chatroom")
	if err != nil {
		t.Fatalf("ensurePortal treated Space attachment as portal creation failure: %v", err)
	}
	if rec.RoomID != "!created-1:example.com" {
		t.Fatalf("ensurePortal returned wrong created portal: %#v", rec)
	}
	if stored, ok := s.portals.get("group@chatroom"); !ok || stored.RoomID != rec.RoomID {
		t.Fatalf("created portal was not retained: %#v, %v", stored, ok)
	}
}

func TestOnMessageBridgesFirstMessageWhenNewPortalSpacePersistenceFails(t *testing.T) {
	accounts := newAccountStore(filepath.Join(t.TempDir(), "accounts.json"))
	if err := accounts.put(savedAccount{ID: "wxid_self", Protocol: "wechat", Session: []byte("session")}); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("persist account Space failed")
	accounts.persist = func(string, []byte, fs.FileMode) error { return injected }

	created := 0
	var messagePaths []string
	var messageBodies []string
	homeserver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/_matrix/client/v3/createRoom" {
			created++
			_, _ = w.Write([]byte(`{"room_id":"!created-` + string(rune('0'+created)) + `:example.com"}`))
			return
		}
		if strings.Contains(r.URL.Path, "/send/m.room.message/") {
			var content struct {
				Body string `json:"body"`
			}
			if err := json.NewDecoder(r.Body).Decode(&content); err != nil {
				t.Errorf("decode bridged message: %v", err)
			}
			messagePaths = append(messagePaths, r.URL.Path)
			messageBodies = append(messageBodies, content.Body)
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
	s := &Server{
		cfg:           &config.Config{Homeserver: config.Homeserver{Domain: "example.com"}},
		as:            as,
		api:           portalCreationAPI{},
		accounts:      accounts,
		portals:       newPortalStore(filepath.Join(t.TempDir(), "portals.json")),
		contacts:      newContactStore(filepath.Join(t.TempDir(), "contacts.json")),
		rooms:         map[id.RoomID]bool{},
		messageEvents: map[string]id.EventID{},
	}

	s.OnMessage(context.Background(), &remoteapi.InboundMessage{
		ID:     "first-id",
		Chat:   "group@chatroom",
		Sender: "wxid_self",
		Type:   remoteapi.MsgText,
		Text:   "first message",
	})

	if created != 2 {
		t.Fatalf("created rooms = %d, want portal plus failed account Space", created)
	}
	if len(messageBodies) != 1 || messageBodies[0] != "first message" {
		t.Fatalf("bridged first messages = %#v", messageBodies)
	}
	if !strings.Contains(messagePaths[0], "!created-1:example.com") {
		t.Fatalf("first message sent to wrong room: %s", messagePaths[0])
	}
	if stored, ok := s.portals.get("group@chatroom"); !ok || stored.RoomID != "!created-1:example.com" {
		t.Fatalf("created portal was not retained: %#v, %v", stored, ok)
	}
	if acc, _ := accounts.get("wxid_self"); acc.SpaceRoom != "" {
		t.Fatalf("failed account Space was published: %#v", acc)
	}
}

func TestRoomMemberDisplayNameUsesRoomDisplayNameWithNickname(t *testing.T) {
	name := roomMemberDisplayName(remoteapi.Contact{
		Name:   "昵称",
		Remark: "备注",
		Extra:  map[string]any{"room_display_name": "群备注"},
	})
	if name != "群备注 [昵称]" {
		t.Fatalf("expected room display name with nickname, got %q", name)
	}
}

func TestRoomMemberDisplayNameUsesBareRoomDisplayNameWithoutNickname(t *testing.T) {
	name := roomMemberDisplayName(remoteapi.Contact{Extra: map[string]any{"room_display_name": "群备注"}})
	if name != "群备注" {
		t.Fatalf("expected bare room display name, got %q", name)
	}
}

func TestRoomMemberDisplayNameUsesBracketedNicknameWithoutDisplayName(t *testing.T) {
	name := roomMemberDisplayName(remoteapi.Contact{Name: "昵称", Remark: "备注"})
	if name != "[昵称]" {
		t.Fatalf("expected bracketed nickname fallback, got %q", name)
	}
}

func TestRoomMemberDisplayNameFallsBackToRemarkOnlyWhenNicknameMissing(t *testing.T) {
	if name := roomMemberDisplayName(remoteapi.Contact{Remark: "备注"}); name != "备注" {
		t.Fatalf("expected remark fallback, got %q", name)
	}
}

func TestPortalRoomNameUsesContactRemarkBeforeNickname(t *testing.T) {
	s := &Server{api: namingAPI{contacts: map[remoteapi.UserID]remoteapi.Contact{
		"wxid_friend": {ID: "wxid_friend", Remark: "朋友备注", Name: "朋友昵称"},
	}}}
	name := s.portalRoomName(context.Background(), remoteapi.Chat{ID: "wxid_friend", Name: "wxid_friend"})
	if name != "朋友备注" {
		t.Fatalf("expected remark as room name, got %q", name)
	}
}

func TestPortalRoomNameUsesContactNicknameBeforeWxid(t *testing.T) {
	s := &Server{api: namingAPI{contacts: map[remoteapi.UserID]remoteapi.Contact{
		"wxid_friend": {ID: "wxid_friend", Name: "朋友昵称"},
	}}}
	name := s.portalRoomName(context.Background(), remoteapi.Chat{ID: "wxid_friend"})
	if name != "朋友昵称" {
		t.Fatalf("expected nickname as room name, got %q", name)
	}
}

func TestPortalRoomNameUsesGroupName(t *testing.T) {
	s := &Server{api: namingAPI{}}
	name := s.portalRoomName(context.Background(), remoteapi.Chat{ID: "123@chatroom", Name: "群名称", IsGroup: true})
	if name != "群名称" {
		t.Fatalf("expected group name, got %q", name)
	}
}
