package matrixas

import (
	"context"
	"fmt"
	"log"

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
	// Allow prefixed local portal commands without leaking them to WeChat.
	cmd, _, ok := parsePortalCommand(body, s.commandPrefix())
	if !ok {
		msgID, err := s.api.SendMessage(ctx, rec.ChatID, remoteapi.OutgoingMessage{Type: remoteapi.MsgText, Text: body})
		if err != nil {
			log.Printf("failed to send Matrix msg to WeChat chat=%s sender=%s: %v", rec.ChatID, sender, err)
			s.sendPortalNotice(ctx, rec.RoomID, fmt.Sprintf("发送到 WeChat 失败: %v", err))
			return
		}
		log.Printf("sent Matrix msg to WeChat: room=%s chat=%s sender=%s msg_id=%s", rec.RoomID, rec.ChatID, sender, msgID)
		return
	}
	switch cmd {
	case "help":
		s.sendPortalNotice(ctx, rec.RoomID, fmt.Sprintf("这是 WeChat portal room。在这里发送文本会转发到 WeChat。命令: %s info, %s sync", s.commandPrefix(), s.commandPrefix()))
		return
	case "info":
		s.sendPortalNotice(ctx, rec.RoomID, fmt.Sprintf("WeChat chat: %s\nMatrix room: %s", rec.ChatID, rec.RoomID))
		return
	case "sync":
		chat, err := s.api.GetChat(ctx, rec.ChatID)
		if err != nil {
			s.sendPortalNotice(ctx, rec.RoomID, fmt.Sprintf("同步元数据失败: %v", err))
			return
		}
		name := s.portalRoomName(ctx, chat)
		if name != "" {
			rec.Name = name
			s.portals.set(rec)
			_, _ = s.as.BotIntent().SendStateEvent(ctx, rec.RoomID, event.StateRoomName, "", map[string]any{"name": name})
		}
		s.sendPortalNotice(ctx, rec.RoomID, "已同步 portal 元数据。")
		return
	default:
		s.sendPortalNotice(ctx, rec.RoomID, fmt.Sprintf("未知命令。发送 `%s help` 查看帮助。", s.commandPrefix()))
		return
	}
}
