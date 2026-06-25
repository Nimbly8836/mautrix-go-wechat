package matrixas

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

func (s *Server) handlePortalMediaMessage(ctx context.Context, rec portalRecord, sender id.UserID, msg *event.MessageEventContent, eventType event.Type) {
	if !s.allowed(sender) {
		s.sendPortalNotice(ctx, rec.RoomID, "你没有使用此 bridge 的权限。")
		return
	}
	if eventType != event.EventSticker && msg.MsgType != event.MsgImage && msg.MsgType != event.CapMsgSticker {
		return
	}
	if msg.URL == "" {
		s.sendPortalNotice(ctx, rec.RoomID, "发送到 WeChat 失败: Matrix 媒体缺少 URL。")
		return
	}
	contentURI, err := msg.URL.Parse()
	if err != nil {
		s.sendPortalNotice(ctx, rec.RoomID, fmt.Sprintf("发送到 WeChat 失败: Matrix 媒体 URL 无效: %v", err))
		return
	}
	data, err := s.as.BotIntent().Client.DownloadBytes(ctx, contentURI)
	if err != nil {
		s.sendPortalNotice(ctx, rec.RoomID, fmt.Sprintf("发送到 WeChat 失败: 下载 Matrix 媒体失败: %v", err))
		return
	}
	mimeType := ""
	if msg.Info != nil {
		mimeType = msg.Info.MimeType
	}
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}
	if !isGIFMedia(data, mimeType, msg.GetFileName()) {
		s.sendPortalNotice(ctx, rec.RoomID, "发送到 WeChat 失败: 微信 emoji/sticker 只能发送 GIF。")
		return
	}
	body := strings.TrimSpace(msg.Body)
	if body == "" {
		body = "[emoji]"
	}
	msgID, err := s.api.SendMessage(ctx, rec.ChatID, remoteapi.OutgoingMessage{
		Type:  remoteapi.MsgEmoji,
		Text:  body,
		Media: &remoteapi.Media{Data: data, MimeType: "image/gif", FileName: msg.GetFileName(), Size: int64(len(data))},
	})
	if err != nil {
		log.Printf("failed to send Matrix sticker to WeChat chat=%s sender=%s: %v", rec.ChatID, sender, err)
		s.sendPortalNotice(ctx, rec.RoomID, fmt.Sprintf("发送到 WeChat 失败: %v", err))
		return
	}
	log.Printf("sent Matrix sticker to WeChat: room=%s chat=%s sender=%s msg_id=%s", rec.RoomID, rec.ChatID, sender, msgID)
}

func isGIFMedia(data []byte, mimeType, fileName string) bool {
	if len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a") {
		return true
	}
	return strings.EqualFold(strings.Split(mimeType, ";")[0], "image/gif") || strings.HasSuffix(strings.ToLower(fileName), ".gif")
}

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
		s.applyPortalMetadata(ctx, rec, chat)
		s.sendPortalNotice(ctx, rec.RoomID, "已同步 portal 元数据。")
		return
	default:
		s.sendPortalNotice(ctx, rec.RoomID, fmt.Sprintf("未知命令。发送 `%s help` 查看帮助。", s.commandPrefix()))
		return
	}
}
