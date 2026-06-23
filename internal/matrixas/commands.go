package matrixas

import (
	"context"
	"fmt"
	"strings"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

func (s *Server) handleCommand(ctx context.Context, room id.RoomID, sender id.UserID, body string) {
	if body == "" {
		return
	}
	if !s.allowed(sender) {
		s.notice(ctx, room, "你没有使用此 bridge 的权限，请在 config.yaml 的 bridge.permissions 中授权。")
		return
	}

	cmd, rest, _ := strings.Cut(body, " ")
	cmd = strings.TrimPrefix(strings.ToLower(cmd), "!")

	switch cmd {
	case "help", "帮助":
		s.notice(ctx, room, strings.TrimSpace(`可用命令:
help                显示帮助
status              查看登录状态
login               使用二维码登录 WeChat
logout              退出 WeChat
send <chat> <text>  发送文本到 WeChat wxid/chatroom

当前版本是最小 appservice 管理 bot:收到的 WeChat 消息会转发到这个管理房间。后续可扩展为真正 portal room。`))
	case "status", "状态":
		ok, err := s.api.IsLoggedIn(ctx)
		if err != nil {
			s.notice(ctx, room, fmt.Sprintf("检查登录状态失败: %v", err))
			return
		}
		if !ok {
			s.notice(ctx, room, "未登录。发送 `login` 开始扫码。")
			return
		}
		self, _ := s.api.Self(ctx)
		s.notice(ctx, room, fmt.Sprintf("已登录: %s", self))
		s.startRemote(ctx)
	case "login", "登录":
		go s.runLogin(ctx, room)
	case "logout", "登出", "退出":
		if err := s.api.Logout(ctx); err != nil {
			s.notice(ctx, room, fmt.Sprintf("退出失败: %v", err))
			return
		}
		s.stopRemote()
		s.notice(ctx, room, "已退出 WeChat。")
	case "send", "发送":
		chat, text, ok := strings.Cut(rest, " ")
		chat = strings.TrimSpace(chat)
		text = strings.TrimSpace(text)
		if !ok || chat == "" || text == "" {
			s.notice(ctx, room, "用法: send <wxid/chatroom> <text>")
			return
		}
		id, err := s.api.SendMessage(ctx, remoteapi.ChatID(chat), remoteapi.OutgoingMessage{Type: remoteapi.MsgText, Text: text})
		if err != nil {
			s.notice(ctx, room, fmt.Sprintf("发送失败: %v", err))
			return
		}
		s.notice(ctx, room, fmt.Sprintf("已发送: %s", id))
	default:
		// If it looks like a normal message, keep quiet to avoid noisy rooms.
		if strings.HasPrefix(body, "!") {
			s.notice(ctx, room, "未知命令。发送 `help` 查看帮助。")
		}
	}
}

func (s *Server) allowed(sender id.UserID) bool {
	perms := s.cfg.Bridge.Permissions
	if len(perms) == 0 {
		return true
	}
	if level := perms[sender.String()]; level != "" {
		return level != "relay"
	}
	_, domain, err := sender.Parse()
	if err != nil {
		return false
	}
	level := perms[domain]
	return level != "" && level != "relay"
}

func (s *Server) runLogin(ctx context.Context, room id.RoomID) {
	if ok, _ := s.api.IsLoggedIn(ctx); ok {
		self, _ := s.api.Self(ctx)
		s.notice(ctx, room, fmt.Sprintf("已经登录: %s", self))
		s.startRemote(ctx)
		return
	}

	flows := s.api.LoginFlows()
	if len(flows) == 0 {
		s.notice(ctx, room, "后端没有提供登录 flow。")
		return
	}
	proc, err := s.api.CreateLogin(ctx, flows[0].ID)
	if err != nil {
		s.notice(ctx, room, fmt.Sprintf("创建登录失败: %v", err))
		return
	}
	defer proc.Cancel()

	step, err := proc.Start(ctx)
	for {
		if err != nil {
			s.notice(ctx, room, fmt.Sprintf("登录失败: %v", err))
			return
		}
		switch step.Type {
		case remoteapi.LoginDisplayAndWait:
			s.handleDisplayStep(ctx, room, step)
			s.notice(ctx, room, "等待扫码/确认中...")
			step, err = proc.Wait(ctx)
		case remoteapi.LoginInputs:
			s.notice(ctx, room, "当前 flow 需要输入验证码/密码，但 Matrix 输入步骤尚未持久化。请先使用 QR 登录。")
			return
		case remoteapi.LoginComplete:
			s.notice(ctx, room, fmt.Sprintf("登录成功: %s", step.Self))
			s.saveSession()
			s.startRemote(ctx)
			return
		case remoteapi.LoginError:
			s.notice(ctx, room, fmt.Sprintf("登录失败: %v", step.Err))
			return
		default:
			s.notice(ctx, room, fmt.Sprintf("未知登录步骤: %s", step.Type))
			return
		}
	}
}

func (s *Server) handleDisplayStep(ctx context.Context, room id.RoomID, step *remoteapi.LoginStep) {
	if step.Display == nil {
		s.notice(ctx, room, step.Instructions)
		return
	}
	if step.Display.Type == "qr" {
		if img, ok := decodeQR(step.Display.Data); ok {
			if err := s.image(ctx, room, img, "WeChat 登录二维码"); err == nil {
				return
			}
		}
	}
	_, _ = s.as.BotIntent().SendMessageEvent(ctx, room, event.EventMessage, &event.MessageEventContent{
		MsgType: event.MsgNotice,
		Body:    fmt.Sprintf("%s\n%s", step.Instructions, step.Display.Data),
	})
}
