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

func (s *Server) portalRoomName(ctx context.Context, chat remoteapi.Chat) string {
	chatID := string(chat.ID)
	name := ""
	if !chat.IsGroup && chat.ID != "" {
		name = s.contactDisplayName(ctx, remoteapi.UserID(chat.ID))
	}
	if name == "" && chat.Name != "" && chat.Name != chatID {
		name = chat.Name
	}
	if name == "" && chat.Name != "" {
		name = chat.Name
	}
	if name == "" {
		name = chatID
	}
	if isOfficialChat(chat) && name != "" && !strings.HasPrefix(name, "公众号 · ") {
		return "公众号 · " + name
	}
	return name
}

func isOfficialChat(chat remoteapi.Chat) bool {
	if chat.Extra == nil {
		return false
	}
	if v, ok := chat.Extra["official_account"].(bool); ok {
		return v
	}
	_, exists := chat.Extra["description"]
	return exists
}

func portalTopic(name string, chat remoteapi.Chat) string {
	kind := "WeChat portal"
	if isOfficialChat(chat) {
		kind = "WeChat official account portal"
	}
	if name != "" && name != string(chat.ID) {
		return fmt.Sprintf("%s for %s (%s)", kind, name, chat.ID)
	}
	return fmt.Sprintf("%s for %s", kind, chat.ID)
}

func matrixPick(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func (s *Server) contactDisplayName(ctx context.Context, user remoteapi.UserID) string {
	if user == "" || s.api == nil {
		return ""
	}
	account := s.currentAccountID(ctx)
	if account != "" && s.contacts != nil {
		if ct, ok := s.contacts.get(account, user); ok {
			if name := matrixPick(ct.Remark, ct.Name); name != "" && name != string(user) {
				return name
			}
			// Cached contact only contains the raw wxid/gh id. Treat it as incomplete
			// and fall through to GetContact so invitations can show real names.
		}
	}
	ct, err := s.api.GetContact(ctx, user)
	if err != nil {
		return ""
	}
	if ct.ID == "" {
		ct.ID = user
	}
	if account != "" && s.contacts != nil {
		s.contacts.put(account, ct)
	}
	return matrixPick(ct.Remark, ct.Name)
}

func (s *Server) refreshContactCache(ctx context.Context, account remoteapi.UserID) (int, error) {
	if s.contacts == nil {
		return 0, fmt.Errorf("contact cache is not configured")
	}
	contacts, err := s.api.ListContacts(ctx)
	if err != nil {
		return 0, err
	}
	s.contacts.putList(account, contacts)
	log.Printf("refreshed contact cache: account=%s contacts=%d", account, len(contacts))
	return len(contacts), nil
}

func (s *Server) roomAvatarFromURL(ctx context.Context, url string) (id.ContentURIString, error) {
	if strings.TrimSpace(url) == "" {
		return "", nil
	}
	if s.as == nil || s.as.BotIntent() == nil || s.as.BotIntent().Client == nil {
		return "", fmt.Errorf("matrix client unavailable")
	}
	resp, err := s.as.BotIntent().Client.UploadLink(ctx, url)
	if err != nil {
		return "", err
	}
	return id.ContentURIString(resp.ContentURI.String()), nil
}

func (s *Server) applyPortalMetadata(ctx context.Context, rec portalRecord, chat remoteapi.Chat) {
	if rec.RoomID == "" {
		return
	}
	name := s.portalRoomName(ctx, chat)
	if name != "" && name != rec.Name {
		rec.Name = name
		s.portals.set(rec)
		_, _ = s.as.BotIntent().SendStateEvent(ctx, rec.RoomID, event.StateRoomName, "", map[string]any{"name": name})
	}
	if name != "" {
		_, _ = s.as.BotIntent().SendStateEvent(ctx, rec.RoomID, event.StateTopic, "", map[string]any{"topic": portalTopic(name, chat)})
	}
	if avatar := strings.TrimSpace(chat.Avatar); avatar != "" {
		if mxc, err := s.roomAvatarFromURL(ctx, avatar); err == nil && mxc != "" {
			_, _ = s.as.BotIntent().SendStateEvent(ctx, rec.RoomID, event.StateRoomAvatar, "", event.RoomAvatarEventContent{URL: mxc})
		}
	}
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
	name := s.portalRoomName(ctx, chat)

	invite := []id.UserID{}
	if owner := s.portals.owner(); owner != "" {
		invite = append(invite, owner)
	}
	topic := portalTopic(name, chat)
	resp, err := s.as.BotIntent().CreateRoom(ctx, &mautrix.ReqCreateRoom{
		Name:   name,
		Topic:  topic,
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
	s.applyPortalMetadata(ctx, rec, chat)
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
	_ = intent.SetDisplayName(ctx, s.displayNameForRemote(ctx, user))
	return intent.Client
}

func shouldJoinPuppet(account, sender remoteapi.UserID) bool {
	return sender != "" && sender != account
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
	if name := s.contactDisplayName(ctx, user); name != "" {
		return name
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
