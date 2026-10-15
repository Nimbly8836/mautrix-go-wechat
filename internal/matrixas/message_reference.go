package matrixas

import (
	"fmt"
	"log"
	"strings"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"
)

// Typed aliases prevent a short recall ID from authorizing a native quote.
type messageReferenceKey struct {
	Account remoteapi.UserID
	Chat    remoteapi.ChatID
	Room    id.RoomID
	ID      string
}

type messageReference struct {
	EventID         id.EventID            `json:"event_id"`
	RelatedEventIDs []id.EventID          `json:"related_event_ids,omitempty"` // Native attachments, never independent WeChat messages.
	MatrixSender    id.UserID             `json:"matrix_sender"`
	BridgeSender    bool                  `json:"bridge_sender,omitempty"`
	Sender          remoteapi.UserID      `json:"sender"`
	SenderName      string                `json:"sender_name,omitempty"`
	Type            remoteapi.MessageType `json:"type"`
	Text            string                `json:"text,omitempty"`
	QuoteText       string                `json:"quote_text,omitempty"` // Bounded backend preview, separate from readable text.
	NewID           remoteapi.MessageID   `json:"new_id,omitempty"`
	Client          *mautrix.Client       `json:"-"` // Original bridge sender, including bot fallback.
}

func (s *Server) rememberMessageReference(account remoteapi.UserID, rec portalRecord, message *remoteapi.InboundMessage, eventID id.EventID, client *mautrix.Client, relatedEventIDs ...id.EventID) error {
	if message == nil || eventID == "" || client == nil || message.Chat != rec.ChatID || (message.Account != "" && message.Account != account) {
		return fmt.Errorf("invalid message reference scope or sender")
	}
	keys := append([]string(nil), message.DeduplicationKeys...)
	// Legacy unqualified aliases remain recall-only and never become new IDs.
	for _, alias := range messageIDAliases(message) {
		keys = append(keys, "id:"+string(alias))
	}
	ref := messageReference{EventID: eventID, RelatedEventIDs: append([]id.EventID(nil), relatedEventIDs...), MatrixSender: client.UserID, BridgeSender: true, Sender: message.Sender, Type: message.Type, Text: referenceSnapshot(message.Text), Client: client}
	if message.Quote != nil {
		// A file still maps to wire type 49, but retain its neutral file kind
		// so a trusted edit part can be distinguished from a generic app event.
		if message.Type != remoteapi.MsgFile {
			ref.Type = message.Quote.Type
		}
		ref.QuoteText = referenceSnapshot(message.Quote.Text)
	}
	if err := s.saveMessageReference(account, rec, keys, ref); err != nil {
		log.Printf("ERROR: inbound message was delivered but its durable reference could not be saved: %v", err)
		return err
	}
	return nil
}

func (s *Server) saveMessageReference(account remoteapi.UserID, rec portalRecord, keys []string, ref messageReference) error {
	valid := make([]string, 0, len(keys))
	seen := make(map[string]bool)
	for _, key := range keys {
		if !validReferenceKey(key) || seen[key] {
			continue
		}
		seen[key] = true
		valid = append(valid, key)
		if strings.HasPrefix(key, "new:") {
			candidate := remoteapi.MessageID(strings.TrimPrefix(key, "new:"))
			if ref.NewID != "" && ref.NewID != candidate {
				return fmt.Errorf("multiple strong IDs for one message")
			}
			ref.NewID = candidate
		}
	}
	if len(valid) == 0 {
		return nil
	}
	if ref.SenderName == "" && s.contacts != nil {
		if contact, ok := s.contacts.get(account, ref.Sender); ok {
			ref.SenderName = referenceSnapshot(contact.Name)
		}
	}
	store, err := s.referenceStore()
	if err != nil {
		return err
	}
	return store.append(referenceRecord{Version: 1, Account: account, Chat: rec.ChatID, Room: rec.RoomID, Keys: valid, Reference: ref})
}

func (s *Server) getMessageReference(account remoteapi.UserID, rec portalRecord, target remoteapi.MessageID, key string) messageReference {
	if target == "" {
		return messageReference{}
	}
	if key == "" {
		key = "id:" + string(target)
	}
	if !validReferenceKey(key) || strings.SplitN(key, ":", 2)[1] != string(target) {
		return messageReference{}
	}
	store, err := s.referenceStore()
	if err != nil {
		log.Printf("ERROR: cannot resolve durable message reference: %v", err)
		return messageReference{}
	}
	store.mu.Lock()
	ref := store.forward[messageReferenceKey{account, rec.ChatID, rec.RoomID, key}]
	ref = store.visibleReferenceLocked(matrixReferenceKey{Account: account, Chat: rec.ChatID, Room: rec.RoomID}, ref, false)
	ref.RelatedEventIDs = append([]id.EventID(nil), ref.RelatedEventIDs...)
	store.mu.Unlock()
	if ref.Client == nil && ref.BridgeSender && s.as != nil {
		// Only our journal, not recall XML or Matrix event content, supplies this
		// identity. Outgoing human-authored Matrix events cannot be impersonated.
		if ref.MatrixSender == s.as.BotMXID() {
			ref.Client = s.as.BotIntent().Client
		} else {
			ref.Client = s.as.Intent(ref.MatrixSender).Client
		}
	}
	return ref
}

// A response-first self-echo already has a real, human-authored Matrix event.
// A callback-first echo may still be delivered; its later send confirmation
// makes the human original canonical in the journal instead of losing it.
func (s *Server) isConfirmedOutboundEcho(account remoteapi.UserID, rec portalRecord, message *remoteapi.InboundMessage) bool {
	if account == "" || message.Sender != account {
		return false
	}
	for _, key := range message.DeduplicationKeys {
		if !strings.HasPrefix(key, "new:") {
			continue
		}
		ref := s.getMessageReference(account, rec, remoteapi.MessageID(strings.TrimPrefix(key, "new:")), key)
		if ref.EventID != "" && !ref.BridgeSender && ref.Sender == account {
			return true
		}
	}
	return false
}

func (s *Server) getMatrixMessageReference(account remoteapi.UserID, rec portalRecord, eventID id.EventID) (messageReference, error) {
	store, err := s.referenceStore()
	if err != nil {
		return messageReference{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.err != nil {
		return messageReference{}, store.err
	}
	key := matrixReferenceKey{account, rec.ChatID, rec.RoomID, eventID}
	ref := store.visibleReferenceLocked(key, store.reverse[key], true)
	ref.RelatedEventIDs = append([]id.EventID(nil), ref.RelatedEventIDs...)
	return ref, nil
}
