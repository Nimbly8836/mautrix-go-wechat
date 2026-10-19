package matrixas

import (
	"context"
	"log"
	"strings"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"maunium.net/go/mautrix/event"
)

func resolvedContactName(ct remoteapi.Contact) string {
	for _, value := range []string{ct.Remark, ct.Name} {
		if name := strings.TrimSpace(value); name != "" && name != string(ct.ID) {
			return name
		}
	}
	return ""
}

func chatWithContact(chat remoteapi.Chat, ct remoteapi.Contact) remoteapi.Chat {
	if chat.IsGroup || chat.ID == "" || ct.ID != remoteapi.UserID(chat.ID) {
		return chat
	}
	if name := resolvedContactName(ct); name != "" {
		chat.Name = name
	}
	if ct.AvatarURL != "" {
		chat.Avatar = ct.AvatarURL
	}
	if len(ct.Extra) > 0 {
		extra := make(map[string]any, len(chat.Extra)+len(ct.Extra))
		for key, value := range chat.Extra {
			extra[key] = value
		}
		for key, value := range ct.Extra {
			extra[key] = value
		}
		chat.Extra = extra
	}
	return chat
}

// Persist only confirmed name/topic writes, so a temporary Matrix failure can
// be retried instead of making the local record claim an update that never ran.
func (s *Server) writePortalIdentity(ctx context.Context, rec portalRecord, chat remoteapi.Chat, name string) bool {
	if name == "" || rec.RoomID == "" {
		return false
	}
	nameCtx, cancelName := s.matrixOperationContext(ctx)
	nameResp, err := s.as.BotIntent().SendStateEvent(nameCtx, rec.RoomID, event.StateRoomName, "", map[string]any{"name": name})
	cancelName()
	if err != nil || nameResp == nil || nameResp.EventID == "" {
		log.Printf("portal room name update unconfirmed room=%s chat=%s: %v", rec.RoomID, rec.ChatID, err)
		return false
	}
	topicCtx, cancelTopic := s.matrixOperationContext(ctx)
	topicResp, err := s.as.BotIntent().SendStateEvent(topicCtx, rec.RoomID, event.StateTopic, "", map[string]any{"topic": portalTopic(name, chat)})
	cancelTopic()
	if err != nil || topicResp == nil || topicResp.EventID == "" {
		log.Printf("portal room topic update unconfirmed room=%s chat=%s: %v", rec.RoomID, rec.ChatID, err)
		return false
	}
	if current, ok := s.portals.get(rec.ChatID); ok && current.RoomID == rec.RoomID {
		current.Name = name
		s.portals.set(current)
	}
	return true
}

// Repair stale official-account identities using the account-scoped directory,
// never article titles or message text. No room creation, invite or media upload
// belongs to this path. Once acknowledged, subsequent messages need no writes.
func (s *Server) refreshOfficialPortalIdentity(ctx context.Context, account remoteapi.UserID, rec portalRecord) portalRecord {
	if s.contacts == nil || account == "" || rec.IsGroup || rec.RoomID == "" {
		return rec
	}
	ct, ok := s.contacts.get(account, remoteapi.UserID(rec.ChatID))
	if !ok || ct.ID != remoteapi.UserID(rec.ChatID) || resolvedContactName(ct) == "" {
		return rec
	}
	chat := chatWithContact(remoteapi.Chat{ID: rec.ChatID}, ct)
	if !isOfficialChat(chat) {
		return rec
	}
	name := resolvedContactName(ct)
	if !strings.HasPrefix(name, "公众号 · ") {
		name = "公众号 · " + name
	}
	if name == rec.Name {
		return rec
	}
	s.portalMetadataMu.Lock()
	defer s.portalMetadataMu.Unlock()
	current, ok := s.portals.get(rec.ChatID)
	if !ok || current.RoomID != rec.RoomID || account != s.currentAccountID(ctx) || ctx.Err() != nil {
		return rec
	}
	// A concurrent message may have refreshed the directory while we waited.
	// Re-read this same account's cache, without another backend lookup.
	ct, ok = s.contacts.get(account, remoteapi.UserID(rec.ChatID))
	if !ok || ct.ID != remoteapi.UserID(rec.ChatID) || resolvedContactName(ct) == "" {
		return rec
	}
	chat = chatWithContact(remoteapi.Chat{ID: rec.ChatID}, ct)
	if !isOfficialChat(chat) {
		return rec
	}
	name = resolvedContactName(ct)
	if !strings.HasPrefix(name, "公众号 · ") {
		name = "公众号 · " + name
	}
	if current.Name == name {
		return current
	}
	// Do not overwrite a human-renamed Matrix room while repairing old raw IDs.
	var existing event.RoomNameEventContent
	readCtx, cancelRead := s.matrixOperationContext(ctx)
	err := s.as.BotIntent().StateEvent(readCtx, rec.RoomID, event.StateRoomName, "", &existing)
	cancelRead()
	if err != nil {
		log.Printf("failed to read portal room name before repair room=%s chat=%s: %v", rec.RoomID, rec.ChatID, err)
		return rec
	}
	if ctx.Err() != nil || account != s.currentAccountID(ctx) {
		return rec
	}
	if existing.Name != "" && existing.Name != current.Name && existing.Name != name {
		return rec
	}
	if s.writePortalIdentity(ctx, current, chat, name) {
		if updated, ok := s.portals.get(rec.ChatID); ok && updated.RoomID == rec.RoomID {
			return updated
		}
	}
	return rec
}
