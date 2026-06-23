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
login               列出 WeChat 登录协议
login <flow>        使用指定协议登录，如 login ipad / login win / login mac
accounts            列出已保存的微信账号
use <wxid>          切换到已保存账号
logout              退出 WeChat
portals             列出已创建的 portal rooms
send <chat> <text>  发送文本到 WeChat wxid/chatroom

收到的 WeChat 会话会自动创建 Matrix portal room；在 portal room 里直接发送文本即可转发到 WeChat。`))
	case "status", "状态":
		ok, err := s.api.IsLoggedIn(ctx)
		if err != nil {
			s.notice(ctx, room, fmt.Sprintf("检查登录状态失败: %v", err))
			return
		}
		if !ok {
			if cur, exists := s.accounts.current(); exists {
				s.notice(ctx, room, fmt.Sprintf("当前选择账号: %s，但 session 未登录/已失效。发送 `login` 重新登录，或 `accounts` 查看账号。", cur.ID))
			} else {
				s.notice(ctx, room, "未登录。发送 `login` 查看可用登录协议。")
			}
			return
		}
		self, _ := s.api.Self(ctx)
		s.notice(ctx, room, fmt.Sprintf("已登录: %s\n当前后端: %s", self, s.currentProtocol()))
		s.startRemote(ctx)
	case "login", "登录":
		flow := strings.TrimSpace(rest)
		if flow == "" {
			s.notice(ctx, room, s.loginFlowHelp())
			return
		}
		s.portals.setOwner(sender)
		go s.runLogin(ctx, room, sender, flow)
	case "accounts", "账号":
		s.notice(ctx, room, s.accountsHelp())
	case "use", "切换":
		wxid := remoteapi.UserID(strings.TrimSpace(rest))
		if wxid == "" {
			s.notice(ctx, room, "用法: use <wxid>。发送 `accounts` 查看已保存账号。")
			return
		}
		if err := s.useSavedAccount(wxid); err != nil {
			s.notice(ctx, room, fmt.Sprintf("切换账号失败: %v", err))
			return
		}
		ok, err := s.api.IsLoggedIn(ctx)
		if err != nil {
			s.notice(ctx, room, fmt.Sprintf("已切换到 %s，但检查状态失败: %v", wxid, err))
			return
		}
		if ok {
			s.startRemote(ctx)
			s.notice(ctx, room, fmt.Sprintf("已切换并连接账号: %s", wxid))
		} else {
			s.notice(ctx, room, fmt.Sprintf("已切换到账号 %s，但 session 可能已失效，请重新 `login <flow>`。", wxid))
		}
	case "logout", "登出", "退出":
		if err := s.api.Logout(ctx); err != nil {
			s.notice(ctx, room, fmt.Sprintf("退出失败: %v", err))
			return
		}
		s.stopRemote()
		s.notice(ctx, room, "已退出 WeChat。")
	case "portals":
		items := s.portals.list()
		if len(items) == 0 {
			s.notice(ctx, room, "还没有 portal room。收到 WeChat 消息后会自动创建。")
			return
		}
		var b strings.Builder
		b.WriteString("Portals:\n")
		for _, p := range items {
			fmt.Fprintf(&b, "- %s -> %s (%s)\n", p.ChatID, p.RoomID, p.Name)
		}
		s.notice(ctx, room, b.String())
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

func (s *Server) loginFlowHelp() string {
	flows := s.api.LoginFlows()
	if len(flows) == 0 {
		return "当前后端没有可用登录协议。"
	}
	var b strings.Builder
	b.WriteString("请选择 WeChat 登录协议，例如 `login ipad`。可用协议:\n")
	for _, f := range flows {
		fmt.Fprintf(&b, "- %s: %s — %s\n", f.ID, f.Name, f.Description)
	}
	b.WriteString("\n已登录/保存账号请发送 `accounts`，切换账号发送 `use <wxid>`。")
	return b.String()
}

func (s *Server) hasLoginFlow(flowID string) bool {
	for _, f := range s.api.LoginFlows() {
		if f.ID == flowID {
			return true
		}
	}
	return false
}

func (s *Server) accountsHelp() string {
	accounts := s.accounts.list()
	if len(accounts) == 0 {
		return "还没有保存的微信账号。发送 `login` 查看协议，然后使用 `login <flow>` 扫码登录。"
	}
	cur, _ := s.accounts.current()
	var b strings.Builder
	b.WriteString("已保存微信账号:\n")
	for _, acc := range accounts {
		mark := " "
		if cur.ID == acc.ID {
			mark = "*"
		}
		fmt.Fprintf(&b, "%s %s  protocol=%s  login_flow=%s  updated=%s\n", mark, acc.ID, acc.Protocol, acc.LoginFlow, acc.UpdatedAt.Format("2006-01-02 15:04:05"))
	}
	b.WriteString("\n使用 `use <wxid>` 切换当前账号。")
	return b.String()
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

func (s *Server) runLogin(ctx context.Context, room id.RoomID, owner id.UserID, flowID string) {
	flows := s.api.LoginFlows()
	if len(flows) == 0 {
		s.notice(ctx, room, "后端没有提供登录 flow。")
		return
	}
	if !s.hasLoginFlow(flowID) {
		s.notice(ctx, room, "未知登录协议: "+flowID+"\n\n"+s.loginFlowHelp())
		return
	}
	proc, err := s.api.CreateLogin(ctx, flowID)
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
			s.notice(ctx, room, fmt.Sprintf("登录成功: %s\n登录协议: %s", step.Self, flowID))
			s.portals.setOwner(owner)
			s.saveSession(flowID)
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
