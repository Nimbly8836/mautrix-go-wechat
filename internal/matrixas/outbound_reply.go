package matrixas

import (
	"context"
	"fmt"
	"log"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// Work on a copy: local commands and wx_xml detection must see only the new
// reply in both representations, without mutating the event owned by mautrix.
func normalizePortalReply(msg *event.MessageEventContent) event.MessageEventContent {
	normalized := *msg
	if msg.RelatesTo.GetReplyTo() != "" {
		normalized.Body = event.TrimReplyFallbackText(msg.Body)
		normalized.FormattedBody = event.TrimReplyFallbackHTML(msg.FormattedBody)
	}
	return normalized
}

func (s *Server) sendPortalOutgoing(ctx context.Context, rec portalRecord, sender id.UserID, msg *event.MessageEventContent, outgoing remoteapi.OutgoingMessage, eventIDs ...id.EventID) (remoteapi.MessageID, error) {
	// Resolve, send and persist against one backend account, not three snapshots
	// which an account-switch command could change independently.
	s.accountTransitionMu.Lock()
	defer s.accountTransitionMu.Unlock()
	account := s.currentAccountID(ctx)
	if account == "" {
		return "", fmt.Errorf("无法确认当前 WeChat 账号")
	}
	if _, err := s.referenceStore(); err != nil {
		return "", err
	}
	if outgoing.Type == remoteapi.MsgText && msg.RelatesTo.GetReplaceID() == "" {
		target := msg.RelatesTo.GetNonFallbackReplyTo()
		if target != "" {
			ref, err := s.getMatrixMessageReference(account, rec, target)
			if err != nil {
				return "", err
			}
			if ref.NewID != "" && ref.Sender != "" {
				name := ref.SenderName
				if name == "" {
					name = string(ref.Sender)
				}
				quoteText := ref.QuoteText
				if quoteText == "" {
					quoteText = ref.Text
				}
				outgoing.Reply = &remoteapi.Reply{Type: ref.Type, Target: ref.NewID, TargetKey: "new:" + string(ref.NewID), Sender: ref.Sender, SenderName: name, Text: quoteText}
			} else {
				return "", fmt.Errorf("未找到原消息对应的微信消息 ID，本次引用未发送；请引用成功桥接后的消息，或取消引用后重新发送")
			}
		}
	}
	var result remoteapi.MessageSendResult
	var err error
	if qualified, ok := s.api.(remoteapi.QualifiedMessageSender); ok {
		result, err = qualified.SendMessageWithResult(ctx, rec.ChatID, outgoing)
	} else {
		result.ID, err = s.api.SendMessage(ctx, rec.ChatID, outgoing)
		if result.ID != "" && result.ID != "0" {
			result.Keys = []string{"id:" + string(result.ID)}
		}
	}
	if err != nil {
		return "", err
	}
	if len(eventIDs) > 0 && eventIDs[0] != "" {
		ref := messageReference{EventID: eventIDs[0], MatrixSender: sender, Sender: account, Type: outgoing.Type, Text: referenceSnapshot(outgoing.Text)}
		if result.Quote != nil {
			ref.Type, ref.Text = result.Quote.Type, referenceSnapshot(result.Quote.Body)
			if result.Quote.Text != result.Quote.Body {
				ref.QuoteText = referenceSnapshot(result.Quote.Text)
			}
		}
		if err = s.saveMessageReference(account, rec, result.Keys, ref); err != nil {
			// Sending succeeded: a storage error must never cause automatic re-send.
			log.Printf("ERROR: outbound message delivered but reference persistence failed: %v", err)
			s.sendPortalNotice(ctx, rec.RoomID, fmt.Sprintf("消息已发送，但引用记录保存失败；请勿重复发送：%v", err))
		}
	}
	return result.ID, nil
}
