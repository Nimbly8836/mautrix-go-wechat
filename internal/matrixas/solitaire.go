package matrixas

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"maunium.net/go/mautrix/event"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

const solitaireSourceKey = "com.leat.wechat.solitaire_source"

func solitaireMatrixContent(content *event.MessageEventContent, source *remoteapi.SolitaireSource, prefix string) any {
	if source == nil {
		return content
	}
	content.Body += fmt.Sprintf("\n\nReply: %s jl <text>", prefix)
	return &event.Content{Parsed: content, Raw: map[string]any{solitaireSourceKey: source}}
}

// Only explicitly prefixed commands opt in. A normal reply remains normal
// text, and thread fallback relations do not implicitly choose a target chain.
func parseSolitaireCommand(msg *event.MessageEventContent, prefix string) (string, bool) {
	body := msg.Body
	if msg.RelatesTo.GetReplyTo() != "" {
		body = event.TrimReplyFallbackText(body)
	}
	nextWord := func(text string) (string, string) {
		text = strings.TrimSpace(text)
		if index := strings.IndexFunc(text, unicode.IsSpace); index >= 0 {
			return text[:index], strings.TrimSpace(text[index:])
		}
		return text, ""
	}
	first, rest := nextWord(body)
	if !strings.EqualFold(first, prefix) {
		return "", false
	}
	command, content := nextWord(rest)
	return content, strings.EqualFold(command, "jl") || command == "接龙" || strings.EqualFold(command, "solitaire")
}

func (s *Server) handleSolitaireReply(ctx context.Context, rec portalRecord, evt *event.Event, content string) {
	if !s.allowed(evt.Sender) {
		s.sendPortalNotice(ctx, rec.RoomID, "你没有使用此 bridge 的权限。")
		return
	}
	msg := evt.Content.AsMessage()
	target := msg.RelatesTo.GetNonFallbackReplyTo()
	if msg.MsgType != event.MsgText || evt.StateKey != nil || msg.RelatesTo.GetReplaceID() != "" || target == "" || content == "" || evt.ID == "" {
		s.sendPortalNotice(ctx, rec.RoomID, fmt.Sprintf("请直接回复接龙消息：%s jl 内容（不支持编辑或线程默认回复）。", s.commandPrefix()))
		return
	}

	// Account switching uses this same lock. Keep source verification and the
	// send bound to one login, rather than observing two different accounts.
	s.accountTransitionMu.Lock()
	defer s.accountTransitionMu.Unlock()
	api, ok := s.api.(remoteapi.SolitaireAPI)
	if !ok {
		s.sendPortalNotice(ctx, rec.RoomID, "当前后端不支持参与接龙。")
		return
	}
	operationCtx, cancel := s.matrixOperationContext(ctx)
	original, err := s.as.BotIntent().Client.GetEvent(operationCtx, rec.RoomID, target)
	cancel()
	if err != nil {
		s.sendPortalNotice(ctx, rec.RoomID, fmt.Sprintf("无法读取你回复的原消息，未发送接龙: %v", err))
		return
	}
	if original == nil || original.ID != target || (original.RoomID != "" && original.RoomID != rec.RoomID) || original.Type != event.EventMessage || original.StateKey != nil || original.Unsigned.RedactedBecause != nil || (original.Sender != s.as.BotMXID() && !s.isBridgeUser(original.Sender)) {
		s.sendPortalNotice(ctx, rec.RoomID, "只能回复本房间由桥接发送、未撤回的接龙消息。未发送接龙。")
		return
	}
	var source remoteapi.SolitaireSource
	raw, err := json.Marshal(original.Content.Raw[solitaireSourceKey])
	if err == nil {
		err = json.Unmarshal(raw, &source)
	}
	if err != nil || source.Account == "" || source.Chat == "" || source.Reference == "" {
		s.sendPortalNotice(ctx, rec.RoomID, "这条消息没有可核验的接龙标识，可能不是接龙或是在升级前桥接的旧消息。请回复升级后带参与提示的接龙消息。未发送接龙。")
		return
	}
	account, err := s.api.Self(ctx)
	if err != nil || account == "" || source.Account != account || source.Chat != rec.ChatID {
		s.sendPortalNotice(ctx, rec.RoomID, "原接龙不属于当前登录账号或当前微信会话。未发送接龙。")
		return
	}
	if s.portals == nil {
		s.sendPortalNotice(ctx, rec.RoomID, "无法保存接龙防重复记录，未发送接龙。")
		return
	}
	claimed, err := claimSolitaireCommand(s.portals.path, rec.RoomID, evt.ID)
	if err != nil {
		s.sendPortalNotice(ctx, rec.RoomID, fmt.Sprintf("无法保存接龙防重复记录，未发送接龙: %v", err))
		return
	}
	if !claimed {
		s.sendPortalNotice(ctx, rec.RoomID, "这条接龙命令已尝试处理，不会重复发送。请先查看微信确认结果；已尝试不代表一定发送成功。")
		return
	}
	result, err := api.JoinSolitaire(ctx, source, content)
	if err == nil && (result.ID == "" || result.ID == "0") {
		err = fmt.Errorf("协议服务未返回消息 ID")
	}
	if err != nil {
		s.sendPortalNotice(ctx, rec.RoomID, fmt.Sprintf("接龙未确认成功: %v\n没有自动重发。若结果未知，请先查看微信，不要直接重试。", err))
		return
	}
	if result.Message == nil || result.Message.ID != result.ID {
		s.sendPortalNotice(ctx, rec.RoomID, fmt.Sprintf("JL 已发送（ID: %s），但未取得完整接龙；请查看微信，勿重发。", result.ID))
		return
	}
	if err := s.deliverSolitaireToPortal(ctx, rec, result.Message); err != nil {
		s.sendPortalNotice(ctx, rec.RoomID, fmt.Sprintf("JL 已发送，但 Matrix 展示未确认；请查看微信，勿重发。详情: %v", err))
	}
}
