package wechat

import (
	"encoding/xml"
	"strconv"
	"strings"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

// XML text is decoded once. In particular, CDATA containing literal entities
// must not go through html.UnescapeString a second time.
type replyXMLText struct {
	XMLName  xml.Name
	Text     string     `xml:",chardata"`
	Children []struct{} `xml:",any"`
}

type replyXMLReference struct {
	XMLName      xml.Name
	Types        []replyXMLText `xml:"type"`
	IDs          []replyXMLText `xml:"svrid"`
	FromUsers    []replyXMLText `xml:"fromusr"`
	ChatUsers    []replyXMLText `xml:"chatusr"`
	DisplayNames []replyXMLText `xml:"displayname"`
	Contents     []replyXMLText `xml:"content"`
}

type replyXMLApp struct {
	XMLName xml.Name
	Titles  []replyXMLText      `xml:"title"`
	Refs    []replyXMLReference `xml:"refermsg"`
}

func replyText(fields []replyXMLText) (string, bool) {
	var selected *replyXMLText
	for i := range fields {
		if fields[i].XMLName.Space != "" {
			continue
		}
		if selected != nil || len(fields[i].Children) != 0 {
			return "", false
		}
		selected = &fields[i]
	}
	if selected == nil {
		return "", false
	}
	return selected.Text, true
}

func positiveMessageID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return ""
		}
	}
	n, err := strconv.ParseUint(value, 10, 64)
	if err != nil || n == 0 {
		return ""
	}
	return strconv.FormatUint(n, 10)
}

// This is only a content-selection probe. Sender authority is still resolved
// by convertInbound, and attachQuotedReply only strips its matching envelope.
func isQuotedReplyXML(raw string) bool {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "<") {
		if _, body, ok := strings.Cut(raw, ":"); ok {
			raw = strings.TrimSpace(body)
		}
	}
	kind, err := appMessageTypeFromXML(raw)
	return err == nil && kind == 57
}

func attachQuotedReply(message *remoteapi.InboundMessage, raw string) {
	raw = strings.TrimSpace(stripLastMessageOps(raw))
	if strings.HasSuffix(string(message.Chat), "@chatroom") && message.Sender != "" {
		raw = strings.TrimSpace(strings.TrimPrefix(raw, string(message.Sender)+":"))
	}
	kind, err := appMessageTypeFromXML(raw)
	if err != nil || kind != 57 {
		return
	}
	var document struct {
		XMLName xml.Name
		Apps    []replyXMLApp `xml:"appmsg"`
	}
	if xml.Unmarshal([]byte(raw), &document) != nil {
		return
	}
	var app *replyXMLApp
	if document.XMLName.Local == "appmsg" {
		app = &replyXMLApp{}
		if xml.Unmarshal([]byte(raw), app) != nil {
			return
		}
	} else {
		for i := range document.Apps {
			if document.Apps[i].XMLName.Space == "" {
				app = &document.Apps[i]
			}
		}
	}
	if app == nil {
		return
	}
	body, ok := replyText(app.Titles)
	if !ok {
		return
	}
	message.Type, message.Text = remoteapi.MsgApp, body
	var ref *replyXMLReference
	for i := range app.Refs {
		if app.Refs[i].XMLName.Space != "" {
			continue
		}
		if ref != nil {
			return
		}
		ref = &app.Refs[i]
	}
	if ref == nil {
		return
	}
	target, _ := replyText(ref.IDs)
	name, _ := replyText(ref.DisplayNames)
	text := quotedMessageText(ref)
	from, _ := replyText(ref.FromUsers)
	sender, _ := replyText(ref.ChatUsers)
	if sender == "" || strings.HasSuffix(sender, "@chatroom") {
		sender = from
	}
	if strings.HasSuffix(sender, "@chatroom") {
		sender = ""
	}
	target = positiveMessageID(target)
	message.Reply = &remoteapi.Reply{Target: remoteapi.MessageID(target), TargetKey: "new:" + target, Sender: remoteapi.UserID(sender), SenderName: name, Text: text}
}

func quotedMessageText(ref *replyXMLReference) string {
	kind, _ := replyText(ref.Types)
	switch strings.TrimSpace(kind) {
	case "1":
		text, _ := replyText(ref.Contents)
		return text
	case "3":
		return "[图片]"
	case "34":
		return "[语音]"
	case "43", "62":
		return "[视频]"
	case "47":
		return "[表情]"
	case "48":
		return "[位置]"
	case "42":
		return "[名片]"
	case "49":
		return "[应用消息]"
	default:
		return "[消息]"
	}
}
