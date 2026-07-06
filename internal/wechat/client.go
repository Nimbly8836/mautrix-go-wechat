package wechat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

// Factory registers the WeChat protocol with the remoteapi registry.
type Factory struct{}

func (Factory) Name() string { return "wechat" }

func (Factory) New(cfg remoteapi.Config) (remoteapi.PuppetAPI, error) {
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("wechat: BaseURL is required")
	}
	mode := cfg.ReceiveMode
	if mode == "" {
		mode = "poll"
	}
	emojiDir := "/data/emojis"
	if cfg.Extra != nil {
		if v, ok := cfg.Extra["emoji_dir"].(string); ok && strings.TrimSpace(v) != "" {
			emojiDir = strings.TrimSpace(v)
		}
	}
	c := &Client{
		t:           newTransport(cfg.BaseURL),
		events:      make(chan remoteapi.Event, 64),
		done:        make(chan struct{}),
		receiveMode: mode,
		emojiDir:    emojiDir,
		localSeen:   map[string]struct{}{},
	}
	if len(cfg.Session) > 0 {
		var s sessionState
		if err := json.Unmarshal(cfg.Session, &s); err == nil {
			c.wxid = s.Wxid
			c.syncKey = s.SyncKey
		}
	}
	return c, nil
}

// init wires WeChat into the global registry.
func init() { remoteapi.Register(Factory{}) }

// sessionState is the persisted, resumable login data.
type sessionState struct {
	Wxid    string `json:"wxid"`
	SyncKey string `json:"sync_key"`
}

// Client implements remoteapi.PuppetAPI for WeChat.
type Client struct {
	t      *transport
	events chan remoteapi.Event

	mu      sync.RWMutex
	wxid    string
	syncKey string

	receiveMode string
	emojiDir    string
	localCursor int64
	localSeen   map[string]struct{}

	done     chan struct{}
	stopOnce sync.Once
}

// compile-time interface checks
var (
	_ remoteapi.PuppetAPI            = (*Client)(nil)
	_ remoteapi.WebhookReceiver      = (*Client)(nil)
	_ remoteapi.MediaUploader        = (*Client)(nil)
	_ remoteapi.Pinger               = (*Client)(nil)
	_ remoteapi.LoginInfoProvider    = (*Client)(nil)
	_ remoteapi.CachedLoginActivator = (*Client)(nil)
	_ remoteapi.LoginRefresher       = (*Client)(nil)
)

// ---------------------------------------------------------------------------
// lifecycle
// ---------------------------------------------------------------------------

func (c *Client) Connect(ctx context.Context) error {
	if c.receiveMode == "push" {
		c.emit(remoteapi.Event{Kind: remoteapi.EventConnected})
		return nil
	}
	loggedIn, err := c.IsLoggedIn(ctx)
	if err != nil {
		return err
	}
	if !loggedIn {
		return fmt.Errorf("wechat: not logged in; call Login first")
	}
	go c.syncLoop()
	c.emit(remoteapi.Event{Kind: remoteapi.EventConnected})
	return nil
}

func (c *Client) Disconnect() {
	c.stopOnce.Do(func() {
		close(c.done)
		close(c.events)
	})
}

func (c *Client) Events() <-chan remoteapi.Event { return c.events }

func (c *Client) HandleWebhook(ctx context.Context, payload []byte) error {
	var decoded []rawMessage
	resp, err := decodeWebhookMessages(payload)
	if err != nil {
		return err
	}
	if resp.SyncKey != "" {
		c.mu.Lock()
		c.syncKey = resp.SyncKey
		c.mu.Unlock()
	}
	decoded = append(decoded, resp.AddMsgs...)
	decoded = append(decoded, resp.Messages...)
	log.Printf("wechat webhook: bytes=%d add_msgs=%d messages=%d", len(payload), len(resp.AddMsgs), len(resp.Messages))
	if len(decoded) == 0 {
		log.Printf("wechat webhook: decoded zero messages")
	}
	seen := map[string]struct{}{}
	for _, m := range decoded {
		key := rawWebhookMessageKey(m)
		if key != "" {
			if _, ok := seen[key]; ok {
				log.Printf("wechat webhook: drop duplicate message key=%s", key)
				continue
			}
			seen[key] = struct{}{}
			if c.markSeenRawMessage(key) {
				log.Printf("wechat webhook: drop already seen message key=%s", key)
				continue
			}
		}
		if ev, ok := c.convertInbound(ctx, m); ok {
			if ev.Message != nil {
				log.Printf("wechat webhook: emit chat=%s sender=%s type=%d text=%q", ev.Message.Chat, ev.Message.Sender, ev.Message.Type, webhookPreview(ev.Message.Text))
			}
			c.emit(ev)
		}
	}
	_ = ctx
	return nil
}

