package matrixas

import (
	"context"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

// portalState is persisted to disk. It's deliberately simple for this MVP;
// replacing it with a SQL store later won't affect the remoteapi layer.
type portalState struct {
	Owner   id.UserID                `json:"owner,omitempty"`
	Portals map[string]portalRecord  `json:"portals"`
	Pending map[string]pendingPortal `json:"pending,omitempty"`
}

type portalRecord struct {
	ChatID  remoteapi.ChatID `json:"chat_id"`
	RoomID  id.RoomID        `json:"room_id"`
	Name    string           `json:"name,omitempty"`
	IsGroup bool             `json:"is_group,omitempty"`
}

type pendingPortal struct {
	ChatID      remoteapi.ChatID `json:"chat_id"`
	Sender      remoteapi.UserID `json:"sender,omitempty"`
	Name        string           `json:"name,omitempty"`
	LastMessage string           `json:"last_message,omitempty"`
	IsGroup     bool             `json:"is_group,omitempty"`
	Count       int              `json:"count"`
}

type portalStore struct {
	mu   sync.Mutex
	path string
	data portalState
}

func newPortalStore(path string) *portalStore {
	ps := &portalStore{path: path, data: portalState{Portals: map[string]portalRecord{}, Pending: map[string]pendingPortal{}}}
	ps.load()
	return ps
}

func (ps *portalStore) load() {
	b, err := os.ReadFile(ps.path)
	if err != nil {
		return
	}
	if err := json.Unmarshal(b, &ps.data); err != nil {
		log.Printf("failed to parse portal store %s: %v", ps.path, err)
		ps.data = portalState{Portals: map[string]portalRecord{}}
	}
	if ps.data.Portals == nil {
		ps.data.Portals = map[string]portalRecord{}
	}
	if ps.data.Pending == nil {
		ps.data.Pending = map[string]pendingPortal{}
	}
}

func (ps *portalStore) saveLocked() {
	b, err := json.MarshalIndent(ps.data, "", "  ")
	if err != nil {
		log.Printf("marshal portal store: %v", err)
		return
	}
	if err := os.WriteFile(ps.path, b, 0o600); err != nil {
		log.Printf("write portal store %s: %v", ps.path, err)
	}
}

func (ps *portalStore) owner() id.UserID {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.data.Owner
}

func (ps *portalStore) setOwner(owner id.UserID) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.data.Owner == owner {
		return
	}
	ps.data.Owner = owner
	ps.saveLocked()
}

func (ps *portalStore) get(chat remoteapi.ChatID) (portalRecord, bool) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	r, ok := ps.data.Portals[string(chat)]
	return r, ok
}

func (ps *portalStore) set(r portalRecord) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.data.Portals[string(r.ChatID)] = r
	delete(ps.data.Pending, string(r.ChatID))
	ps.saveLocked()
}

func (ps *portalStore) byRoom(room id.RoomID) (portalRecord, bool) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	for _, r := range ps.data.Portals {
		if r.RoomID == room {
			return r, true
		}
	}
	return portalRecord{}, false
}

func (ps *portalStore) list() []portalRecord {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	out := make([]portalRecord, 0, len(ps.data.Portals))
	for _, r := range ps.data.Portals {
		if r.ChatID == "" {
			continue
		}
		out = append(out, r)
	}
	return out
}

func (ps *portalStore) addPending(p pendingPortal) (isNew bool, count int) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	key := string(p.ChatID)
	old, ok := ps.data.Pending[key]
	if ok {
		p.Count = old.Count + 1
		if p.Name == "" {
			p.Name = old.Name
		}
	} else if p.Count == 0 {
		p.Count = 1
	}
	ps.data.Pending[key] = p
	ps.saveLocked()
	return !ok, p.Count
}

func (ps *portalStore) pending(chat remoteapi.ChatID) (pendingPortal, bool) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	p, ok := ps.data.Pending[string(chat)]
	return p, ok
}

func (ps *portalStore) pendingList() []pendingPortal {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	out := make([]pendingPortal, 0, len(ps.data.Pending))
	for _, p := range ps.data.Pending {
		out = append(out, p)
	}
	return out
}

