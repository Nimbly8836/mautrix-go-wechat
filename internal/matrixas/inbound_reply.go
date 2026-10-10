package matrixas

import (
	"fmt"
	"strings"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"maunium.net/go/mautrix/event"
)

func (s *Server) attachInboundReply(account remoteapi.UserID, rec portalRecord, message *remoteapi.InboundMessage, content *event.MessageEventContent) {
	if message.Reply == nil {
		return
	}
	reply := message.Reply
	if reply.Target != "" {
		if target := s.getMessageReference(account, rec, reply.Target, reply.TargetKey); target.EventID != "" {
			content.RelatesTo = (&event.RelatesTo{}).SetReplyTo(target.EventID)
			return
		}
	}
	name := strings.TrimSpace(reply.SenderName)
	if name == "" {
		name = string(reply.Sender)
	}
	if name == "" {
		name = "原消息"
	}
	quote := reply.Text
	if quote == "" {
		quote = "[引用内容不可用]"
	}
	content.Body = fmt.Sprintf("引用 %s：\n> %s\n\n%s", name, strings.ReplaceAll(quote, "\n", "\n> "), content.Body)
}
