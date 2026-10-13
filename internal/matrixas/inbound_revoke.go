package matrixas

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"strings"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func (s *Server) OnRedaction(ctx context.Context, r *remoteapi.Redaction) {
	if r == nil {
		return
	}
	account := s.currentAccountID(ctx)
	if r.Account != "" && r.Account != account {
		return
	}
	rec, ok := s.portals.get(r.Chat)
	if !ok || rec.RoomID == "" {
		return
	}
	body := strings.TrimSpace(r.Text)
	if body == "" {
		who := string(r.By)
		if r.By != "" && r.By != remoteapi.UserID(r.Chat) {
			if display := s.contactDisplayName(ctx, r.By); display != "" {
				who = display
			}
		}
		if who == "" || who == string(r.Chat) {
			who = "有人"
		}
		body = fmt.Sprintf("%s 撤回了一条消息", who)
	}
	content := &event.MessageEventContent{MsgType: event.MsgNotice, Body: body}
	reference := s.getMessageReference(account, rec, r.Target, r.TargetKey)
	if reference.EventID != "" && reference.Client != nil {
		// Redact as the actual original bridge sender. Do not impersonate an
		// arbitrary user named in the recall XML or rely on bot admin powers.
		targets := append([]id.EventID{reference.EventID}, reference.RelatedEventIDs...)
		confirmed := true
		for _, target := range targets {
			sum := sha256.Sum256([]byte(string(account) + "\x00" + string(rec.RoomID) + "\x00" + string(target)))
			redactCtx, cancelRedact := s.matrixOperationContext(ctx)
			response, err := reference.Client.RedactEvent(redactCtx, rec.RoomID, target, mautrix.ReqRedact{Reason: body, TxnID: fmt.Sprintf("wechat-revoke-%x", sum)})
			cancelRedact()
			if err != nil || response == nil || response.EventID == "" {
				log.Printf("failed to confirm Matrix recall in %s event=%s: %v", rec.RoomID, target, err)
				confirmed = false
			}
		}
		if !confirmed {
			content.Body += "（Matrix 原消息撤回未确认）"
		}
		content.RelatesTo = (&event.RelatesTo{}).SetReplyTo(reference.EventID)
	} else {
		content.Body += "（未找到对应的 Matrix 原消息，仅同步撤回提示）"
	}
	// A timed-out redaction must not consume the notice's own time budget.
	noticeCtx, cancelNotice := s.matrixOperationContext(ctx)
	defer cancelNotice()
	if _, err := s.as.BotIntent().SendMessageEvent(noticeCtx, rec.RoomID, event.EventMessage, content); err != nil {
		log.Printf("failed to send recall notice to %s: %v", rec.RoomID, err)
	}
}