// ensurePortal returns the Matrix room for a remote chat, creating it if needed.
func (s *Server) ensurePortal(ctx context.Context, chatID remoteapi.ChatID) (portalRecord, error) {
	if rec, ok := s.portals.get(chatID); ok && rec.RoomID != "" {
		s.addPortalToAccountSpace(ctx, s.currentAccountID(ctx), rec)
		return rec, nil
	}

	chat, err := s.api.GetChat(ctx, chatID)
	if err != nil {
		// Some APIs don't expose metadata reliably; still create a room.
		chat = remoteapi.Chat{ID: chatID, Name: string(chatID), IsGroup: strings.HasSuffix(string(chatID), "@chatroom")}
	}
	name := chat.Name
	if name == "" {
		name = string(chatID)
	}

	invite := []id.UserID{}
	if owner := s.portals.owner(); owner != "" {
		invite = append(invite, owner)
	}
	resp, err := s.as.BotIntent().CreateRoom(ctx, &mautrix.ReqCreateRoom{
		Name:   name + " (WeChat)",
		Topic:  fmt.Sprintf("WeChat portal for %s", chatID),
		Invite: invite,
		Preset: "private_chat",
		// Do not set is_direct here. Some Matrix clients then display the
		// room as the ghost user (@wechat_...) instead of the WeChat chat name.
		IsDirect: false,
	})
	if err != nil {
		return portalRecord{}, fmt.Errorf("create portal room for %s: %w", chatID, err)
	}
	rec := portalRecord{ChatID: chatID, RoomID: resp.RoomID, Name: name, IsGroup: chat.IsGroup}
	s.portals.set(rec)
	log.Printf("created portal room: chat=%s room=%s", chatID, resp.RoomID)
	s.addPortalToAccountSpace(ctx, s.currentAccountID(ctx), rec)
	return rec, nil
}

func (s *Server) puppetIntent(ctx context.Context, user remoteapi.UserID) *mautrix.Client {
	localpart := s.cfg.Appservice.ID + "_" + encodeLocalpart(string(user))
	intent := s.as.Intent(id.NewUserID(localpart, s.cfg.Homeserver.Domain))
	if intent == nil {
		return s.as.BotIntent().Client
	}
	if err := intent.EnsureRegistered(ctx); err != nil {
		log.Printf("failed to register puppet %s: %v", user, err)
		return s.as.BotIntent().Client
	}
	// Best effort profile update. Avoid doing expensive contact lookup here.
	_ = intent.SetDisplayName(ctx, string(user))
	return intent.Client
}

func (s *Server) ensurePuppetJoined(ctx context.Context, user remoteapi.UserID, room id.RoomID) *mautrix.Client {
	localpart := s.cfg.Appservice.ID + "_" + encodeLocalpart(string(user))
	intent := s.as.Intent(id.NewUserID(localpart, s.cfg.Homeserver.Domain))
	if intent == nil {
		return s.as.BotIntent().Client
	}
	if err := intent.EnsureJoined(ctx, room); err != nil {
		log.Printf("failed to join puppet %s to %s: %v", user, room, err)
		return s.as.BotIntent().Client
	}
	_ = intent.SetDisplayName(ctx, s.displayNameForRemote(ctx, user))
	return intent.Client
}

func (s *Server) displayNameForRemote(ctx context.Context, user remoteapi.UserID) string {
	ct, err := s.api.GetContact(ctx, user)
	if err == nil {
		if ct.Remark != "" {
			return ct.Remark
		}
		if ct.Name != "" {
			return ct.Name
		}
	}
	return string(user)
}

func (s *Server) sendPortalNotice(ctx context.Context, room id.RoomID, body string) {
	_, err := s.as.BotIntent().SendMessageEvent(ctx, room, event.EventMessage, &event.MessageEventContent{MsgType: event.MsgNotice, Body: body})
	if err != nil {
		log.Printf("failed to send portal notice to %s: %v", room, err)
	}
}

func encodeLocalpart(raw string) string {
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte(raw))
	return strings.ToLower(enc)
}
