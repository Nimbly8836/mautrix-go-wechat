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
	"github.com/leat/mautrix-go-wechat/internal/wechat/textemoji"
)

func (s *Server) handlePortalMediaMessage(ctx context.Context, rec portalRecord, sender id.UserID, msg *event.MessageEventContent, eventType event.Type) {
	if !s.allowed(sender) {
		s.sendPortalNotice(ctx, rec.RoomID, "你没有使用此 bridge 的权限。")
		return
	}
	if eventType != event.EventSticker && msg.MsgType != event.MsgImage && msg.MsgType != event.MsgVideo && msg.MsgType != event.MsgAudio && msg.MsgType != event.MsgFile && msg.MsgType != event.CapMsgSticker {
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
	outgoing, err := portalMediaToRemoteMessage(data, mimeType, msg.GetFileName(), msg.Body, msg.MsgType, eventType)
	if err == nil && msg.MsgType == event.MsgAudio && msg.Info != nil && msg.Info.Duration > 0 {
		if outgoing.Extra == nil {
			outgoing.Extra = map[string]any{}
		}
		outgoing.Extra["duration"] = msg.Info.Duration
	}
	if err != nil {
		s.sendPortalNotice(ctx, rec.RoomID, fmt.Sprintf("发送到 WeChat 失败: %v", err))
		return
	}
	msgID, err := s.api.SendMessage(ctx, rec.ChatID, outgoing)
	if err != nil {
		log.Printf("failed to send Matrix media to WeChat chat=%s sender=%s msgtype=%s: %v", rec.ChatID, sender, msg.MsgType, err)
		s.sendPortalNotice(ctx, rec.RoomID, fmt.Sprintf("发送到 WeChat 失败: %v", err))
		return
	}
	log.Printf("sent Matrix media to WeChat: room=%s chat=%s sender=%s msgtype=%s msg_id=%s", rec.RoomID, rec.ChatID, sender, msg.MsgType, msgID)
}

func portalMediaToRemoteMessage(data []byte, mimeType, fileName, body string, msgType event.MessageType, eventType event.Type) (remoteapi.OutgoingMessage, error) {
	body = strings.TrimSpace(body)
	if eventType == event.EventSticker || msgType == event.CapMsgSticker {
		if !isGIFMedia(data, mimeType, fileName) {
			return remoteapi.OutgoingMessage{}, fmt.Errorf("微信 emoji/sticker 只能发送 GIF。")
		}
		if body == "" {
			body = "[emoji]"
		}
		return remoteapi.OutgoingMessage{
			Type:  remoteapi.MsgEmoji,
			Text:  body,
			Media: &remoteapi.Media{Data: data, MimeType: "image/gif", FileName: fileName, Size: int64(len(data))},
		}, nil
	}
	if msgType == event.MsgImage {
		if body == "" {
			body = "[image]"
		}
		return remoteapi.OutgoingMessage{
			Type:  remoteapi.MsgImage,
			Text:  body,
			Media: &remoteapi.Media{Data: data, MimeType: mimeType, FileName: fileName, Size: int64(len(data))},
		}, nil
	}
	if msgType == event.MsgVideo {
		if body == "" {
			body = fileName
		}
		if body == "" {
			body = "[video]"
		}
		return remoteapi.OutgoingMessage{
			Type:  remoteapi.MsgVideo,
			Text:  body,
			Media: &remoteapi.Media{Data: data, MimeType: mimeType, FileName: fileName, Size: int64(len(data))},
		}, nil
	}
	if msgType == event.MsgAudio {
		if body == "" {
			body = fileName
		}
		if body == "" {
			body = "[voice]"
		}
		return remoteapi.OutgoingMessage{
			Type:  remoteapi.MsgAudio,
			Text:  body,
			Media: &remoteapi.Media{Data: data, MimeType: mimeType, FileName: fileName, Size: int64(len(data))},
		}, nil
	}
	if msgType == event.MsgFile {
		if body == "" {
			body = fileName
		}
		if body == "" {
			body = "[file]"
		}
		return remoteapi.OutgoingMessage{
			Type:  remoteapi.MsgFile,
			Text:  body,
			Media: &remoteapi.Media{Data: data, MimeType: mimeType, FileName: fileName, Size: int64(len(data))},
		}, nil
	}
	return remoteapi.OutgoingMessage{}, fmt.Errorf("暂不支持的 Matrix 媒体类型: %s", msgType)
}

func isGIFMedia(data []byte, mimeType, fileName string) bool {
	if len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a") {
		return true
	}
	return strings.EqualFold(strings.Split(mimeType, ";")[0], "image/gif") || strings.HasSuffix(strings.ToLower(fileName), ".gif")
}

func (s *Server) handlePortalMessage(ctx context.Context, rec portalRecord, sender id.UserID, msg *event.MessageEventContent) {
	body := msg.Body
	if strings.TrimSpace(body) == "" {
		return
	}
	if !s.allowed(sender) {
		s.sendPortalNotice(ctx, rec.RoomID, "你没有使用此 bridge 的权限。")
		return
	}
	// Allow prefixed local portal commands without leaking them to WeChat.
	cmd, _, ok := parsePortalCommand(body, s.commandPrefix())
	if !ok {
		payload, isXML, err := portalXML(msg)
		if err != nil {
			s.sendPortalNotice(ctx, rec.RoomID, fmt.Sprintf("发送到 WeChat 失败: %v", err))
			return
		}
		var outgoing remoteapi.OutgoingMessage
		if isXML {
			outgoing = remoteapi.OutgoingMessage{Type: remoteapi.MsgApp, Text: payload}
		} else {
			outgoing = remoteapi.OutgoingMessage{Type: remoteapi.MsgText, Text: textemoji.ToChineseTokens(body)}
		}
		msgID, err := s.api.SendMessage(ctx, rec.ChatID, outgoing)
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
		s.sendPortalNotice(ctx, rec.RoomID, fmt.Sprintf("这是 WeChat portal room。在这里发送文本会转发到 WeChat；独立的 wx_xml 代码块会作为自定义 App XML 发送，Type 从 appmsg/type 自动读取。回复带提示的接龙消息并发送「%s 接龙 你的记录」可参与。命令: %s info, %s sync/%s refresh（仅刷新当前房间）", s.commandPrefix(), s.commandPrefix(), s.commandPrefix(), s.commandPrefix()))
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
		chat = normalizePortalChat(rec, chat)
		s.applyPortalMetadata(ctx, rec, chat)
		joined, memberFailed := s.syncPortalMembers(ctx, rec, chat, true, puppetProfileUpdateCommand)
		if err := s.addPortalToAccountSpace(ctx, s.currentAccountID(ctx), rec); err != nil {
			s.sendPortalNotice(ctx, rec.RoomID, fmt.Sprintf("已刷新当前 portal 房间元数据，但更新账号 Space 失败: %v", err))
			return
		}
		s.sendPortalNotice(ctx, rec.RoomID, fmt.Sprintf("已刷新当前 portal 房间元数据。%s", s.portalRefreshDebug(chat, joined, memberFailed)))
		return
	default:
		s.sendPortalNotice(ctx, rec.RoomID, fmt.Sprintf("未知命令。发送 `%s help` 查看帮助。", s.commandPrefix()))
		return
	}
}