func decodeWebhookMessages(payload []byte) (syncResponse, error) {
	var env Envelope
	if err := json.Unmarshal(payload, &env); err == nil && (len(env.Data) > 0 || env.Code != 0 || env.Message != "") {
		if !env.Success && env.Code != 0 {
			if isTransientLoggedOutCode(env.Code, env.Message) {
				log.Printf("wechat webhook: ignoring transient logged-out envelope code=%d message=%q", env.Code, env.Message)
				return syncResponse{}, nil
			}
			return syncResponse{}, &APIError{Code: env.Code, Message: env.Message, Path: "webhook"}
		}
		if len(env.Data) == 0 {
			return syncResponse{}, nil
		}
		resp, err := decodeWebhookMessageSet(env.Data)
		if err != nil {
			return syncResponse{}, fmt.Errorf("decode webhook data: %w", err)
		}
		return resp, nil
	}
	return decodeWebhookMessageSet(payload)
}

func isTransientLoggedOutError(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return isTransientLoggedOutCode(apiErr.Code, apiErr.Message)
}

func isTransientLoggedOutCode(code int, message string) bool {
	return code == -13 || strings.Contains(message, "用户可能退出")
}

func decodeWebhookMessageSet(payload []byte) (syncResponse, error) {
	var resp syncResponse
	if err := json.Unmarshal(payload, &resp); err == nil && (len(resp.AddMsgs) > 0 || len(resp.Messages) > 0 || resp.SyncKey != "") {
		return resp, nil
	}
	var list []rawMessage
	if err := json.Unmarshal(payload, &list); err == nil {
		resp.Messages = list
		return resp, nil
	}
	var single rawMessage
	if err := json.Unmarshal(payload, &single); err == nil && (single.MsgId != 0 || single.MsgID != 0 || single.Talker != "" || single.ContentText != "" || single.Content.Str != "") {
		resp.Messages = []rawMessage{single}
		return resp, nil
	}
	if err := json.Unmarshal(payload, &resp); err != nil {
		return syncResponse{}, fmt.Errorf("decode webhook payload: %w", err)
	}
	return resp, nil
}

func localMessageKey(m localMessage) string {
	if m.NewMsgID != 0 {
		return fmt.Sprintf("new:%d", m.NewMsgID)
	}
	if m.MsgID != 0 {
		return fmt.Sprintf("msg:%d", m.MsgID)
	}
	if m.Talker != "" && m.ContentText != "" && m.CreateTime != 0 {
		return fmt.Sprintf("fallback:%s:%d:%s", m.Talker, m.CreateTime, m.ContentText)
	}
	return fmt.Sprintf("local:%d:%s", m.CreateTime, firstNonEmpty(m.ContentText, m.Content, m.PushContent))
}

func (c *Client) markSeenRawMessage(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.localSeen == nil {
		c.localSeen = map[string]struct{}{}
	}
	if _, ok := c.localSeen[key]; ok {
		return true
	}
	c.localSeen[key] = struct{}{}
	if len(c.localSeen) > 2000 {
		c.localSeen = map[string]struct{}{key: struct{}{}}
	}
	return false
}

func rawWebhookMessageKey(m rawMessage) string {
	if m.NewMsgId != 0 {
		return fmt.Sprintf("new:%d", m.NewMsgId)
	}
	if m.NewMsgID != 0 {
		return fmt.Sprintf("new:%d", m.NewMsgID)
	}
	if m.MsgId != 0 {
		return fmt.Sprintf("msg:%d", m.MsgId)
	}
	if m.MsgID != 0 {
		return fmt.Sprintf("msg:%d", m.MsgID)
	}
	text := firstNonEmpty(m.ContentText, m.Content.Str, m.PushContent)
	chat := firstNonEmpty(m.Talker, m.FromUserName.Str)
	if chat != "" && text != "" && m.CreateTime != 0 {
		return fmt.Sprintf("fallback:%s:%d:%s", chat, m.CreateTime, text)
	}
	return ""
}

func webhookPreview(s string) string {
	s = strings.TrimSpace(s)
	if len([]rune(s)) <= 160 {
		return s
	}
	r := []rune(s)
	return string(r[:160]) + "…"
}

func (c *Client) emit(e remoteapi.Event) {
	select {
	case c.events <- e:
	case <-c.done:
	}
}

