package wechat

import (
	"context"
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

type sendResultContextKey struct{}

// Capture aliases directly from this request's decoded response, not a shared
// last-send cache (which could race across rooms or accounts).
func (c *Client) SendMessageWithResult(ctx context.Context, chat remoteapi.ChatID, msg remoteapi.OutgoingMessage) (remoteapi.MessageSendResult, error) {
	result := remoteapi.MessageSendResult{}
	ctx = context.WithValue(ctx, sendResultContextKey{}, &result)
	// Preserve the ordinary send policy (retry only explicit logged-out API
	// rejections). Native replies still make one attempt via SendMessage.
	id, err := c.SendMessage(ctx, chat, msg)
	if err != nil {
		return remoteapi.MessageSendResult{}, err
	}
	result.ID = id
	result.Quote = sentMessageQuote(msg)
	return result, nil
}

func attachAppQuotePreview(message *remoteapi.InboundMessage, raw string) {
	raw = strings.TrimSpace(stripLastMessageOps(raw))
	if strings.HasSuffix(string(message.Chat), "@chatroom") && message.Sender != "" {
		raw = strings.TrimSpace(strings.TrimPrefix(raw, string(message.Sender)+":"))
	}
	message.Quote = sentMessageQuote(remoteapi.OutgoingMessage{Type: remoteapi.MsgApp, Text: raw})
}

func sentMessageQuote(msg remoteapi.OutgoingMessage) *remoteapi.SentMessageQuote {
	quote := &remoteapi.SentMessageQuote{Type: msg.Type, Text: msg.Text, Body: msg.Text}
	appType, title := 57, msg.Text
	if msg.Reply == nil {
		if msg.Type != remoteapi.MsgApp {
			return quote
		}
		kind, err := appMessageTypeFromXML(msg.Text)
		if err != nil {
			return nil
		}
		appType = kind
		var doc struct {
			XMLName xml.Name
			Apps    []replyXMLApp `xml:"appmsg"`
		}
		if xml.Unmarshal([]byte(msg.Text), &doc) != nil {
			return nil
		}
		var app replyXMLApp
		if doc.XMLName.Local == "appmsg" {
			if xml.Unmarshal([]byte(msg.Text), &app) != nil {
				return nil
			}
		} else {
			for _, candidate := range doc.Apps {
				if candidate.XMLName.Space == "" {
					app = candidate
				}
			}
		}
		title, _ = replyText(app.Titles)
	}
	// A compact, valid app preview avoids retaining attachment credentials,
	// Markdown fences or an exponentially nested reply chain. Its typed server
	// ID still points to the complete original message in WeChat.
	characters := []rune(title)
	if len(characters) > 512 {
		title = string(characters[:512]) + "…"
	}
	var escaped strings.Builder
	_ = xml.EscapeText(&escaped, []byte(title))
	quote.Type = remoteapi.MsgApp
	quote.Body = title
	quote.Text = fmt.Sprintf("<msg><appmsg><title>%s</title><type>%d</type></appmsg></msg>", escaped.String(), appType)
	return quote
}

func qualifiedSendID(ctx context.Context, response sendResp) remoteapi.MessageID {
	if result, ok := ctx.Value(sendResultContextKey{}).(*remoteapi.MessageSendResult); ok {
		if response.NewMsgId > 0 {
			result.Keys = append(result.Keys, fmt.Sprintf("new:%d", response.NewMsgId))
		}
		if response.MsgId > 0 {
			result.Keys = append(result.Keys, fmt.Sprintf("msg:%d", response.MsgId))
		}
		if id := msgID(response); id != "" && id != "0" {
			result.Keys = append(result.Keys, "id:"+string(id))
		}
	}
	return msgID(response)
}

func buildNativeReplyXML(chat remoteapi.ChatID, text string, reply *remoteapi.Reply) (string, error) {
	if reply == nil || reply.Sender == "" || reply.TargetKey != "new:"+string(reply.Target) {
		return "", fmt.Errorf("wechat: native reply requires a verified sender and qualified new ID")
	}
	number, err := strconv.ParseUint(string(reply.Target), 10, 64)
	if err != nil || number == 0 || strconv.FormatUint(number, 10) != string(reply.Target) {
		return "", fmt.Errorf("wechat: invalid native reply server ID")
	}
	// These are wire message kinds, not the ordinal neutral API enum.
	kinds := map[remoteapi.MessageType]int{remoteapi.MsgText: 1, remoteapi.MsgImage: 3, remoteapi.MsgAudio: 34, remoteapi.MsgVideo: 43, remoteapi.MsgFile: 49, remoteapi.MsgLocation: 48, remoteapi.MsgContactCard: 42, remoteapi.MsgLink: 49, remoteapi.MsgEmoji: 47, remoteapi.MsgApp: 49}
	kind, ok := kinds[reply.Type]
	if !ok {
		return "", fmt.Errorf("wechat: unsupported quoted message type")
	}
	escape := func(value string) string {
		var out strings.Builder
		_ = xml.EscapeText(&out, []byte(value))
		return out.String()
	}
	from, member := string(reply.Sender), ""
	if strings.HasSuffix(string(chat), "@chatroom") {
		from, member = string(chat), string(reply.Sender)
	}
	// Never interpolate raw XML, CDATA, attributes, or Matrix extension fields.
	return fmt.Sprintf("<msg><appmsg><title>%s</title><type>57</type><refermsg><type>%d</type><svrid>%s</svrid><fromusr>%s</fromusr><chatusr>%s</chatusr><displayname>%s</displayname><content>%s</content></refermsg></appmsg></msg>", escape(text), kind, escape(string(reply.Target)), escape(from), escape(member), escape(reply.SenderName), escape(reply.Text)), nil
}
