package wechat

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

type solitaireMessageRef struct {
	MsgID    int32 `json:"MsgID,omitempty"`
	NewMsgID int64 `json:"NewMsgID,omitempty"`
}

var _ remoteapi.SolitaireAPI = (*Client)(nil)

// newSolitaireSource retains both exact integer IDs inside an opaque string so
// Matrix's generic JSON maps cannot round an int64 through a float64.
func newSolitaireSource(account string, chat remoteapi.ChatID, msgID, newMsgID int64) *remoteapi.SolitaireSource {
	ref := solitaireMessageRef{NewMsgID: newMsgID}
	if msgID >= -1<<31 && msgID <= 1<<31-1 {
		ref.MsgID = int32(msgID)
	}
	if account == "" || chat == "" || (ref.MsgID == 0 && ref.NewMsgID == 0) {
		return nil
	}
	raw, _ := json.Marshal(ref) // Fixed integer-only structure cannot fail.
	return &remoteapi.SolitaireSource{Account: remoteapi.UserID(account), Chat: chat, Reference: string(raw)}
}

// attachSolitaireSource examines the original XML, not a callback's rendered
// ContentText. Recognition does not replace the protocol server's validation
// of the saved message, participant ranges, or the selected version.
func (c *Client) attachSolitaireSource(base *remoteapi.InboundMessage, payload string, msgID, newMsgID int64) {
	payload = strings.TrimSpace(payload)
	if strings.HasSuffix(string(base.Chat), "@chatroom") && !strings.HasPrefix(payload, "<") {
		if sender, rest, found := strings.Cut(payload, "\n"); found {
			prefix := strings.TrimSuffix(sender, "\r")
			if strings.TrimSuffix(prefix, ":") == string(base.Sender) && strings.HasSuffix(prefix, ":") {
				payload = strings.TrimSpace(rest)
			}
		}
	}
	title, ok := solitaireTitleFromXML(payload)
	if !ok {
		return
	}
	base.Solitaire = newSolitaireSource(c.selfWxid(), base.Chat, msgID, newMsgID)
	if base.Solitaire != nil {
		base.DeduplicationKeys = solitaireMessageKeys(msgID, newMsgID)
	}
	// Push, local history and the send response must preserve the same title,
	// including literal entity text inside CDATA. Decode XML exactly once.
	base.Text = title
}

// Keep namespaces distinct: a legacy ID may numerically equal another
// message's new ID. Never use floating point when forming identity keys.
func solitaireMessageKeys(msgID, newMsgID int64) []string {
	keys := make([]string, 0, 2)
	if newMsgID != 0 {
		keys = append(keys, fmt.Sprintf("new:%d", newMsgID))
	}
	if msgID != 0 {
		keys = append(keys, fmt.Sprintf("msg:%d", msgID))
	}
	return keys
}