// syncLoop long-polls /Msg/Sync and pushes inbound events. This is the
// polling adapter that hides WeChat's pull model behind the event stream.
func (c *Client) syncLoop() {
	backoff := time.Second
	for {
		select {
		case <-c.done:
			return
		default:
		}

		ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
		msgs, err := c.queryLocal(ctx)

		if err != nil {
			cancel()
			c.emit(remoteapi.Event{Kind: remoteapi.EventDisconnected, Error: err})
			select {
			case <-time.After(backoff):
			case <-c.done:
				return
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second

		for _, m := range msgs {
			if ev, ok := c.convertLocal(ctx, m); ok {
				c.emit(ev)
			}
		}
		cancel()
		select {
		case <-time.After(3 * time.Second):
		case <-c.done:
			return
		}
	}
}

func (c *Client) queryLocal(ctx context.Context) ([]localMessage, error) {
	c.mu.RLock()
	wxid := c.wxid
	cursor := c.localCursor
	c.mu.RUnlock()

	req := queryLocalReq{Wxid: wxid, Limit: 50, Offset: 0, Desc: false}
	if cursor > 0 {
		req.StartCreateTime = cursor
	} else {
		// First run: grab a small recent window so existing active chats can create
		// portal rooms, but don't backfill the entire 100k+ local DB.
		req.Limit = 30
		req.Desc = true
	}

	var resp queryLocalResp
	if err := c.t.post(ctx, "/Msg/QueryLocal", req, nil, &resp); err != nil {
		return nil, err
	}
	msgs := resp.List
	if req.Desc {
		for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
			msgs[i], msgs[j] = msgs[j], msgs[i]
		}
	}

	out := make([]localMessage, 0, len(msgs))
	var maxTime int64 = cursor
	c.mu.Lock()
	for _, m := range msgs {
		if m.CreateTime > maxTime {
			maxTime = m.CreateTime
		}
		key := localMessageKey(m)
		if _, ok := c.localSeen[key]; ok {
			continue
		}
		c.localSeen[key] = struct{}{}
		if len(c.localSeen) > 2000 {
			c.localSeen = map[string]struct{}{key: struct{}{}}
		}
		if shouldDropLocal(m) {
			continue
		}
		out = append(out, m)
	}
	if maxTime > c.localCursor {
		c.localCursor = maxTime
	}
	c.mu.Unlock()
	return out, nil
}

func (c *Client) sync(ctx context.Context) ([]rawMessage, error) {
	c.mu.RLock()
	req := syncReq{Wxid: c.wxid, Scene: 0, Synckey: c.syncKey}
	c.mu.RUnlock()

	var resp syncResponse
	if err := c.t.post(ctx, "/Msg/Sync", req, nil, &resp); err != nil {
		return nil, err
	}
	if resp.SyncKey != "" {
		c.mu.Lock()
		c.syncKey = resp.SyncKey
		c.mu.Unlock()
	}
	if len(resp.Messages) > 0 {
		return append(resp.AddMsgs, resp.Messages...), nil
	}
	return resp.AddMsgs, nil
}

// ---------------------------------------------------------------------------
// auth
// ---------------------------------------------------------------------------

func (c *Client) IsLoggedIn(ctx context.Context) (bool, error) {
	c.mu.RLock()
	wxid := c.wxid
	c.mu.RUnlock()
	if wxid == "" {
		return false, nil
	}
	// HeartBeat doubles as a session validity probe.
	err := c.t.post(ctx, "/Login/HeartBeat", nil, q("wxid", wxid), nil)
	if err != nil {
		if _, ok := err.(*APIError); ok {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (c *Client) Self(ctx context.Context) (remoteapi.UserID, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.wxid == "" {
		return "", fmt.Errorf("wechat: no logged-in account")
	}
	return remoteapi.UserID(c.wxid), nil
}

func (c *Client) Logout(ctx context.Context) error {
	c.mu.RLock()
	wxid := c.wxid
	c.mu.RUnlock()
	return c.t.post(ctx, "/Login/LogOut", nil, q("wxid", wxid), nil)
}

func (c *Client) DumpSession() ([]byte, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return json.Marshal(sessionState{Wxid: c.wxid, SyncKey: c.syncKey})
}

func (c *Client) Ping(ctx context.Context) error {
	c.mu.RLock()
	wxid := c.wxid
	c.mu.RUnlock()
	return c.t.post(ctx, "/Login/HeartBeat", nil, q("wxid", wxid), nil)
}

// GetLoginInfo fetches server-side login/cache info for a wxid using
// /Login/GetCacheInfo?wxid=... . The API response is undocumented in
// swagger.json, so fields are returned dynamically.
func (c *Client) GetLoginInfo(ctx context.Context, id remoteapi.UserID) (remoteapi.LoginInfo, error) {
	var raw any
	if err := c.t.post(ctx, "/Login/GetCacheInfo", nil, q("wxid", string(id)), &raw); err != nil {
		return remoteapi.LoginInfo{}, err
	}
	return remoteapi.LoginInfo{ID: id, Fields: normalizeInfoFields(raw)}, nil
}

// ActivateCachedLogin makes this client use the given wxid's cached login state
// from the API service. After this succeeds, IsLoggedIn/Connect/Sync operate as
// if the account had just completed interactive QR login.
func (c *Client) ActivateCachedLogin(ctx context.Context, id remoteapi.UserID) (remoteapi.LoginInfo, error) {
	info, err := c.GetLoginInfo(ctx, id)
	if err != nil {
		return remoteapi.LoginInfo{}, err
	}
	wxid := stringValue(info.Fields["Wxid"])
	if wxid == "" {
		wxid = string(id)
	}
	if wxid == "" {
		return remoteapi.LoginInfo{}, fmt.Errorf("wechat: cached login info has no wxid")
	}
	c.mu.Lock()
	c.wxid = wxid
	c.syncKey = stringValue(info.Fields["SyncKey"])
	c.mu.Unlock()
	if c.syncKey == "" {
		// Best effort: initialize sync state if cache didn't include SyncKey.
		_ = c.t.post(ctx, "/Login/Newinit", nil, q("wxid", wxid), nil)
	}
	return info, nil
}

func stringValue(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func normalizeInfoFields(raw any) map[string]any {
	fields := map[string]any{}
	switch v := raw.(type) {
	case map[string]any:
		for k, val := range v {
			fields[k] = val
		}
	case nil:
	default:
		fields["raw"] = v
	}
	return fields
}

// ---------------------------------------------------------------------------
// outbound messaging
// ---------------------------------------------------------------------------

var transientSendRetryDelays = []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second, 20 * time.Second, 30 * time.Second, 60 * time.Second}

func (c *Client) SendMessage(ctx context.Context, chat remoteapi.ChatID, msg remoteapi.OutgoingMessage) (remoteapi.MessageID, error) {
	msgID, err := c.sendMessageOnce(ctx, chat, msg)
	if !isTransientLoggedOutError(err) {
		return msgID, err
	}
	lastErr := err
	for attempt, delay := range transientSendRetryDelays {
		log.Printf("wechat send: transient logged-out error, waiting for upstream auto relogin before retry attempt=%d/%d chat=%s delay=%s err=%v", attempt+1, len(transientSendRetryDelays), chat, delay, lastErr)
		if err := sleepContext(ctx, delay); err != nil {
			return "", fmt.Errorf("%w (stopped waiting for upstream auto relogin: %v)", lastErr, err)
		}
		msgID, err = c.sendMessageOnce(ctx, chat, msg)
		if !isTransientLoggedOutError(err) {
			return msgID, err
		}
		lastErr = err
	}
	return "", lastErr
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (c *Client) sendMessageOnce(ctx context.Context, chat remoteapi.ChatID, msg remoteapi.OutgoingMessage) (remoteapi.MessageID, error) {
	c.mu.RLock()
	wxid := c.wxid
	c.mu.RUnlock()
	to := string(chat)

	switch msg.Type {
	case remoteapi.MsgText:
		req := sendTxtReq{
			Wxid: wxid, ToWxid: to, Content: msg.Text, Type: 1,
			At: strings.Join(userIDs(msg.Mentions), ","),
		}
		var r sendResp
		if err := c.t.post(ctx, "/Msg/SendTxt", req, nil, &r); err != nil {
			return "", err
		}
		return msgID(r), nil

	case remoteapi.MsgImage:
		if msg.Media == nil {
			return "", fmt.Errorf("wechat: image message without media")
		}
		req := uploadImgReq{Wxid: wxid, ToWxid: to, Base64: base64.StdEncoding.EncodeToString(msg.Media.Data)}
		var r sendResp
		if err := c.t.post(ctx, "/Msg/UploadImg", req, nil, &r); err != nil {
			return "", err
		}
		return msgID(r), nil

	case remoteapi.MsgFile:
		if msg.Media == nil || len(msg.Media.Data) == 0 {
			return "", fmt.Errorf("wechat: file message without media")
		}
		fileName := strings.TrimSpace(msg.Media.FileName)
		if fileName == "" {
			fileName = strings.TrimSpace(msg.Text)
		}
		if fileName == "" {
			fileName = "file"
		}
		uploaded, err := c.uploadFileBinary(ctx, wxid, fileName, msg.Media.Data)
		if err != nil {
			return "", err
		}
		totalLen := uploaded.TotalLen
		if totalLen == 0 {
			totalLen = msg.Media.Size
		}
		if totalLen == 0 {
			totalLen = int64(len(msg.Media.Data))
		}
		req := sendAppReq{Wxid: wxid, ToWxid: to, Type: 6, Xml: buildFileAppXML(fileName, uploaded.MediaID, totalLen)}
		var r sendResp
		if err := c.t.post(ctx, "/Msg/SendApp", req, nil, &r); err != nil {
			return "", err
		}
		return msgID(r), nil

	case remoteapi.MsgEmoji:
		if msg.Media == nil || len(msg.Media.Data) == 0 {
			return "", fmt.Errorf("wechat: emoji message without media")
		}
		if !isGIF(msg.Media.Data) {
			return "", fmt.Errorf("wechat: emoji sending requires GIF media")
		}
		fileMD5 := contentMD5(msg.Media.Data)
		if err := c.writeOutgoingEmojiCache(fileMD5, msg.Media.Data); err != nil {
			return "", err
		}
		req := sendEmojiReq{
			Wxid:     wxid,
			ToWxid:   to,
			Md5:      fileMD5,
			Base64:   base64.StdEncoding.EncodeToString(msg.Media.Data),
			TotalLen: len(msg.Media.Data),
		}
		var r sendResp
		if err := c.t.post(ctx, "/Msg/SendEmoji", req, nil, &r); err != nil {
			return "", err
		}
		return msgID(r), nil

	default:
		// Fallback: send a textual placeholder for unhandled rich types so the
		// bridge degrades gracefully. Extend per-type as needed.
		req := sendTxtReq{Wxid: wxid, ToWxid: to, Content: msg.Text, Type: 1}
		var r sendResp
		if err := c.t.post(ctx, "/Msg/SendTxt", req, nil, &r); err != nil {
			return "", err
		}
		return msgID(r), nil
	}
}

func (c *Client) uploadFileBinary(ctx context.Context, wxid, fileName string, data []byte) (uploadFileResp, error) {
	var uploaded uploadFileResp
	if err := c.t.postMultipartFile(ctx, "/Tools/UploadFileBinary", "file", fileName, data, map[string]string{"wxid": wxid}, &uploaded); err != nil {
		return uploadFileResp{}, err
	}
	if uploaded.MediaID == "" {
		return uploadFileResp{}, fmt.Errorf("wechat: upload file response missing mediaId")
	}
	return uploaded, nil
}

func buildFileAppXML(fileName, mediaID string, totalLen int64) string {
	return fmt.Sprintf("<appmsg appid='' sdkver=''><title>%s</title><type>6</type><appattach><totallen>%d</totallen><attachid>%s</attachid><fileext>%s</fileext></appattach></appmsg>",
		html.EscapeString(fileName), totalLen, html.EscapeString(mediaID), html.EscapeString(fileExt(fileName)))
}

func fileExt(fileName string) string {
	idx := strings.LastIndex(fileName, ".")
	if idx < 0 || idx == len(fileName)-1 {
		return ""
	}
	return fileName[idx+1:]
}

func (c *Client) RedactMessage(ctx context.Context, chat remoteapi.ChatID, id remoteapi.MessageID) error {
	c.mu.RLock()
	wxid := c.wxid
	c.mu.RUnlock()
	newMsgID := parseMsgID(id)
	req := revokeReq{Wxid: wxid, ToWxid: string(chat), NewMsgId: newMsgID}
	return c.t.post(ctx, "/Msg/Revoke", req, nil, nil)
}

// UploadMedia satisfies remoteapi.MediaUploader for CDN-style media.
func (c *Client) UploadMedia(ctx context.Context, chat remoteapi.ChatID, m remoteapi.Media) (string, error) {
	c.mu.RLock()
	wxid := c.wxid
	c.mu.RUnlock()
	req := uploadImgReq{Wxid: wxid, ToWxid: string(chat), Base64: base64.StdEncoding.EncodeToString(m.Data)}
	var r sendResp
	if err := c.t.post(ctx, "/Msg/UploadImg", req, nil, &r); err != nil {
		return "", err
	}
	return string(msgID(r)), nil
}

// ---------------------------------------------------------------------------
// directory
// ---------------------------------------------------------------------------

func (c *Client) GetContact(ctx context.Context, id remoteapi.UserID) (remoteapi.Contact, error) {
	c.mu.RLock()
	wxid := c.wxid
	c.mu.RUnlock()
	var raw any
	if err := c.t.post(ctx, "/Friend/GetContractDetail",
		map[string]any{"Wxid": wxid, "Towxids": string(id)}, nil, &raw); err != nil {
		return remoteapi.Contact{}, err
	}
	ct := parseContactDetail(raw, id)
	if ct.ID == "" {
		ct.ID = id
	}
	if ct.Name == "" {
		ct.Name = string(id)
	}
	return ct, nil
}

func parseContactDetail(raw any, fallback remoteapi.UserID) remoteapi.Contact {
	// API shape often is: {ContactList:[{UserName:{string:"..."}, NickName:{string:"..."}, ...}]}
	if m, ok := raw.(map[string]any); ok {
		if list, ok := m["ContactList"].([]any); ok && len(list) > 0 {
			if item, ok := list[0].(map[string]any); ok {
				return contactFromMap(item, fallback)
			}
		}
		return contactFromMap(m, fallback)
	}
	return remoteapi.Contact{ID: fallback, Name: string(fallback)}
}

func contactFromMap(m map[string]any, fallback remoteapi.UserID) remoteapi.Contact {
	id := firstString(m, "UserName", "Wxid", "wxid", "Username", "userName")
	if id == "" {
		id = string(fallback)
	}
	description, hasDescription := nestedString(m, "CustomizedInfo", "ExternalInfo")
	avatar := firstString(m, "BigHeadImgUrl", "SmallHeadImgUrl", "HeadImgUrl", "HeadUrl", "BrandIconURL")
	if brandIcon, ok := nestedString(m, "CustomizedInfo", "BrandIconURL"); ok && brandIcon != "" {
		avatar = brandIcon
	}
	extra := map[string]any{}
	if hasDescription {
		// Rails parity: Contact#official_account? is true when description is not nil.
		// Empty string still means official account.
		extra["description"] = description
		extra["official_account"] = true
	}
	if members := chatMemberIDsFromContactMap(m); len(members) > 0 {
		extra["member_list"] = members
	}
	if alias := firstString(m, "Alias", "alias"); alias != "" {
		extra["alias"] = alias
	}
	if verify := firstString(m, "VerifyInfo"); verify != "" {
		extra["verify_info"] = verify
	}
	return remoteapi.Contact{
		ID:        remoteapi.UserID(id),
		Name:      firstString(m, "NickName", "nickname", "Name", "name", "DisplayName", "displayname", "VerifyInfo"),
		Remark:    firstString(m, "Remark", "remark", "RemarkName"),
		AvatarURL: avatar,
		Extra:     extra,
	}
}

func (c *Client) ListContacts(ctx context.Context) ([]remoteapi.Contact, error) {
	c.mu.RLock()
	wxid := c.wxid
	c.mu.RUnlock()
	var raw any
	if err := c.t.post(ctx, "/Friend/GetContractList", map[string]any{"Wxid": wxid}, nil, &raw); err != nil {
		return nil, err
	}
	return parseContactList(raw), nil
}

func parseContactList(raw any) []remoteapi.Contact {
	out := []remoteapi.Contact{}
	switch v := raw.(type) {
	case []any:
		for _, item := range v {
			out = appendContact(out, item)
		}
	case map[string]any:
		if list, ok := v["ContactUsernameList"].([]any); ok {
			for _, item := range list {
				id := fmt.Sprint(item)
				if id != "" && id != "<nil>" {
					out = append(out, remoteapi.Contact{ID: remoteapi.UserID(id), Name: id})
				}
			}
			return out
		}
		for _, key := range []string{"List", "list", "Contacts", "contacts", "Items", "items"} {
			if list, ok := v[key].([]any); ok {
				for _, item := range list {
					out = appendContact(out, item)
				}
				return out
			}
		}
	}
	return out
}

func appendContact(out []remoteapi.Contact, item any) []remoteapi.Contact {
	m, ok := item.(map[string]any)
	if !ok {
		id := fmt.Sprint(item)
		if id != "" && id != "<nil>" {
			return append(out, remoteapi.Contact{ID: remoteapi.UserID(id), Name: id})
		}
		return out
	}
	ct := contactFromMap(m, "")
	if ct.ID == "" {
		return out
	}
	return append(out, ct)
}

func nestedString(m map[string]any, path ...string) (string, bool) {
	v, ok := nestedAny(m, path...)
	if !ok {
		return "", false
	}
	return anyString(v), true
}

func nestedAny(m map[string]any, path ...string) (any, bool) {
	var cur any = m
	for _, key := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		v, exists := mm[key]
		if !exists {
			return nil, false
		}
		cur = v
	}
	return cur, true
}

func firstString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := m[key]; ok {
			if s := anyString(v); s != "" {
				return s
			}
		}
	}
	return ""
}

func anyString(v any) string {
	switch val := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(val)
	case map[string]any:
		// protobuf-json style: {"string":"..."}
		if s := anyString(val["string"]); s != "" {
			return s
		}
		if s := anyString(val["Str"]); s != "" {
			return s
		}
		if s := anyString(val["Value"]); s != "" {
			return s
		}
		return ""
	default:
		s := strings.TrimSpace(fmt.Sprint(v))
		if s == "<nil>" || s == "map[]" {
			return ""
		}
		return s
	}
}

func (c *Client) GetChat(ctx context.Context, id remoteapi.ChatID) (remoteapi.Chat, error) {
	isGroup := strings.HasSuffix(string(id), "@chatroom")
	if !isGroup {
		ct, err := c.GetContact(ctx, remoteapi.UserID(id))
		if err != nil {
			return remoteapi.Chat{}, err
		}
		return remoteapi.Chat{ID: id, Name: pick(ct.Remark, ct.Name), IsGroup: false, Avatar: ct.AvatarURL, Extra: ct.Extra}, nil
	}
	c.mu.RLock()
	wxid := c.wxid
	c.mu.RUnlock()

	// Rails parity: chatroom member wxid list primarily comes from contact detail
	// NewChatroomData.ChatRoomMember, not from Group/GetChatRoomInfo.
	chat := remoteapi.Chat{ID: id, Name: string(id), IsGroup: true, Extra: map[string]any{}}
	var raw any
	if err := c.t.post(ctx, "/Friend/GetContractDetail",
		map[string]any{"Wxid": wxid, "Towxids": string(id)}, nil, &raw); err == nil {
		if contactMap, ok := firstContactMap(raw); ok {
			ct := contactFromMap(contactMap, remoteapi.UserID(id))
			chat.Name = pick(ct.Remark, ct.Name, string(id))
			chat.Avatar = ct.AvatarURL
			for k, v := range ct.Extra {
				chat.Extra[k] = v
			}
			chat.Members = chatMemberIDsFromContactMap(contactMap)
		}
	} else {
		log.Printf("failed to fetch chatroom contact detail chat=%s: %v", id, err)
	}

	// Fallback/merge for APIs that return room metadata/member list from Group/GetChatRoomInfo.
	if chat.Name == string(id) || chat.Avatar == "" || len(chat.Members) == 0 {
		var groupRaw any
		if err := c.t.post(ctx, "/Group/GetChatRoomInfo",
			map[string]any{"Wxid": wxid, "QID": string(id)}, nil, &groupRaw); err == nil {
			m, _ := groupRaw.(map[string]any)
			if name := pick(firstString(m, "Remark", "RemarkName"), firstString(m, "NickName", "Name")); name != "" {
				chat.Name = name
			}
			if avatar := firstString(m, "ChatRoomHeadImgUrl", "BigHeadImgUrl", "HeadImgUrl", "SmallHeadImgUrl"); avatar != "" {
				chat.Avatar = avatar
			}
			members, memberContacts := parseChatMembers(m)
			if len(chat.Members) == 0 {
				chat.Members = members
			}
			if len(memberContacts) > 0 {
				chat.Extra["member_contacts"] = memberContacts
			}
		} else {
			log.Printf("failed to fetch chatroom group info chat=%s: %v", id, err)
		}
	}
	chat.Members = dedupeUserIDs(chat.Members)
	if len(chat.Members) > 0 {
		chat.Extra["member_count"] = len(chat.Members)
		if _, ok := chat.Extra["member_contacts"]; !ok {
			contacts := c.fetchChatMemberContacts(ctx, wxid, id, chat.Members)
			if len(contacts) > 0 {
				chat.Extra["member_contacts"] = contacts
			}
		}
	}
	return chat, nil
}

func firstContactMap(raw any) (map[string]any, bool) {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, false
	}
	if list, ok := m["ContactList"].([]any); ok && len(list) > 0 {
		item, ok := list[0].(map[string]any)
		return item, ok
	}
	return m, true
}

