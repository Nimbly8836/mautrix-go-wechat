package matrixas

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

func parseBridgeCommand(body, commandPrefix string) (cmd, rest string, ok bool) {
	body = strings.TrimSpace(body)
	if body == "" {
		return "", "", false
	}
	lower := strings.ToLower(body)
	commandPrefix = strings.ToLower(commandPrefix)
	if lower == commandPrefix || strings.HasPrefix(lower, commandPrefix+" ") {
		rest = strings.TrimSpace(body[len(commandPrefix):])
		if rest == "" {
			return "help", "", true
		}
		cmd, rest, _ = strings.Cut(rest, " ")
		return strings.ToLower(cmd), strings.TrimSpace(rest), true
	}
	if strings.HasPrefix(body, "!") {
		return "", "", false
	}
	cmd, rest, _ = strings.Cut(body, " ")
	return strings.ToLower(cmd), strings.TrimSpace(rest), true
}

func parsePortalCommand(body, commandPrefix string) (cmd, rest string, ok bool) {
	body = strings.TrimSpace(body)
	lower := strings.ToLower(body)
	commandPrefix = strings.ToLower(commandPrefix)
	if lower != commandPrefix && !strings.HasPrefix(lower, commandPrefix+" ") {
		return "", "", false
	}
	rest = strings.TrimSpace(body[len(commandPrefix):])
	if rest == "" {
		return "help", "", true
	}
	cmd, rest, _ = strings.Cut(rest, " ")
	return strings.ToLower(cmd), strings.TrimSpace(rest), true
}

func (s *Server) commandPrefix() string {
	if s.cfg != nil && s.cfg.Bridge.CommandPrefix != "" {
		return s.cfg.Bridge.CommandPrefix
	}
	return "!wx"
}

