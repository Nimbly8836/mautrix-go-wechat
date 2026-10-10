package matrixas

import (
	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"
)

// Strong references are separate from the legacy unqualified alias lookup.
// Recalls must never confuse a short ID with a new ID, or cross accounts/rooms.
type messageReferenceKey struct {
	Account remoteapi.UserID
	Chat    remoteapi.ChatID
	Room    id.RoomID
	ID      string
}

type messageReference struct {
	EventID id.EventID
	Client  *mautrix.Client // Actual sender, including a bot fallback.
}

func (s *Server) rememberMessageReference(account remoteapi.UserID, rec portalRecord, message *remoteapi.InboundMessage, eventID id.EventID, client *mautrix.Client) {
	if eventID == "" || client == nil || (message.Account != "" && message.Account != account) {
		return
	}
	keys := message.DeduplicationKeys
	if len(keys) == 0 {
		for _, alias := range messageIDAliases(message) {
			if alias != "" && alias != "0" {
				keys = append(keys, "id:"+string(alias))
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.messageReferences == nil || len(s.messageReferences)+len(keys) > 5000 {
		s.messageReferences = make(map[messageReferenceKey]messageReference)
	}
	for _, key := range keys {
		if key != "" {
			s.messageReferences[messageReferenceKey{account, message.Chat, rec.RoomID, key}] = messageReference{eventID, client}
		}
	}
}

func (s *Server) getMessageReference(account remoteapi.UserID, rec portalRecord, target remoteapi.MessageID, key string) messageReference {
	if target == "" {
		return messageReference{}
	}
	if key == "" {
		key = "id:" + string(target)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.messageReferences[messageReferenceKey{account, rec.ChatID, rec.RoomID, key}]
}
