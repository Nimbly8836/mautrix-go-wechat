package matrixas

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

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
account <wxid>      查看 wxid 登录缓存，并设为当前登录账号
use <wxid>          切换到已保存账号
logout              退出 WeChat
space               创建/查看当前微信账号的 Matrix Space
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
	case "account", "账号信息", "logininfo":
		wxid := remoteapi.UserID(strings.TrimSpace(rest))
		if wxid == "" {
			if self, err := s.api.Self(ctx); err == nil && self != "" {
				wxid = self
			} else {
				s.notice(ctx, room, "用法: account <wxid>。如果该 wxid 在 API 里有登录缓存，会被设置为当前登录账号。")
				return
			}
		}
		s.portals.setOwner(sender)
		s.notice(ctx, room, s.activateAccountText(ctx, wxid))
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
			if _, err := s.ensureAccountSpace(ctx, wxid); err != nil {
				s.notice(ctx, room, fmt.Sprintf("已切换账号，但创建/获取 Space 失败: %v", err))
			}
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
	case "space", "空间":
		account := s.currentAccountID(ctx)
		if account == "" {
			s.notice(ctx, room, "当前没有选择/登录微信账号。先使用 `account <wxid>` 或 `login <flow>`。")
			return
		}
		space, err := s.ensureAccountSpace(ctx, account)
		if err != nil {
			s.notice(ctx, room, fmt.Sprintf("创建/获取 Space 失败: %v", err))
			return
		}
		s.notice(ctx, room, fmt.Sprintf("当前微信账号 Space: %s", space))
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
		fmt.Fprintf(&b, "%s %s  protocol=%s  login_flow=%s  space=%s  updated=%s\n", mark, acc.ID, acc.Protocol, acc.LoginFlow, acc.SpaceRoom, acc.UpdatedAt.Format("2006-01-02 15:04:05"))
	}
	b.WriteString("\n使用 `account <wxid>` 从服务端登录缓存激活账号，使用 `use <wxid>` 切换本地已保存账号。")
	return b.String()
}

func (s *Server) loginInfoText(ctx context.Context, wxid remoteapi.UserID) string {
	provider, ok := s.api.(remoteapi.LoginInfoProvider)
	if !ok {
		return "当前后端不支持查看登录缓存信息。"
	}
	info, err := provider.GetLoginInfo(ctx, wxid)
	if err != nil {
		return fmt.Sprintf("获取 %s 登录信息失败: %v", wxid, err)
	}
	if len(info.Fields) == 0 {
		return fmt.Sprintf("%s 没有返回登录缓存信息。", wxid)
	}
	return safeLoginInfoSummary(info)
}

func (s *Server) activateAccountText(ctx context.Context, wxid remoteapi.UserID) string {
	activator, ok := s.api.(remoteapi.CachedLoginActivator)
	if !ok {
		return s.loginInfoText(ctx, wxid)
	}
	info, err := activator.ActivateCachedLogin(ctx, wxid)
	if err != nil {
		return fmt.Sprintf("激活 %s 登录缓存失败: %v", wxid, err)
	}
	if len(info.Fields) == 0 {
		return fmt.Sprintf("%s 没有返回登录缓存信息，无法作为当前登录账号。", wxid)
	}
	s.saveSession("cache")
	if account := s.currentAccountID(ctx); account != "" {
		if _, err := s.ensureAccountSpace(ctx, account); err != nil {
			return fmt.Sprintf("已读取并保存 %s 的登录缓存，但创建 Space 失败: %v\n\n%s", wxid, err, safeLoginInfoSummary(info))
		}
	}
	if ok, err := s.api.IsLoggedIn(ctx); err != nil {
		return fmt.Sprintf("已读取并保存 %s 的登录缓存，但心跳检查失败: %v\n\n%s", wxid, err, safeLoginInfoSummary(info))
	} else if ok {
		s.startRemote(ctx)
		return fmt.Sprintf("已将 %s 设置为当前登录账号，并已连接。\n\n%s", wxid, safeLoginInfoSummary(info))
	}
	return fmt.Sprintf("已将 %s 设置为当前账号，但心跳检查未通过；缓存可能已过期。\n\n%s", wxid, safeLoginInfoSummary(info))
}

func safeLoginInfoSummary(info remoteapi.LoginInfo) string {
	f := info.Fields
	var b strings.Builder
	fmt.Fprintf(&b, "%s 登录信息摘要:\n", info.ID)
	appendField := func(label, key string) {
		if v, ok := f[key]; ok && fmt.Sprint(v) != "" && fmt.Sprint(v) != "<nil>" {
			fmt.Fprintf(&b, "- %s: %v\n", label, v)
		}
	}
	appendField("Wxid", "Wxid")
	appendField("Uin", "Uin")
	appendField("昵称", "NickName")
	appendField("Alias", "Alais")
	appendField("设备类型", "DeviceType")
	appendField("设备名", "DeviceName")
	appendField("客户端版本", "ClientVersion")
	appendField("短连接 Host", "ShortHost")
	appendField("长连接 Host", "LongHost")
	appendField("RomModel", "RomModel")
	appendField("OS", "OsVersion")
	if v, ok := f["Mobile"]; ok {
		fmt.Fprintf(&b, "- 手机: %s\n", maskString(fmt.Sprint(v), 3, 4))
	}
	if v, ok := f["Email"]; ok {
		fmt.Fprintf(&b, "- 邮箱: %s\n", maskEmail(fmt.Sprint(v)))
	}
	appendUnixField(&b, "登录时间", f["LoginDate"])
	appendUnixField(&b, "刷新 Token 时间", f["RefreshTokenDate"])
	if dev, ok := f["DeviceInfo"].(map[string]any); ok {
		if brand := fmt.Sprint(dev["devicebrand"]); brand != "" && brand != "<nil>" {
			fmt.Fprintf(&b, "- 设备品牌: %s\n", brand)
		}
		if ostype := fmt.Sprint(dev["ostype"]); ostype != "" && ostype != "<nil>" {
			fmt.Fprintf(&b, "- 设备 OS: %s\n", ostype)
		}
	}
	keys := make([]string, 0, len(f))
	for k := range f {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Fprintf(&b, "\n已返回 %d 个顶层字段。敏感密钥/session/token 已隐藏。字段列表:\n%s", len(keys), strings.Join(keys, ", "))
	return b.String()
}

func appendUnixField(b *strings.Builder, label string, raw any) {
	var ts int64
	switch v := raw.(type) {
	case float64:
		ts = int64(v)
	case int64:
		ts = v
	case int:
		ts = int64(v)
	default:
		return
	}
	if ts <= 0 {
		return
	}
	fmt.Fprintf(b, "- %s: %s\n", label, time.Unix(ts, 0).Format("2006-01-02 15:04:05"))
}

func maskString(s string, left, right int) string {
	if len(s) <= left+right {
		return strings.Repeat("*", len(s))
	}
	return s[:left] + strings.Repeat("*", len(s)-left-right) + s[len(s)-right:]
}

func maskEmail(s string) string {
	parts := strings.SplitN(s, "@", 2)
	if len(parts) != 2 {
		return maskString(s, 2, 2)
	}
	return maskString(parts[0], 2, 1) + "@" + parts[1]
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
			if _, err := s.ensureAccountSpace(ctx, step.Self); err != nil {
				s.notice(ctx, room, fmt.Sprintf("登录成功，但创建 Space 失败: %v", err))
			}
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