func (s *Server) handleCommand(ctx context.Context, room id.RoomID, sender id.UserID, body string) {
	if body == "" {
		return
	}
	if !s.allowed(sender) {
		s.notice(ctx, room, "你没有使用此 bridge 的权限，请在 config.yaml 的 bridge.permissions 中授权。")
		return
	}

	cmd, rest, ok := parseBridgeCommand(body, s.commandPrefix())
	if !ok {
		return
	}

	switch cmd {
	case "help", "帮助":
		prefix := s.commandPrefix()
		s.notice(ctx, room, strings.TrimSpace(fmt.Sprintf(`可用命令:
%[1]s help                显示帮助
%[1]s status              查看登录状态
%[1]s login               列出 WeChat 登录协议
%[1]s login <flow>        使用指定协议登录，如 %[1]s login ipad / %[1]s login win / %[1]s login mac
%[1]s accounts            列出已保存的微信账号
%[1]s account <wxid>      查看 wxid 登录缓存，并设为当前登录账号
%[1]s use <wxid>          切换到已保存账号
%[1]s logout              退出 WeChat
%[1]s space               创建/查看当前微信账号的 Matrix Space，并挂载已有 portal
%[1]s stickers add <shortcode> <mxc-or-image-url> [label]  添加 sticker 到当前微信账号 Space
%[1]s contacts refresh    后台刷新联系人缓存，并更新一对一 portal 房间名
%[1]s contacts refresh <wxid/chatroom>  只刷新指定 portal/联系人
%[1]s relogin [wxid]      对当前或指定账号执行二次登录，并开启自动心跳
%[1]s portal-mode         查看/切换 portal 创建模式(auto/manual)
%[1]s pending             列出等待确认创建的会话
%[1]s create <chat|all>   手动创建 pending portal
%[1]s portals             列出已创建的 portal rooms
%[1]s send <chat> <text>  发送文本到 WeChat wxid/chatroom

收到的 WeChat 会话会自动创建 Matrix portal room；在 portal room 里直接发送文本即可转发到 WeChat。portal 本地命令使用 %[1]s info / %[1]s sync。`, prefix)))
	case "status", "状态":
		ok, err := s.api.IsLoggedIn(ctx)
		if err != nil {
			s.notice(ctx, room, fmt.Sprintf("检查登录状态失败: %v", err))
			return
		}
		if !ok {
			if cur, exists := s.accounts.current(); exists {
				s.notice(ctx, room, fmt.Sprintf("当前选择账号: %s，但 session 未登录/已失效。发送 `%s login` 重新登录，或 `%s accounts` 查看账号。", cur.ID, s.commandPrefix(), s.commandPrefix()))
			} else {
				s.notice(ctx, room, fmt.Sprintf("未登录。发送 `%s login` 查看可用登录协议。", s.commandPrefix()))
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
			s.notice(ctx, room, fmt.Sprintf("用法: use <wxid>。发送 `%s accounts` 查看已保存账号。", s.commandPrefix()))
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
			s.notice(ctx, room, fmt.Sprintf("已切换到账号 %s，但 session 可能已失效，请重新 `%s login <flow>`。", wxid, s.commandPrefix()))
		}
	case "logout", "登出", "退出":
		if err := s.api.Logout(ctx); err != nil {
			s.notice(ctx, room, fmt.Sprintf("退出失败: %v", err))
			return
		}
		s.stopRemote()
		s.notice(ctx, room, "已退出 WeChat。")
	case "contacts", "联系人":
		s.handleContactsCommand(ctx, room, rest)
	case "stickers", "sticker", "贴纸", "表情":
		s.handleStickersCommand(ctx, room, sender, rest)
	case "relogin", "twice-auth", "twice", "二次登录":
		go s.handleRefreshLoginCommand(ctx, room, rest)
	case "portal-mode":
		mode := strings.TrimSpace(strings.ToLower(rest))
		if mode == "" {
			s.notice(ctx, room, fmt.Sprintf("当前 portal 创建模式: %s\n可用: portal-mode auto / portal-mode manual", s.portalCreationMode()))
			return
		}
		if mode != "auto" && mode != "manual" {
			s.notice(ctx, room, "用法: portal-mode auto 或 portal-mode manual")
			return
		}
		s.cfg.Bridge.PortalCreation = mode
		s.notice(ctx, room, fmt.Sprintf("已切换 portal 创建模式为 %s。注意: 这是运行时设置，如需持久化请修改 config.yaml。", mode))
	case "pending":
		s.notice(ctx, room, s.pendingPortalsText())
	case "create", "创建":
		target := strings.TrimSpace(rest)
		if target == "" {
			s.notice(ctx, room, "用法: create <chat> 或 create all。发送 pending 查看待创建会话。")
			return
		}
		go s.createPendingPortalCommand(ctx, room, target)
	case "space", "空间":
		account := s.currentAccountID(ctx)
		if account == "" {
			s.notice(ctx, room, fmt.Sprintf("当前没有选择/登录微信账号。先使用 `%s account <wxid>` 或 `%s login <flow>`。", s.commandPrefix(), s.commandPrefix()))
			return
		}
		s.portals.setOwner(sender)
		space, folders, err := s.ensureAccountSpaceVisibleTo(ctx, account, sender)
		if err != nil {
			s.notice(ctx, room, fmt.Sprintf("创建/获取 Space 失败: %v", err))
			return
		}
		s.attachExistingPortalsToAccountSpace(ctx, account, space)
		s.notice(ctx, room, fmt.Sprintf("当前微信账号 Space: %s\n分类 Space:\n- 联系人: %s\n- 公众号: %s\n- 群聊: %s\n\n已邀请你加入这些 Space，并重新整理已有 portal。", space, folders[spaceFolderContacts], folders[spaceFolderOfficial], folders[spaceFolderGroups]))
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
		if strings.HasPrefix(body, s.commandPrefix()) {
			s.notice(ctx, room, fmt.Sprintf("未知命令。发送 `%s help` 查看帮助。", s.commandPrefix()))
		}
	}
}

func (s *Server) beginContactRefresh(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.contactRefresh == nil {
		s.contactRefresh = map[string]bool{}
	}
	if s.contactRefresh[key] {
		return false
	}
	s.contactRefresh[key] = true
	return true
}

func (s *Server) finishContactRefresh(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.contactRefresh, key)
}

func (s *Server) handleRefreshLoginCommand(ctx context.Context, room id.RoomID, rest string) {
	wxid := remoteapi.UserID(strings.TrimSpace(rest))
	if wxid == "" {
		wxid = s.currentAccountID(ctx)
	}
	if wxid == "" {
		s.notice(ctx, room, fmt.Sprintf("用法: %s relogin [wxid]。先使用 `%s account <wxid>` 或 `%s login <flow>` 选择账号。", s.commandPrefix(), s.commandPrefix(), s.commandPrefix()))
		return
	}
	refresher, ok := s.api.(remoteapi.LoginRefresher)
	if !ok {
		s.notice(ctx, room, "当前后端不支持二次登录。")
		return
	}
	if err := refresher.RefreshLogin(ctx, wxid); err != nil {
		s.notice(ctx, room, fmt.Sprintf("二次登录失败: %v", err))
		return
	}
	s.saveSession("twice")
	if _, err := s.ensureAccountSpace(ctx, wxid); err != nil {
		s.notice(ctx, room, fmt.Sprintf("二次登录成功，但创建 Space 失败: %v", err))
	}
	s.startRemote(ctx)
	s.notice(ctx, room, fmt.Sprintf("已对 %s 完成二次登录并开启自动心跳。", wxid))
}

