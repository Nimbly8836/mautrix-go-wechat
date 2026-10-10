package matrixas

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

// This is a bounded display cache, separate from the durable per-command claim.
// Only overlapping backend ID keys establish identity; identical text does not.
const maxSolitaireDeliveries = 1024

type solitaireDeliveryKey struct {
	Account remoteapi.UserID
	Chat    remoteapi.ChatID
	Room    id.RoomID
	ID      string
}

type solitaireDelivery struct {
	transactionID string
	eventID       id.EventID
	client        *mautrix.Client
	keys          []solitaireDeliveryKey
}

// deliverSolitaireToPortal serializes the response and inbound paths, and only
// marks a version displayed after Matrix returns an event ID. A failed/unknown
// Matrix write retains its transaction ID so a later callback can safely finish
// the display without repeating the WeChat send. Do not acquire
// accountTransitionMu here: the command caller holds it, and inbound delivery
// must remain drainable while an account transition stops the remote connector.
func (s *Server) deliverSolitaireToPortal(ctx context.Context, rec portalRecord, m *remoteapi.InboundMessage) error {
	if m == nil || m.Solitaire == nil || m.Solitaire.Account == "" || m.Chat != rec.ChatID || m.Solitaire.Chat != rec.ChatID {
		return fmt.Errorf("接龙来源与当前账号或会话不一致")
	}
	if m.Text == "" || m.Solitaire.Reference == "" {
		return fmt.Errorf("接龙缺少完整正文或来源")
	}
	s.solitaireDeliveryMu.Lock()
	defer s.solitaireDeliveryMu.Unlock()
	if m.Solitaire.Account != s.currentAccountID(ctx) {
		return fmt.Errorf("接龙来源与当前账号不一致")
	}
	delivery := s.solitaireDeliveryFor(rec, m)
	if delivery.eventID == "" {
		if delivery.client == nil {
			delivery.client = s.as.BotIntent().Client
			if shouldJoinPuppet(m.Solitaire.Account, m.Sender) {
				delivery.client = s.ensurePuppetJoined(ctx, m.Sender, rec.RoomID)
			}
		}
		// A transaction ID is not idempotent across different Matrix senders.
		// Keep the first actual sender, including a bot fallback, for recovery.
		content := solitaireMatrixContent(&event.MessageEventContent{MsgType: event.MsgText, Body: m.Text}, m.Solitaire, s.commandPrefix())
		operationCtx, cancel := s.matrixOperationContext(ctx)
		resp, err := delivery.client.SendMessageEvent(operationCtx, rec.RoomID, event.EventMessage, content, mautrix.ReqSendEvent{Timestamp: m.Timestamp.UnixMilli(), TransactionID: delivery.transactionID})
		cancel()
		if err != nil {
			return err
		}
		if resp == nil || resp.EventID == "" {
			return fmt.Errorf("Matrix 响应缺少消息 ID，展示结果未知")
		}
		delivery.eventID = resp.EventID
	}
	s.rememberMessageReference(m.Solitaire.Account, rec, m, delivery.eventID, delivery.client)
	// Also learn aliases discovered by a later callback after the response.
	for _, msgID := range messageIDAliases(m) {
		if msgID != "" && msgID != "0" {
			s.rememberMessageEvent(m.Chat, msgID, delivery.eventID)
		}
	}
	return nil
}

// Requires solitaireDeliveryMu. Reserve identity before the Matrix request,
// but do not confuse a reservation with a successfully displayed event.
func (s *Server) solitaireDeliveryFor(rec portalRecord, m *remoteapi.InboundMessage) *solitaireDelivery {
	ids := m.DeduplicationKeys
	if len(ids) == 0 {
		// Backends without typed aliases can still identify exact duplicates.
		ids = []string{"reference:" + m.Solitaire.Reference}
	}
	keys := make([]solitaireDeliveryKey, 0, len(ids))
	var delivery *solitaireDelivery
	known := make(map[*solitaireDelivery]bool)
	for _, msgID := range ids {
		key := solitaireDeliveryKey{Account: m.Solitaire.Account, Chat: m.Chat, Room: rec.RoomID, ID: msgID}
		keys = append(keys, key)
		if existing := s.solitaireDeliveries[key]; existing != nil {
			known[existing] = true
			if delivery == nil || (delivery.eventID == "" && existing.eventID != "") {
				delivery = existing
			}
		}
	}
	if delivery == nil {
		if len(s.solitaireDeliveryOrder) == maxSolitaireDeliveries {
			oldest := s.solitaireDeliveryOrder[0]
			for _, key := range oldest.keys {
				if s.solitaireDeliveries[key] == oldest {
					delete(s.solitaireDeliveries, key)
				}
			}
			s.solitaireDeliveryOrder[0] = nil
			s.solitaireDeliveryOrder = s.solitaireDeliveryOrder[1:]
		}
		encoded, _ := json.Marshal(keys[0])
		delivery = &solitaireDelivery{transactionID: fmt.Sprintf("wechat-solitaire-%x", sha256.Sum256(encoded))}
		s.solitaireDeliveryOrder = append(s.solitaireDeliveryOrder, delivery)
	}
	if s.solitaireDeliveries == nil {
		s.solitaireDeliveries = make(map[solitaireDeliveryKey]*solitaireDelivery)
	}
	// Disjoint reservations can become identifiable when a later delivery
	// contains both aliases. Prefer an acknowledged event and merge ALL known
	// aliases, not only those present on the current message.
	for other := range known {
		if other == delivery {
			continue
		}
		for _, key := range other.keys {
			if s.solitaireDeliveries[key] == other {
				s.solitaireDeliveries[key] = delivery
				delivery.keys = append(delivery.keys, key)
			}
		}
		other.keys = nil
	}
	for _, key := range keys {
		if s.solitaireDeliveries[key] != delivery {
			s.solitaireDeliveries[key] = delivery
			delivery.keys = append(delivery.keys, key)
		}
	}
	return delivery
}