// JoinSolitaire deliberately bypasses SendMessage's retry loop. The protocol
// server owns its one explicit-login-expiry recovery; a second bridge retry
// could append the same entry twice. Redirects must not replay this POST either.
func (c *Client) JoinSolitaire(ctx context.Context, source remoteapi.SolitaireSource, content string) (remoteapi.SolitaireResult, error) {
	c.mu.RLock()
	account := c.wxid
	c.mu.RUnlock()
	if account == "" || source.Account != remoteapi.UserID(account) || strings.TrimSpace(string(source.Chat)) == "" {
		return remoteapi.SolitaireResult{}, fmt.Errorf("接龙原消息不属于当前登录账号或缺少会话标识")
	}
	if strings.TrimSpace(content) == "" || len(content) > 64<<10 {
		return remoteapi.SolitaireResult{}, fmt.Errorf("接龙记录不能为空，且不能超过 64 KiB")
	}
	if len(source.Reference) > 256 {
		return remoteapi.SolitaireResult{}, fmt.Errorf("接龙原消息引用无效")
	}
	var ref solitaireMessageRef
	decoder := json.NewDecoder(strings.NewReader(source.Reference))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ref); err != nil {
		return remoteapi.SolitaireResult{}, fmt.Errorf("接龙原消息引用无效: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF || (ref.MsgID == 0 && ref.NewMsgID == 0) {
		return remoteapi.SolitaireResult{}, fmt.Errorf("接龙原消息缺少准确的微信消息 ID")
	}
	request := struct {
		Wxid    string `json:"Wxid"`
		ToWxid  string `json:"ToWxid"`
		Content string `json:"Content"`
		solitaireMessageRef
	}{Wxid: account, ToWxid: string(source.Chat), Content: content, solitaireMessageRef: ref}

	transport := *c.t
	client := *c.t.hc
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	transport.hc = &client
	raw, _, err := transport.postRaw(ctx, "/Msg/SendSolitaire", request, nil)
	if err != nil {
		return remoteapi.SolitaireResult{}, fmt.Errorf("接龙发送结果未确认，请先检查微信，不要直接重试: %w", err)
	}
	var response struct {
		Code    *int            `json:"Code"`
		Success bool            `json:"Success"`
		Message string          `json:"Message"`
		Data    json.RawMessage `json:"Data"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return remoteapi.SolitaireResult{}, fmt.Errorf("接龙响应无效，结果未知，请勿直接重试: %w", err)
	}
	if response.Code == nil {
		return remoteapi.SolitaireResult{}, fmt.Errorf("接龙响应缺少 Code，结果未知，请勿直接重试")
	}
	if !response.Success || *response.Code != 0 {
		return remoteapi.SolitaireResult{}, &APIError{Path: "/Msg/SendSolitaire", Code: *response.Code, Message: response.Message}
	}
	var data struct {
		XML        json.RawMessage `json:"Xml"`
		SendResult struct {
			sendResp
			BaseResponse *struct {
				Ret *int `json:"ret"`
			} `json:"BaseResponse"`
		} `json:"SendResult"`
	}
	if err := json.Unmarshal(response.Data, &data); err != nil {
		return remoteapi.SolitaireResult{}, fmt.Errorf("接龙发送响应无效，结果未知，请勿直接重试: %w", err)
	}
	result := data.SendResult
	if result.BaseResponse == nil || result.BaseResponse.Ret == nil || *result.BaseResponse.Ret != 0 || (result.MsgId == 0 && result.NewMsgId == 0) {
		return remoteapi.SolitaireResult{}, fmt.Errorf("接龙响应不能确认发送成功，请先检查微信，不要直接重试")
	}
	confirmed := remoteapi.SolitaireResult{ID: msgID(result.sendResp)}
	var payload string
	if json.Unmarshal(data.XML, &payload) == nil {
		confirmed.Message = sentSolitaireMessage(account, source.Chat, payload, result.sendResp)
	}
	return confirmed, nil
}

// Read only the direct, unambiguous plain-text title of a valid type-53 app.
// Do not accidentally use a quoted/namespaced title or discard nested markup.
func solitaireTitleFromXML(payload string) (string, bool) {
	appType, err := appMessageTypeFromXML(payload)
	if err != nil || appType != 53 {
		return "", false
	}
	type title struct {
		XMLName  xml.Name
		Text     string     `xml:",chardata"`
		Children []struct{} `xml:",any"`
	}
	var document struct {
		XMLName xml.Name
		Titles  []title `xml:"title"`
		AppMsgs []struct {
			XMLName xml.Name
			Titles  []title `xml:"title"`
		} `xml:"appmsg"`
	}
	if err := xml.Unmarshal([]byte(payload), &document); err != nil {
		return "", false
	}
	titles := document.Titles
	if document.XMLName.Local == "msg" {
		titles = nil
		for _, appMsg := range document.AppMsgs {
			if appMsg.XMLName.Space == "" {
				titles = appMsg.Titles
				break
			}
		}
	}
	if len(titles) != 1 || titles[0].XMLName.Space != "" || len(titles[0].Children) != 0 || strings.TrimSpace(titles[0].Text) == "" {
		return "", false
	}
	return titles[0].Text, true
}

// Only use the returned, validated type-53 XML and newly confirmed IDs. Never
// append the entry to the old title locally: numbering and layout belong to
// the protocol service. Invalid/missing XML does not undo a confirmed send.
func sentSolitaireMessage(account string, chat remoteapi.ChatID, payload string, sent sendResp) *remoteapi.InboundMessage {
	title, ok := solitaireTitleFromXML(payload)
	source := newSolitaireSource(account, chat, sent.MsgId, sent.NewMsgId)
	if !ok || source == nil {
		return nil
	}
	timestamp := time.Now()
	if sent.CreateTime > 0 {
		timestamp = time.Unix(sent.CreateTime, 0)
	}
	return &remoteapi.InboundMessage{
		ID: msgID(sent), Chat: chat, Sender: remoteapi.UserID(account),
		Type: remoteapi.MsgApp, Text: title, Timestamp: timestamp,
		Extra:     map[string]any{"raw_msg_id": sent.MsgId, "msg_type": wxApp},
		Solitaire: source, DeduplicationKeys: solitaireMessageKeys(sent.MsgId, sent.NewMsgId),
	}
}
