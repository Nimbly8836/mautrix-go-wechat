package matrixas

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"maunium.net/go/mautrix/id"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

const (
	officialAccountModeInvite = "invite"
	officialAccountModeMute   = "mute"
	officialAccountModeReject = "reject"
)

func normalizeOfficialAccountMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case officialAccountModeMute:
		return officialAccountModeMute
	case officialAccountModeReject:
		return officialAccountModeReject
	default:
		return officialAccountModeInvite
	}
}

func officialAccountModeInvitesOwner(mode string, official bool) bool {
	if !official {
		return true
	}
	return normalizeOfficialAccountMode(mode) == officialAccountModeInvite
}

func officialAccountModeCreatesPortal(mode string, official bool) bool {
	if !official {
		return true
	}
	return normalizeOfficialAccountMode(mode) != officialAccountModeReject
}

var errOfficialAccountRejected = errors.New("official account portal rejected by configuration")

func (s *Server) officialAccountMode() string {
	if s == nil || s.cfg == nil {
		return officialAccountModeInvite
	}
	return normalizeOfficialAccountMode(s.cfg.Bridge.OfficialAccountMode)
}

func (s *Server) setOfficialAccountMode(mode string) string {
	mode = normalizeOfficialAccountMode(mode)
	if s != nil && s.cfg != nil {
		s.cfg.Bridge.OfficialAccountMode = mode
	}
	return mode
}

func (s *Server) officialChatInfo(ctx context.Context, chatID remoteapi.ChatID) (remoteapi.Chat, bool) {
	if s == nil || s.api == nil || chatID == "" {
		return remoteapi.Chat{ID: chatID, Name: string(chatID), IsGroup: strings.HasSuffix(string(chatID), "@chatroom")}, false
	}
	chat, err := s.api.GetChat(ctx, chatID)
	if err != nil {
		return remoteapi.Chat{ID: chatID, Name: string(chatID), IsGroup: strings.HasSuffix(string(chatID), "@chatroom")}, false
	}
	chat = normalizePortalChat(portalRecord{ChatID: chatID, IsGroup: strings.HasSuffix(string(chatID), "@chatroom")}, chat)
	return chat, isOfficialChat(chat)
}

func (s *Server) shouldRejectOfficialAccount(ctx context.Context, chatID remoteapi.ChatID) bool {
	_, official := s.officialChatInfo(ctx, chatID)
	return !officialAccountModeCreatesPortal(s.officialAccountMode(), official)
}

func (s *Server) handleOfficialsCommand(ctx context.Context, room id.RoomID, rest string) {
	fields := strings.Fields(strings.TrimSpace(rest))
	if len(fields) == 0 || fields[0] == "status" || fields[0] == "状态" {
		s.notice(ctx, room, fmt.Sprintf("当前公众号策略: %s\n可用: officials mode invite / mute / reject", s.officialAccountMode()))
		return
	}
	if fields[0] != "mode" && fields[0] != "模式" {
		s.notice(ctx, room, "用法: officials status 或 officials mode invite|mute|reject")
		return
	}
	if len(fields) < 2 {
		s.notice(ctx, room, "用法: officials mode invite|mute|reject")
		return
	}
	mode := normalizeOfficialAccountMode(fields[1])
	if mode != strings.ToLower(strings.TrimSpace(fields[1])) {
		s.notice(ctx, room, "公众号策略必须是 invite、mute 或 reject")
		return
	}
	s.setOfficialAccountMode(mode)
	s.notice(ctx, room, fmt.Sprintf("已切换公众号策略为 %s。注意: 这是运行时设置，如需重启后保留请同步修改 config.yaml 的 bridge.official_account_mode。", mode))
}