func contactListFromDetail(raw any) []remoteapi.Contact {
	out := []remoteapi.Contact{}
	m, ok := raw.(map[string]any)
	if !ok {
		return out
	}
	if list, ok := m["ContactList"].([]any); ok {
		for _, item := range list {
			if mm, ok := item.(map[string]any); ok {
				ct := contactFromMap(mm, "")
				if ct.ID != "" {
					out = append(out, ct)
				}
			}
		}
		return out
	}
	ct := contactFromMap(m, "")
	if ct.ID != "" {
		out = append(out, ct)
	}
	return out
}

func chatMemberIDsFromContactMap(m map[string]any) []remoteapi.UserID {
	for _, path := range [][]string{
		{"NewChatroomData", "ChatRoomMember"},
		{"NewChatRoomData", "ChatRoomMember"},
		{"ChatRoomMember"},
		{"MemberList"},
		{"Members"},
	} {
		if v, ok := nestedAny(m, path...); ok {
			if members := parseChatMemberIDs(v); len(members) > 0 {
				return dedupeUserIDs(members)
			}
		}
	}
	return nil
}

func parseChatMembers(m map[string]any) ([]remoteapi.UserID, []remoteapi.Contact) {
	members := []remoteapi.UserID{}
	contacts := []remoteapi.Contact{}
	for _, key := range []string{"MemberList", "Members", "member_list", "ChatRoomMember"} {
		v, ok := m[key]
		if !ok {
			continue
		}
		members = append(members, parseChatMemberIDs(v)...)
		if list, ok := v.([]any); ok {
			for _, item := range list {
				if mm, ok := item.(map[string]any); ok {
					ct := contactFromMap(mm, "")
					if roomName := firstString(mm, "DisplayName", "displayname", "RemarkName", "ChatRoomNickName", "RoomNickName", "MemberDisplayName"); roomName != "" {
						if ct.Extra == nil {
							ct.Extra = map[string]any{}
						}
						ct.Extra["room_display_name"] = roomName
					}
					if ct.ID != "" && (ct.Name != "" || ct.Remark != "" || ct.AvatarURL != "" || ct.Extra["room_display_name"] != nil) {
						contacts = append(contacts, ct)
					}
				}
			}
		}
		break
	}
	return dedupeUserIDs(members), contacts
}

