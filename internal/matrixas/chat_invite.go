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

type pendingChatRoomInvite struct {
	Account remoteapi.UserID
	Room    id.RoomID
	Owner   id.UserID
	Invite  *remoteapi.ChatRoomInvite
}

func chatInviteKey(account remoteapi.UserID, invite *remoteapi.ChatRoomInvite) string {
	if invite == nil {
		return ""
	}
	return string(account) + "\x00" + invite.URL
}

func (s *Server) bridgeChatRoomInvite(ctx context.Context, account remoteapi.UserID, rec portalRecord, m *remoteapi.InboundMessage) {
	if m == nil || m.ChatInvite == nil || rec.RoomID == "" || account == "" {
		return
	}
	if s.portals.owner() == "" || !s.allowed(s.portals.owner()) {
		return
	}
	key := chatInviteKey(account, m.ChatInvite)
	s.chatInviteMu.Lock()
	if s.chatInviteAccepted[key] || s.chatInviteKeys[key] != "" || s.chatInviteProcessing[key] {
		s.chatInviteMu.Unlock()
		return
	}
	if s.chatInviteProcessing == nil {
		s.chatInviteProcessing = map[string]bool{}
	}
	// Reserve the URL before sending either event. Webhook/local-poll duplicates
	// can arrive concurrently, and both must not create two action polls.
	s.chatInviteProcessing[key] = true
	s.chatInviteMu.Unlock()
	clearReservation := func() {
		s.chatInviteMu.Lock()
		delete(s.chatInviteProcessing, key)
		s.chatInviteMu.Unlock()
	}

	body := "收到群聊邀请"
	if m.ChatInvite.Inviter != "" {
		body += "\n邀请人：" + m.ChatInvite.Inviter
	}
	if m.ChatInvite.ChatName != "" {
		body += "\n群聊：" + m.ChatInvite.ChatName
	}
	body += "\n请点击下面的“接收邀请”按钮；桥接会通过当前 WeChat 登录协议接受，不会在浏览器打开邀请链接。"
	noticeCtx, cancel := s.matrixOperationContext(ctx)
	_, err := s.as.BotIntent().SendMessageEvent(noticeCtx, rec.RoomID, event.EventMessage, &event.MessageEventContent{MsgType: event.MsgNotice, Body: body})
	cancel()
	if err != nil {
		clearReservation()
		log.Printf("failed to send group invitation notice room=%s: %v", rec.RoomID, err)
		return
	}

	poll := &event.PollStartEventContent{}
	poll.PollStart.Kind = "org.matrix.msc3381.poll.disclosed"
	poll.PollStart.MaxSelections = 1
	poll.PollStart.Question.Text = "是否接收这个群聊邀请？"
	poll.PollStart.Answers = []struct {
		ID string `json:"id"`
		event.MSC1767Message
	}{{ID: "accept", MSC1767Message: event.MSC1767Message{Text: "接收邀请"}}}
	pollCtx, cancelPoll := s.matrixOperationContext(ctx)
	resp, err := s.as.BotIntent().SendMessageEvent(pollCtx, rec.RoomID, event.EventUnstablePollStart, poll)
	cancelPoll()
	if err != nil || resp == nil || resp.EventID == "" {
		clearReservation()
		log.Printf("failed to send group invitation action room=%s: %v", rec.RoomID, err)
		return
	}
	s.chatInviteMu.Lock()
	if s.chatInvites == nil {
		s.chatInvites = map[id.EventID]pendingChatRoomInvite{}
	}
	if s.chatInviteKeys == nil {
		s.chatInviteKeys = map[string]id.EventID{}
	}
	s.chatInvites[resp.EventID] = pendingChatRoomInvite{Account: account, Room: rec.RoomID, Owner: s.portals.owner(), Invite: m.ChatInvite}
	s.chatInviteKeys[key] = resp.EventID
	delete(s.chatInviteProcessing, key)
	s.chatInviteMu.Unlock()
}

func (s *Server) handleChatRoomInviteResponse(ctx context.Context, evt *event.Event) {
	if evt == nil || evt.RoomID == "" {
		return
	}
	response := event.CastOrDefault[event.PollResponseEventContent](&evt.Content)
	if response == nil {
		return
	}
	pollID := response.RelatesTo.GetReferenceID()
	if pollID == "" {
		pollID = response.RelatesTo.EventID
	}
	if pollID == "" {
		return
	}
	s.chatInviteMu.Lock()
	pending, ok := s.chatInvites[id.EventID(pollID)]
	if !ok {
		s.chatInviteMu.Unlock()
		return
	}
	key := chatInviteKey(pending.Account, pending.Invite)
	if len(response.Response.Answers) != 1 || response.Response.Answers[0] != "accept" || pending.Room != evt.RoomID || pending.Owner != evt.Sender || !s.allowed(evt.Sender) || s.chatInviteProcessing[key] {
		s.chatInviteMu.Unlock()
		return
	}
	if s.chatInviteProcessing == nil {
		s.chatInviteProcessing = map[string]bool{}
	}
	s.chatInviteProcessing[key] = true
	s.chatInviteMu.Unlock()
	clearProcessing := func() {
		s.chatInviteMu.Lock()
		delete(s.chatInviteProcessing, key)
		s.chatInviteMu.Unlock()
	}

	if pending.Account != s.currentAccountID(ctx) {
		clearProcessing()
		s.sendPortalNotice(ctx, pending.Room, "当前 WeChat 账号已切换，未执行这个邀请。请等待新的邀请提示后再接收。")
		return
	}
	acceptor, ok := s.api.(remoteapi.ChatRoomInviteAcceptor)
	if !ok {
		clearProcessing()
		s.sendPortalNotice(ctx, pending.Room, "当前 WeChat 后端不支持协议内接收群聊邀请。")
		return
	}
	operationCtx, cancel := s.matrixOperationContext(ctx)
	chatID, err := acceptor.AcceptChatRoomInvite(operationCtx, pending.Invite.URL)
	cancel()
	if err != nil {
		clearProcessing()
		s.sendPortalNotice(ctx, pending.Room, fmt.Sprintf("接收群聊邀请失败：%v\n没有自动重试，请确认微信中的邀请状态后再点击。", err))
		return
	}
	if chatID == "" || !strings.HasSuffix(string(chatID), "@chatroom") {
		clearProcessing()
		s.sendPortalNotice(ctx, pending.Room, "已收到接收成功回执，但协议没有返回可验证的群 ID；未创建未知群房间。")
		return
	}
	s.chatInviteMu.Lock()
	delete(s.chatInvites, id.EventID(pollID))
	delete(s.chatInviteKeys, key)
	delete(s.chatInviteProcessing, key)
	if s.chatInviteAccepted == nil {
		s.chatInviteAccepted = map[string]bool{}
	}
	s.chatInviteAccepted[key] = true
	s.chatInviteMu.Unlock()
	rec, err := s.ensurePortal(ctx, chatID)
	if err != nil {
		s.sendPortalNotice(ctx, pending.Room, fmt.Sprintf("已接受群聊邀请（%s），但创建 Matrix portal 失败：%v", chatID, err))
		return
	}
	s.sendPortalNotice(ctx, pending.Room, fmt.Sprintf("已接受群聊邀请，已创建/复用 Matrix portal：%s", rec.RoomID))
}
