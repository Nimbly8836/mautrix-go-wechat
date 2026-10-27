package wechat

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

const chatRoomInviteHost = "support.weixin.qq.com"
const chatRoomInvitePath = "/cgi-bin/mmsupport-bin/addchatroombyinvite"

var chatRoomInviteDetails = regexp.MustCompile(`"([^"]+)"邀请你加入群聊"([^"]+)"`)

func isChatRoomInviteURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, chatRoomInviteHost) || u.Path != chatRoomInvitePath || u.User != nil || u.RawQuery == "" || u.Fragment != "" {
		return false
	}
	query := u.Query()
	ticketValues, ok := query["ticket"]
	return ok && len(query) == 1 && len(ticketValues) == 1 && ticketValues[0] != "" && len(ticketValues[0]) <= 4096
}

func chatRoomIDFromInviteResult(data any) string {
	switch value := data.(type) {
	case string:
		return normalizeChatRoomJump(value)
	case map[string]any:
		for _, key := range []string{"QID", "qid", "ChatRoomName", "chat_room_name", "chatroom"} {
			if raw, ok := value[key]; ok {
				if id := normalizeChatRoomJump(fmt.Sprint(raw)); id != "" {
					return id
				}
			}
		}
	}
	return ""
}

func normalizeChatRoomJump(raw string) string {
	raw = strings.TrimSpace(raw)
	for _, prefix := range []string{"weixin://jump/mainframe/", "weixin://jump/mainframe"} {
		if strings.HasPrefix(raw, prefix) {
			raw = strings.TrimPrefix(raw, prefix)
			break
		}
	}
	if raw == "" || strings.ContainsAny(raw, "\r\n/?#") || !strings.HasSuffix(raw, "@chatroom") {
		return ""
	}
	return raw
}

func (c *Client) AcceptChatRoomInvite(ctx context.Context, inviteURL string) (remoteapi.ChatID, error) {
	inviteURL = strings.TrimSpace(inviteURL)
	if !isChatRoomInviteURL(inviteURL) {
		return "", fmt.Errorf("invalid WeChat group invitation URL")
	}
	var data any
	if err := c.t.post(ctx, "/Group/ConsentToJoin", map[string]any{"Wxid": c.selfWxid(), "Url": inviteURL}, nil, &data); err != nil {
		return "", err
	}
	chatID := chatRoomIDFromInviteResult(data)
	if chatID == "" {
		return "", fmt.Errorf("WeChat group invitation accepted without a group ID")
	}
	return remoteapi.ChatID(chatID), nil
}

func parseChatRoomInvite(content ...string) (*remoteapi.ChatRoomInvite, bool) {
	joined := strings.Join(content, "\n")
	for _, candidate := range extractURLs(joined) {
		if !isChatRoomInviteURL(candidate) {
			continue
		}
		inviter, chatName := inviteDisplayDetails(joined)
		return &remoteapi.ChatRoomInvite{URL: candidate, Inviter: inviter, ChatName: chatName}, true
	}
	return nil, false
}

func extractURLs(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, token := range strings.FieldsFunc(text, func(r rune) bool {
		return r == ' ' || r == '\n' || r == '\r' || r == '\t' || r == '"' || r == '\'' || r == '<' || r == '>'
	}) {
		candidate := strings.TrimRight(token, "，。；,.;)")
		if strings.HasPrefix(candidate, "https://") && !seen[candidate] {
			seen[candidate] = true
			out = append(out, candidate)
		}
	}
	return out
}

func inviteDisplayDetails(text string) (string, string) {
	if match := chatRoomInviteDetails.FindStringSubmatch(text); len(match) == 3 {
		return strings.TrimSpace(match[1]), strings.TrimSpace(match[2])
	}
	return inviteBetween(text, "邀请你加入群聊", "邀请你加入"), ""
}

func inviteBetween(text string, markers ...string) string {
	for _, marker := range markers {
		if idx := strings.Index(text, marker); idx > 0 {
			prefix := strings.TrimSpace(text[:idx])
			if cut := strings.LastIndexAny(prefix, "\n\r"); cut >= 0 {
				prefix = strings.TrimSpace(prefix[cut+1:])
			}
			prefix = strings.Trim(prefix, "[]（）() ")
			if prefix != "" {
				return prefix
			}
		}
	}
	return ""
}
