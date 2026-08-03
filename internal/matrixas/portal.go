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
	"maunium.net/go/mautrix/appservice"
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
	ct, ok := s.contactForRemote(ctx, user, false)
	if !ok {
		return ""
	}
	return matrixPick(ct.Remark, ct.Name)
}

func roomMemberDisplayName(ct remoteapi.Contact) string {
	roomName := remoteExtraString(ct.Extra, "room_display_name")
	name := strings.TrimSpace(ct.Name)
	if roomName != "" {
		if name != "" && name != roomName {
			return fmt.Sprintf("%s [%s]", roomName, name)
		}
		return roomName
	}
	if name != "" {
		return fmt.Sprintf("[%s]", name)
	}
	return strings.TrimSpace(ct.Remark)
}

func (s *Server) roomMemberDisplayNameForRemote(ctx context.Context, user remoteapi.UserID) string {
	ct, ok := s.contactForRemote(ctx, user, false)
	if !ok {
		return ""
	}
	return roomMemberDisplayName(ct)
}

func (s *Server) contactForRemote(ctx context.Context, user remoteapi.UserID, force bool) (remoteapi.Contact, bool) {
	if user == "" || s.api == nil {
		return remoteapi.Contact{}, false
	}
	account := s.currentAccountID(ctx)
	if !force && account != "" && s.contacts != nil {
		if ct, ok := s.contacts.get(account, user); ok {
			if name := matrixPick(ct.Remark, ct.Name); name != "" && name != string(user) {
				return ct, true
			}
			// Cached contact only contains the raw wxid/gh id. Treat it as incomplete
			// and fall through to GetContact so invitations/profile refresh can show real names.
		}
	}
	ct, err := s.api.GetContact(ctx, user)
	if err != nil {
		if account != "" && s.contacts != nil {
			if cached, ok := s.contacts.get(account, user); ok {
				return cached, true
			}
		}
		return remoteapi.Contact{ID: user, Name: string(user)}, false
	}
	if ct.ID == "" {
		ct.ID = user
	}
	if ct.Name == "" && ct.Remark == "" {
		ct.Name = string(user)
	}
	if account != "" && s.contacts != nil {
		s.contacts.put(account, ct)
	}
	return ct, true
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

func normalizePortalChat(rec portalRecord, chat remoteapi.Chat) remoteapi.Chat {
	if chat.ID == "" {
		chat.ID = rec.ChatID
	}
	if strings.HasSuffix(string(chat.ID), "@chatroom") {
		chat.IsGroup = true
	} else if chat.ID != "" {
		chat.IsGroup = false
	}
	if chat.Name == "" && chat.ID != "" {
		chat.Name = string(chat.ID)
	}
	return chat
}

func (s *Server) portalRefreshDebug(chat remoteapi.Chat, joined, failed int) string {
	avatar := "no"
	if strings.TrimSpace(chat.Avatar) != "" {
		avatar = "yes"
	}
	return fmt.Sprintf("chat=%s name=%q group=%v members=%d avatar=%s synced_members=%d failed_members=%d", chat.ID, chat.Name, chat.IsGroup, len(chat.Members), avatar, joined, failed)
}

func (s *Server) applyPortalMetadata(ctx context.Context, rec portalRecord, chat remoteapi.Chat) {
	if rec.RoomID == "" {
		return
	}
	chat = normalizePortalChat(rec, chat)
	if !chat.IsGroup && chat.ID != "" {
		if ct, ok := s.contactForRemote(ctx, remoteapi.UserID(chat.ID), true); ok {
			if ct.AvatarURL != "" {
				chat.Avatar = ct.AvatarURL
			}
			if name := matrixPick(ct.Remark, ct.Name); name != "" {
				chat.Name = name
			}
			if len(ct.Extra) > 0 {
				chat.Extra = ct.Extra
			}
		}
	}
	name := s.portalRoomName(ctx, chat)
	if name != "" {
		if name != rec.Name {
			rec.Name = name
			s.portals.set(rec)
		}
		if _, err := s.as.BotIntent().SendStateEvent(ctx, rec.RoomID, event.StateRoomName, "", map[string]any{"name": name}); err != nil {
			log.Printf("failed to set portal room name room=%s chat=%s: %v", rec.RoomID, rec.ChatID, err)
		}
		if _, err := s.as.BotIntent().SendStateEvent(ctx, rec.RoomID, event.StateTopic, "", map[string]any{"topic": portalTopic(name, chat)}); err != nil {
			log.Printf("failed to set portal room topic room=%s chat=%s: %v", rec.RoomID, rec.ChatID, err)
		}
	}
	if avatar := strings.TrimSpace(chat.Avatar); avatar != "" {
		if mxc, err := s.roomAvatarFromURL(ctx, avatar); err == nil && mxc != "" {
			if _, err := s.as.BotIntent().SendStateEvent(ctx, rec.RoomID, event.StateRoomAvatar, "", event.RoomAvatarEventContent{URL: mxc}); err != nil {
				log.Printf("failed to set portal room avatar room=%s chat=%s: %v", rec.RoomID, rec.ChatID, err)
			}
		} else if err != nil {
			log.Printf("failed to upload portal room avatar room=%s chat=%s url=%s: %v", rec.RoomID, rec.ChatID, avatar, err)
		}
	}
}

// ensurePortal returns the Matrix room for a remote chat, creating it if needed.
func (s *Server) ensurePortal(ctx context.Context, chatID remoteapi.ChatID) (portalRecord, error) {
	if rec, ok := s.portals.get(chatID); ok && rec.RoomID != "" {
		if err := s.addPortalToAccountSpace(ctx, s.currentAccountID(ctx), rec); err != nil {
			s.reportPortalSpaceAttachmentFailure(ctx, rec, err)
		}
		return rec, nil
	}

	chat, err := s.api.GetChat(ctx, chatID)
	if err != nil {
		// Some APIs don't expose metadata reliably; still create a room.
		chat = remoteapi.Chat{ID: chatID, Name: string(chatID), IsGroup: strings.HasSuffix(string(chatID), "@chatroom")}
	}
	name := s.portalRoomName(ctx, chat)
	official := isOfficialChat(chat)
	if !officialAccountModeCreatesPortal(s.officialAccountMode(), official) {
		return portalRecord{}, errOfficialAccountRejected
	}

	invite := []id.UserID{}
	owner := s.portals.owner()
	if owner != "" && officialAccountModeInvitesOwner(s.officialAccountMode(), official) {
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
	if owner != "" {
		s.ensureRoomAdminPower(ctx, owner, resp.RoomID)
	}
	s.applyPortalMetadata(ctx, rec, chat)
	s.syncPortalMembers(ctx, rec, chat, true, puppetProfileUpdateAuto)
	log.Printf("created portal room: chat=%s room=%s", chatID, resp.RoomID)
	if err := s.addPortalToAccountSpace(ctx, s.currentAccountID(ctx), rec); err != nil {
		s.reportPortalSpaceAttachmentFailure(ctx, rec, err)
	}
	return rec, nil
}

func (s *Server) reportPortalSpaceAttachmentFailure(ctx context.Context, rec portalRecord, err error) {
	log.Printf("portal created/available but account Space metadata attachment failed: chat=%s room=%s: %v", rec.ChatID, rec.RoomID, err)
	s.notifyManagement(ctx, fmt.Sprintf("Portal 房间已创建并可继续桥接消息，但账号 Space 元数据更新失败:\n- chat: %s\n- room: %s\n- error: %v", rec.ChatID, rec.RoomID, err))
}

type puppetProfileUpdateMode int

const (
	puppetProfileUpdateAuto puppetProfileUpdateMode = iota
	puppetProfileUpdateStartup
	puppetProfileUpdateCommand
)

func shouldUpdatePuppetProfile(mode puppetProfileUpdateMode) bool {
	return mode == puppetProfileUpdateCommand
}

func puppetJoinKey(room id.RoomID, user remoteapi.UserID) string {
	return string(room) + "\x00" + string(user)
}

func (s *Server) hasPuppetJoin(room id.RoomID, user remoteapi.UserID) bool {
	if s == nil || room == "" || user == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.puppetJoins[puppetJoinKey(room, user)]
}

func (s *Server) markPuppetJoined(room id.RoomID, user remoteapi.UserID) {
	if s == nil || room == "" || user == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.puppetJoins == nil {
		s.puppetJoins = map[string]bool{}
	}
	s.puppetJoins[puppetJoinKey(room, user)] = true
}

func (s *Server) puppetIntent(ctx context.Context, user remoteapi.UserID) *mautrix.Client {
	intent, ok := s.puppetIntentAPI(ctx, user)
	if !ok {
		return s.as.BotIntent().Client
	}
	return intent.Client
}

func (s *Server) puppetIntentAPI(ctx context.Context, user remoteapi.UserID) (*appservice.IntentAPI, bool) {
	localpart := s.cfg.Appservice.ID + "_" + encodeLocalpart(string(user))
	intent := s.as.Intent(id.NewUserID(localpart, s.cfg.Homeserver.Domain))
	if intent == nil {
		return nil, false
	}
	if err := intent.EnsureRegistered(ctx); err != nil {
		log.Printf("failed to register puppet %s: %v", user, err)
		return nil, false
	}
	return intent, true
}

func shouldJoinPuppet(account, sender remoteapi.UserID) bool {
	return sender != "" && sender != account
}

func (s *Server) ensurePuppetJoined(ctx context.Context, user remoteapi.UserID, room id.RoomID) *mautrix.Client {
	intent, ok := s.ensurePuppetJoinedWithProfile(ctx, user, room, false, puppetProfileUpdateAuto)
	if !ok {
		return s.as.BotIntent().Client
	}
	return intent.Client
}

func (s *Server) ensurePuppetJoinedWithProfile(ctx context.Context, user remoteapi.UserID, room id.RoomID, force bool, profileMode puppetProfileUpdateMode) (*appservice.IntentAPI, bool) {
	intent, ok := s.puppetIntentAPI(ctx, user)
	if !ok {
		return nil, false
	}
	if !force && profileMode == puppetProfileUpdateAuto && s.hasPuppetJoin(room, user) {
		return intent, true
	}
	if shouldUpdatePuppetProfile(profileMode) {
		s.updatePuppetProfile(ctx, user, force)
	}
	if _, err := intent.JoinRoomByID(ctx, room); err != nil {
		// JoinRoomByID with custom membership extra cannot be used for displayname
		// overrides here: mautrix merges parsed membership content after Raw, so
		// the profile displayname overwrites Raw["displayname"]. Send an explicit
		// m.room.member state event after joining instead.
		log.Printf("failed to join/update puppet %s in %s: %v", user, room, err)
		if err := intent.EnsureJoined(ctx, room); err != nil {
			log.Printf("failed to ensure puppet %s joined to %s: %v", user, room, err)
			return nil, false
		}
	}
	if name := s.roomMemberDisplayNameForRemote(ctx, user); name != "" {
		s.setPuppetRoomDisplayName(ctx, intent, room, name)
	}
	s.markPuppetJoined(room, user)
	return intent, true
}

func (s *Server) setPuppetRoomDisplayName(ctx context.Context, intent *appservice.IntentAPI, room id.RoomID, name string) {
	if intent == nil || room == "" || strings.TrimSpace(name) == "" {
		return
	}
	content := event.MemberEventContent{Membership: event.MembershipJoin}
	if s != nil && s.as != nil && s.as.BotIntent() != nil {
		var existing event.MemberEventContent
		if err := s.as.BotIntent().StateEvent(ctx, room, event.StateMember, intent.UserID.String(), &existing); err == nil {
			content = existing
		}
	}
	if content.Membership == "" {
		content.Membership = event.MembershipJoin
	}
	if content.Displayname == name {
		return
	}
	content.Displayname = name
	if _, err := intent.SendStateEvent(ctx, room, event.StateMember, intent.UserID.String(), &content); err != nil {
		log.Printf("failed to set puppet room displayname user=%s room=%s name=%q: %v", intent.UserID, room, name, err)
	}
}

func (s *Server) updatePuppetProfile(ctx context.Context, user remoteapi.UserID, force bool) (string, id.ContentURIString, bool) {
	intent, ok := s.puppetIntentAPI(ctx, user)
	if !ok {
		return string(user), "", false
	}
	ct, _ := s.contactForRemote(ctx, user, force)
	name := matrixPick(ct.Remark, ct.Name)
	if name == "" {
		name = string(user)
	}
	avatarURL := strings.TrimSpace(ct.AvatarURL)
	mxc := id.ContentURIString("")
	if account := s.currentAccountID(ctx); account != "" && s.contacts != nil {
		if cache, ok := s.contacts.getPuppetProfileCache(account, user); ok {
			if avatarURL != "" && cache.AvatarSourceURL == avatarURL && cache.AvatarMXC != "" {
				mxc = cache.AvatarMXC
			}
			desired := puppetProfileCache{DisplayName: name, AvatarSourceURL: avatarURL, AvatarMXC: mxc}
			if samePuppetProfileCache(cache, desired) {
				return name, mxc, false
			}
		}
	}
	if avatarURL != "" && mxc == "" {
		var err error
		mxc, err = s.roomAvatarFromURL(ctx, avatarURL)
		if err != nil {
			log.Printf("failed to upload puppet avatar user=%s url=%s: %v", user, avatarURL, err)
			return name, "", false
		}
	}
	if err := intent.SetDisplayName(ctx, name); err != nil {
		log.Printf("failed to set puppet displayname user=%s name=%q: %v", user, name, err)
	}
	if avatarURL == "" {
		s.cachePuppetProfile(ctx, user, puppetProfileCache{DisplayName: name})
		return name, "", true
	}
	parsed, err := mxc.Parse()
	if err != nil {
		log.Printf("failed to parse uploaded puppet avatar user=%s mxc=%s: %v", user, mxc, err)
		return name, "", false
	}
	if err := intent.SetAvatarURL(ctx, parsed); err != nil {
		log.Printf("failed to set puppet avatar user=%s mxc=%s: %v", user, mxc, err)
	}
	s.cachePuppetProfile(ctx, user, puppetProfileCache{DisplayName: name, AvatarSourceURL: avatarURL, AvatarMXC: mxc})
	return name, mxc, true
}

func (s *Server) cachePuppetProfile(ctx context.Context, user remoteapi.UserID, cache puppetProfileCache) {
	if s == nil || s.contacts == nil || user == "" {
		return
	}
	account := s.currentAccountID(ctx)
	if account == "" {
		return
	}
	s.contacts.setPuppetProfileCache(account, user, cache)
}

func extraMemberContacts(v any) []remoteapi.Contact {
	switch contacts := v.(type) {
	case []remoteapi.Contact:
		return contacts
	case []any:
		out := []remoteapi.Contact{}
		for _, item := range contacts {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			id := remoteapi.UserID(fmt.Sprint(m["ID"]))
			if id == "" || id == "<nil>" {
				id = remoteapi.UserID(fmt.Sprint(m["id"]))
			}
			if id == "" || id == "<nil>" {
				continue
			}
			out = append(out, remoteapi.Contact{
				ID:        id,
				Name:      fmt.Sprint(m["Name"]),
				Remark:    fmt.Sprint(m["Remark"]),
				AvatarURL: fmt.Sprint(m["AvatarURL"]),
			})
		}
		return out
	default:
		return nil
	}
}

func (s *Server) syncPortalMembers(ctx context.Context, rec portalRecord, chat remoteapi.Chat, force bool, profileMode puppetProfileUpdateMode) (joined, failed int) {
	if rec.RoomID == "" {
		return 0, 0
	}
	chat = normalizePortalChat(rec, chat)
	account := s.currentAccountID(ctx)
	hasMemberContacts := false
	if account != "" && s.contacts != nil && chat.Extra != nil {
		contacts := extraMemberContacts(chat.Extra["member_contacts"])
		if len(contacts) > 0 {
			hasMemberContacts = true
			for _, ct := range contacts {
				s.contacts.put(account, ct)
			}
		}
	}
	members := chat.Members
	if !chat.IsGroup && chat.ID != "" {
		members = []remoteapi.UserID{remoteapi.UserID(chat.ID)}
	}
	seen := map[remoteapi.UserID]bool{}
	for _, user := range members {
		if user == "" || user == account || seen[user] {
			continue
		}
		seen[user] = true
		profileForce := force && !(chat.IsGroup && hasMemberContacts)
		if _, ok := s.ensurePuppetJoinedWithProfile(ctx, user, rec.RoomID, profileForce, profileMode); ok {
			joined++
		} else {
			failed++
		}
	}
	return joined, failed
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
