package wechat

import (
	"encoding/xml"
	"io"
	"strings"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

func revokeFallbackText(candidates ...string) string {
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		lower := strings.ToLower(candidate)
		if candidate == "" || strings.HasPrefix(candidate, "<") || strings.Contains(lower, "<sysmsg") || strings.Contains(lower, "<revokemsg") || strings.Contains(lower, "&lt;sysmsg") {
			continue
		}
		switch lower {
		case "[系统消息]", "[系统摘要]", "[消息]", "[撤回]", "[message]":
			continue
		}
		return candidate
	}
	return "收到一条撤回通知"
}

type revokeInfo struct {
	Chat, MsgID, OldMsgID, NewMsgID, Text string
	Valid, HasNewID                       bool
}

// A recall can delete a Matrix event, so require a complete, unambiguous
// sysmsg/revokemsg document. Never pick IDs out of nested or partial XML.
func parseRevokeMessage(raw string) revokeInfo {
	if len(raw) > maxAppXMLBytes {
		return revokeInfo{}
	}
	decoder := xml.NewDecoder(strings.NewReader(raw))
	depth, roots := 0, 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return revokeInfo{}
		}
		switch node := token.(type) {
		case xml.StartElement:
			if depth == 0 {
				roots++
				if roots != 1 || node.Name.Space != "" || node.Name.Local != "sysmsg" {
					return revokeInfo{}
				}
				types := 0
				for _, attr := range node.Attr {
					if attr.Name.Space == "" && attr.Name.Local == "type" {
						types++
						if attr.Value != "revokemsg" {
							return revokeInfo{}
						}
					}
				}
				if types != 1 {
					return revokeInfo{}
				}
			}
			depth++
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(string(node)) != "" {
				return revokeInfo{}
			}
		case xml.Directive:
			return revokeInfo{}
		}
	}
	if roots != 1 || depth != 0 {
		return revokeInfo{}
	}
	var document struct {
		Revokes []struct {
			XMLName xml.Name
			Fields  []replyXMLText `xml:",any"`
		} `xml:"revokemsg"`
	}
	if xml.Unmarshal([]byte(raw), &document) != nil {
		return revokeInfo{}
	}
	fields := map[string]string{}
	count := 0
	for _, revoke := range document.Revokes {
		if revoke.XMLName.Space != "" {
			continue
		}
		count++
		for _, field := range revoke.Fields {
			if field.XMLName.Space != "" {
				continue
			}
			switch field.XMLName.Local {
			case "session", "msgid", "oldmsgid", "newmsgid", "replacemsg":
				if _, duplicate := fields[field.XMLName.Local]; duplicate || len(field.Children) != 0 {
					return revokeInfo{}
				}
				fields[field.XMLName.Local] = strings.TrimSpace(field.Text)
			}
		}
	}
	if count != 1 {
		return revokeInfo{}
	}
	_, hasNewID := fields["newmsgid"]
	return revokeInfo{Chat: fields["session"], MsgID: fields["msgid"], OldMsgID: fields["oldmsgid"], NewMsgID: fields["newmsgid"], Text: fields["replacemsg"], Valid: true, HasNewID: hasNewID}
}

func (c *Client) convertRevoke(base remoteapi.InboundMessage, raw, rendered, push string) (remoteapi.Event, bool) {
	raw = strings.TrimSpace(stripLastMessageOps(raw))
	if strings.HasSuffix(string(base.Chat), "@chatroom") && base.Sender != "" {
		raw = strings.TrimSpace(strings.TrimPrefix(raw, string(base.Sender)+":"))
	}
	info := parseRevokeMessage(raw)
	if !info.Valid && !strings.Contains(raw, "revokemsg") && !strings.Contains(rendered, "撤回") {
		return remoteapi.Event{}, false
	}
	text := info.Text
	if text == "" {
		text = revokeFallbackText(rendered, push)
	}
	target, targetKey := "", ""
	if info.Valid && (info.Chat == "" || info.Chat == string(base.Chat)) {
		if info.HasNewID {
			target = positiveMessageID(info.NewMsgID)
			targetKey = "new:" + target
		} else {
			target = positiveMessageID(firstNonEmpty(info.OldMsgID, info.MsgID))
			targetKey = "msg:" + target
		}
	}
	return remoteapi.Event{Kind: remoteapi.EventRedaction, Redaction: &remoteapi.Redaction{Account: base.Account, Chat: base.Chat, By: base.Sender, Target: remoteapi.MessageID(target), TargetKey: targetKey, Text: text, Timestamp: base.Timestamp}}, true
}
