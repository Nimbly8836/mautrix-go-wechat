package matrixas

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leat/mautrix-go-wechat/internal/config"
	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/id"
)

// Synthetic identifiers preserve the reported raw-ID/name divergence without
// committing the user's subscription or Matrix room identifiers.
const officialRegressionChat remoteapi.ChatID = "gh_001122334455"
const officialRegressionName = "示例公众号"

type officialRegressionAPI struct{ namingAPI }

func (a officialRegressionAPI) GetChat(ctx context.Context, chat remoteapi.ChatID) (remoteapi.Chat, error) {
	ct, err := a.GetContact(ctx, remoteapi.UserID(chat))
	return remoteapi.Chat{ID: chat, Name: ct.Name, Extra: ct.Extra}, err
}

func TestOfficialPortalInboundRepairsRawNameWithoutNewRoomOrInvite(t *testing.T) {
	var mu sync.Mutex
	var names, topics []string
	matrixName := string(officialRegressionChat)
	created, invites, messages := 0, 0, 0
	homeserver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/createRoom"):
			created++
			_, _ = w.Write([]byte(`{"room_id":"!unwanted:example.com"}`))
			return
		case strings.HasSuffix(r.URL.Path, "/invite"):
			invites++
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/state/m.room.member/"):
			_ = json.NewEncoder(w).Encode(map[string]string{"membership": "join", "displayname": "[" + officialRegressionName + "]"})
			return
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/state/m.room.name"):
			_ = json.NewEncoder(w).Encode(map[string]string{"name": matrixName})
			return
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/state/m.room.name"):
			var content struct {
				Name string `json:"name"`
			}
			if err := json.NewDecoder(r.Body).Decode(&content); err != nil {
				t.Error(err)
			}
			names = append(names, content.Name)
			matrixName = content.Name
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/state/m.room.topic"):
			var content struct {
				Topic string `json:"topic"`
			}
			if err := json.NewDecoder(r.Body).Decode(&content); err != nil {
				t.Error(err)
			}
			topics = append(topics, content.Topic)
		case strings.Contains(r.URL.Path, "/send/m.room.message/"):
			messages++
			_ = json.NewEncoder(w).Encode(map[string]string{"event_id": fmt.Sprintf("$article-%d", messages)})
			return
		}
		_, _ = w.Write([]byte(`{"event_id":"$state-event"}`))
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
		cfg: &config.Config{Homeserver: config.Homeserver{Domain: "example.com"}, Appservice: config.Appservice{ID: "wechat"}},
		as:  as,
		api: officialRegressionAPI{namingAPI{contacts: map[remoteapi.UserID]remoteapi.Contact{
			remoteapi.UserID(officialRegressionChat): {ID: remoteapi.UserID(officialRegressionChat), Name: officialRegressionName, Extra: map[string]any{"official_account": true}},
		}}},
		portals:  newPortalStore(filepath.Join(dir, "portals.json")),
		contacts: newContactStore(filepath.Join(dir, "contacts.json")),
		rooms:    map[id.RoomID]bool{}, messageEvents: map[string]id.EventID{},
	}
	s.portals.set(portalRecord{ChatID: officialRegressionChat, RoomID: "!existing:example.com", Name: string(officialRegressionChat)})
	for _, msgID := range []remoteapi.MessageID{"article-1", "article-2"} {
		s.OnMessage(t.Context(), &remoteapi.InboundMessage{ID: msgID, Chat: officialRegressionChat, Sender: remoteapi.UserID(officialRegressionChat), Type: remoteapi.MsgText, Text: "公众号推文", Timestamp: time.Unix(100, 0)})
	}
	mu.Lock()
	defer mu.Unlock()
	if len(names) != 1 || names[0] != "公众号 · "+officialRegressionName {
		t.Errorf("room names = %#v, want one repair to verified official name", names)
	}
	if len(topics) != 1 || !strings.Contains(topics[0], "official account") || !strings.Contains(topics[0], officialRegressionName) {
		t.Errorf("room topics = %#v, want official account identity", topics)
	}
	if rec, _ := s.portals.get(officialRegressionChat); rec.Name != "公众号 · "+officialRegressionName {
		t.Errorf("persisted portal name = %q", rec.Name)
	}
	if created != 0 || invites != 0 || messages != 2 {
		t.Errorf("created=%d invites=%d messages=%d, want 0/0/2", created, invites, messages)
	}
}