func parseChatMemberIDs(v any) []remoteapi.UserID {
	members := []remoteapi.UserID{}
	switch val := v.(type) {
	case []any:
		for _, item := range val {
			members = append(members, parseChatMemberIDs(item)...)
		}
	case map[string]any:
		if id := firstString(val, "UserName", "Wxid", "wxid", "Username", "userName"); id != "" {
			members = append(members, remoteapi.UserID(id))
			return members
		}
		for _, key := range []string{"ChatRoomMember", "MemberList", "Members", "List", "list"} {
			if nested, ok := val[key]; ok {
				members = append(members, parseChatMemberIDs(nested)...)
			}
		}
	case string:
		s := strings.TrimSpace(val)
		if s == "" {
			return members
		}
		var decoded any
		if json.Unmarshal([]byte(s), &decoded) == nil {
			return parseChatMemberIDs(decoded)
		}
		for _, part := range strings.Split(s, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				members = append(members, remoteapi.UserID(part))
			}
		}
	default:
		if id := anyString(val); id != "" {
			members = append(members, remoteapi.UserID(id))
		}
	}
	return members
}

func dedupeUserIDs(in []remoteapi.UserID) []remoteapi.UserID {
	seen := map[remoteapi.UserID]bool{}
	out := make([]remoteapi.UserID, 0, len(in))
	for _, id := range in {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func (c *Client) fetchChatMemberContacts(ctx context.Context, wxid string, chatroom remoteapi.ChatID, members []remoteapi.UserID) []remoteapi.Contact {
	contacts := []remoteapi.Contact{}
	const chunkSize = 20
	for start := 0; start < len(members); start += chunkSize {
		end := start + chunkSize
		if end > len(members) {
			end = len(members)
		}
		ids := userIDs(members[start:end])
		var raw any
		if err := c.t.post(ctx, "/Friend/GetContractDetail", map[string]any{
			"Wxid":     wxid,
			"ChatRoom": string(chatroom),
			"Towxids":  strings.Join(ids, ","),
		}, nil, &raw); err != nil {
			log.Printf("failed to fetch chatroom member details chat=%s members=%d: %v", chatroom, len(ids), err)
			continue
		}
		contacts = append(contacts, contactListFromDetail(raw)...)
	}
	return contacts
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func userIDs(ids []remoteapi.UserID) []string {
	out := make([]string, len(ids))
	for i, v := range ids {
		out[i] = string(v)
	}
	return out
}

func msgID(r sendResp) remoteapi.MessageID {
	if r.NewMsgId != 0 {
		return remoteapi.MessageID(fmt.Sprint(r.NewMsgId))
	}
	return remoteapi.MessageID(fmt.Sprint(r.MsgId))
}

func parseMsgID(id remoteapi.MessageID) int64 {
	var v int64
	fmt.Sscan(string(id), &v)
	return v
}

func pick(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
