package matrixas

import (
	"context"
	"fmt"
	"log"
	"strings"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

func (s *Server) handlePortalMessage(ctx context.Context, rec portalRecord, sender id.UserID, body string) {
	if body == "" {
		return
	}
	if !s.allowed(sender) {
		s.sendPortalNotice(ctx, rec.RoomID, "你没有使用此 bridge 的权限。")
		return
	}
	// Allow a few local portal commands without leaking them to WeChat.
	cmd := strings.TrimPrefix(strings.ToLower(strings.Fields(body)[0]), "!")
	switch cmd {
	case "help":
		s.sendPortalNotice(ctx, rec.RoomID, "这是 WeChat portal room。在这里发送文本会转发到 WeChat。命令: !info, !sync/!refresh（仅刷新当前房间）")
		return
	case "info":
		s.sendPortalNotice(ctx, rec.RoomID, fmt.Sprintf("WeChat chat: %s\nMatrix room: %s", rec.ChatID, rec.RoomID))
		return
	case "sync", "refresh", "刷新":
		// Single-room refresh only: do not refresh the whole contact cache.
		chat, err := s.api.GetChat(ctx, rec.ChatID)
		if err != nil {
			s.sendPortalNotice(ctx, rec.RoomID, fmt.Sprintf("同步元数据失败: %v", err))
			return
		}
		if chat.Name != "" {
			rec.Name = chat.Name
			s.portals.set(rec)
			_, _ = s.as.BotIntent().SendStateEvent(ctx, rec.RoomID, event.StateRoomName, "", map[string]any{"name": chat.Name + " (WeChat)"})
		}
		s.addPortalToAccountSpace(ctx, s.currentAccountID(ctx), rec)
		s.sendPortalNotice(ctx, rec.RoomID, "已刷新当前 portal 房间元数据。")
		return
	}

	msgID, err := s.api.SendMessage(ctx, rec.ChatID, remoteapi.OutgoingMessage{Type: remoteapi.MsgText, Text: body})
	if err != nil {
		log.Printf("failed to send Matrix msg to WeChat chat=%s sender=%s: %v", rec.ChatID, sender, err)
		s.sendPortalNotice(ctx, rec.RoomID, fmt.Sprintf("发送到 WeChat 失败: %v", err))
		return
	}
	log.Printf("sent Matrix msg to WeChat: room=%s chat=%s sender=%s msg_id=%s", rec.RoomID, rec.ChatID, sender, msgID)
}