func (s *Server) handleContactsCommand(ctx context.Context, room id.RoomID, rest string) {
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		s.notice(ctx, room, fmt.Sprintf("用法: %s contacts refresh [wxid/chatroom]", s.commandPrefix()))
		return
	}
	action := strings.ToLower(fields[0])
	if action != "refresh" && action != "刷新" {
		s.notice(ctx, room, fmt.Sprintf("未知 contacts 命令。用法: %s contacts refresh [wxid/chatroom]", s.commandPrefix()))
		return
	}
	account := s.currentAccountID(ctx)
	if account == "" {
		s.notice(ctx, room, fmt.Sprintf("当前没有选择/登录微信账号。先使用 `%s account <wxid>` 或 `%s login <flow>`。", s.commandPrefix(), s.commandPrefix()))
		return
	}
	if len(fields) > 1 {
		s.startSingleContactRefresh(ctx, room, account, remoteapi.ChatID(fields[1]))
		return
	}
	s.startFullContactRefresh(ctx, room, account)
}

func (s *Server) startFullContactRefresh(ctx context.Context, room id.RoomID, account remoteapi.UserID) {
	key := "full:" + string(account)
	if !s.beginContactRefresh(key) {
		s.notice(ctx, room, fmt.Sprintf("账号 %s 的联系人缓存正在后台刷新中，请稍后再试。", account))
		return
	}
	s.notice(ctx, room, fmt.Sprintf("已开始后台刷新账号 %s 的联系人缓存。完成后会在这里通知。", account))
	bg := context.WithoutCancel(ctx)
	go func() {
		defer s.finishContactRefresh(key)
		count, err := s.refreshContactCache(bg, account)
		if err != nil {
			s.notice(ctx, room, fmt.Sprintf("刷新联系人缓存失败: %v", err))
			return
		}
		updated, failed, members, memberFailed := s.syncPortalMetadata(bg, account)
		s.notice(ctx, room, fmt.Sprintf("已刷新联系人缓存: %d 个联系人。已同步 %d 个 portal 元数据，失败 %d 个；已同步 %d 个成员/puppet，失败 %d 个。", count, updated, failed, members, memberFailed))
	}()
}

func (s *Server) startSingleContactRefresh(ctx context.Context, room id.RoomID, account remoteapi.UserID, chatID remoteapi.ChatID) {
	if strings.TrimSpace(string(chatID)) == "" {
		s.notice(ctx, room, fmt.Sprintf("用法: %s contacts refresh <wxid/chatroom>", s.commandPrefix()))
		return
	}
	key := "single:" + string(account) + ":" + string(chatID)
	if !s.beginContactRefresh(key) {
		s.notice(ctx, room, fmt.Sprintf("%s 正在后台刷新中，请稍后再试。", chatID))
		return
	}
	s.notice(ctx, room, fmt.Sprintf("已开始后台刷新 %s。完成后会在这里通知。", chatID))
	bg := context.WithoutCancel(ctx)
	go func() {
		defer s.finishContactRefresh(key)
		if rec, ok := s.portals.get(chatID); ok && rec.RoomID != "" {
			chat, err := s.api.GetChat(bg, rec.ChatID)
			if err != nil {
				s.notice(ctx, room, fmt.Sprintf("刷新 %s 失败: %v", chatID, err))
				return
			}
			chat = normalizePortalChat(rec, chat)
			s.applyPortalMetadata(bg, rec, chat)
			joined, failed := s.syncPortalMembers(bg, rec, chat, true)
			s.addPortalToAccountSpace(bg, account, rec)
			s.notice(ctx, room, fmt.Sprintf("已刷新 %s。%s", chatID, s.portalRefreshDebug(chat, joined, failed)))
			return
		}
		ct, ok := s.contactForRemote(bg, remoteapi.UserID(chatID), true)
		if !ok {
			s.notice(ctx, room, fmt.Sprintf("刷新联系人 %s 失败。", chatID))
			return
		}
		s.notice(ctx, room, fmt.Sprintf("已刷新联系人 %s: %s", chatID, matrixPick(ct.Remark, ct.Name, string(ct.ID))))
	}()
}

