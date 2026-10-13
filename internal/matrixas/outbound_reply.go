package matrixas

import (
	"context"
	"fmt"
	"log"
	"strings"

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
				outgoing.Text = s.readableOutgoingReply(ctx, rec, target, ref, outgoing.Text)
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

func (s *Server) readableOutgoingReply(ctx context.Context, rec portalRecord, target id.EventID, ref messageReference, body string) string {
	quote, name := ref.Text, ref.SenderName
	if name == "" {
		name = string(ref.Sender)
	}
	if ref.EventID == "" && s.as != nil {
		fetchCtx, cancel := s.matrixOperationContext(ctx)
		original, err := s.as.BotIntent().Client.GetEvent(fetchCtx, rec.RoomID, target)
		cancel()
		if err == nil && original != nil && original.ID == target && original.RoomID == rec.RoomID && original.Type == event.EventMessage {
			// Only text for a readable fallback. No remote IDs, namespaced extension
			// fields, or body matching can upgrade a fetched event to a native quote.
			_ = original.Content.ParseRaw(original.Type)
			content := original.Content.AsMessage()
			if content != nil {
				normalized := normalizePortalReply(content)
				quote = referenceSnapshot(normalized.Body)
				name = string(original.Sender)
			}
		}
	}
	if quote == "" {
		return "[引用原消息不可用，以下为新回复]\n" + body
	}
	if name == "" {
		name = "原消息"
	}
	return fmt.Sprintf("引用 %s（原生引用不可用）：\n> %s\n\n%s", name, strings.ReplaceAll(quote, "\n", "\n> "), body)
}