func (s *Server) syncPortalMetadata(ctx context.Context, account remoteapi.UserID) (updated, failed, members, memberFailed int) {
	if s.portals == nil {
		return 0, 0, 0, 0
	}
	for _, rec := range s.portals.list() {
		if rec.RoomID == "" {
			continue
		}
		chat, err := s.api.GetChat(ctx, rec.ChatID)
		if err != nil {
			failed++
			log.Printf("failed to refresh portal metadata for %s: %v", rec.ChatID, err)
			continue
		}
		chat = normalizePortalChat(rec, chat)
		if !rec.IsGroup && chat.Avatar == "" && s.contacts != nil {
			if ct, ok := s.contacts.get(account, remoteapi.UserID(rec.ChatID)); ok {
				chat.Avatar = ct.AvatarURL
			}
		}
		s.applyPortalMetadata(ctx, rec, chat)
		joined, failedMembers := s.syncPortalMembers(ctx, rec, chat, true)
		members += joined
		memberFailed += failedMembers
		updated++
	}
	return updated, failed, members, memberFailed
}

func (s *Server) portalCreationMode() string {
	mode := strings.ToLower(strings.TrimSpace(s.cfg.Bridge.PortalCreation))
	if mode == "" {
		return "auto"
	}
	return mode
}

func (s *Server) pendingPortalsText() string {
	items := s.portals.pendingList()
	if len(items) == 0 {
		return "没有等待确认创建的 WeChat 会话。"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "等待创建的 WeChat 会话(%d):\n", len(items))
	for _, p := range items {
		fmt.Fprintf(&b, "- %s  name=%s  sender=%s  count=%d  preview=%s\n", p.ChatID, p.Name, p.Sender, p.Count, trimPreview(p.LastMessage))
	}
	fmt.Fprintf(&b, "\n发送 `%s create <chat>` 创建指定 portal，或 `%s create all` 全部创建。", s.commandPrefix(), s.commandPrefix())
	return b.String()
}

func (s *Server) createPendingPortalCommand(ctx context.Context, room id.RoomID, target string) {
	if strings.EqualFold(target, "all") {
		items := s.portals.pendingList()
		if len(items) == 0 {
			s.notice(ctx, room, "没有待创建会话。")
			return
		}
		created := 0
		for _, p := range items {
			if _, err := s.ensurePortal(ctx, p.ChatID); err != nil {
				s.notice(ctx, room, fmt.Sprintf("创建 %s 失败: %v", p.ChatID, err))
				continue
			}
			created++
		}
		s.notice(ctx, room, fmt.Sprintf("已创建 %d 个 pending portal。", created))
		return
	}
	chat := remoteapi.ChatID(target)
	if _, ok := s.portals.pending(chat); !ok {
		if rec, exists := s.portals.get(chat); exists && rec.RoomID != "" {
			s.notice(ctx, room, fmt.Sprintf("portal 已存在: %s -> %s", rec.ChatID, rec.RoomID))
			return
		}
		s.notice(ctx, room, fmt.Sprintf("没有找到 pending 会话 %s。发送 pending 查看。", chat))
		return
	}
	rec, err := s.ensurePortal(ctx, chat)
	if err != nil {
		s.notice(ctx, room, fmt.Sprintf("创建 portal 失败: %v", err))
		return
	}
	s.notice(ctx, room, fmt.Sprintf("已创建 WeChat portal: %s -> %s", rec.ChatID, rec.RoomID))
}

func (s *Server) loginFlowHelp() string {
	flows := s.api.LoginFlows()
	if len(flows) == 0 {
		return "当前后端没有可用登录协议。"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "请选择 WeChat 登录协议，例如 `%s login ipad`。可用协议:\n", s.commandPrefix())
	for _, f := range flows {
		fmt.Fprintf(&b, "- %s: %s — %s\n", f.ID, f.Name, f.Description)
	}
	fmt.Fprintf(&b, "\n已登录/保存账号请发送 `%s accounts`，切换账号发送 `%s use <wxid>`。", s.commandPrefix(), s.commandPrefix())
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
		return fmt.Sprintf("还没有保存的微信账号。发送 `%s login` 查看协议，然后使用 `%s login <flow>` 扫码登录。", s.commandPrefix(), s.commandPrefix())
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
	fmt.Fprintf(&b, "\n使用 `%s account <wxid>` 从服务端登录缓存激活账号，使用 `%s use <wxid>` 切换本地已保存账号。", s.commandPrefix(), s.commandPrefix())
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
