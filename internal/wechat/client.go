package wechat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
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
	c := &Client{
		t:      newTransport(cfg.BaseURL),
		events: make(chan remoteapi.Event, 64),
		done:   make(chan struct{}),
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

	done     chan struct{}
	stopOnce sync.Once
}

// compile-time interface checks
var (
	_ remoteapi.PuppetAPI         = (*Client)(nil)
	_ remoteapi.MediaUploader     = (*Client)(nil)
	_ remoteapi.Pinger            = (*Client)(nil)
	_ remoteapi.LoginInfoProvider = (*Client)(nil)
)

// ---------------------------------------------------------------------------
// lifecycle
// ---------------------------------------------------------------------------

func (c *Client) Connect(ctx context.Context) error {
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
		msgs, err := c.sync(ctx)
		cancel()

		if err != nil {
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
			if ev, ok := c.convertInbound(m); ok {
				c.emit(ev)
			}
		}
	}
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

func (c *Client) SendMessage(ctx context.Context, chat remoteapi.ChatID, msg remoteapi.OutgoingMessage) (remoteapi.MessageID, error) {
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
	var r contactResp
	// Endpoint name is illustrative; map to your actual Friend/GetContact path.
	if err := c.t.post(ctx, "/Friend/GetContractDetail",
		map[string]any{"Wxid": wxid, "Towxids": string(id)}, nil, &r); err != nil {
		return remoteapi.Contact{}, err
	}
	return remoteapi.Contact{
		ID:        remoteapi.UserID(r.Wxid),
		Name:      r.NickName,
		Remark:    r.Remark,
		AvatarURL: pick(r.BigHeadImg, r.HeadImgUrl),
	}, nil
}

func (c *Client) ListContacts(ctx context.Context) ([]remoteapi.Contact, error) {
	c.mu.RLock()
	wxid := c.wxid
	c.mu.RUnlock()
	var raw []contactResp
	if err := c.t.post(ctx, "/Friend/GetContractList", map[string]any{"Wxid": wxid}, nil, &raw); err != nil {
		return nil, err
	}
	out := make([]remoteapi.Contact, 0, len(raw))
	for _, r := range raw {
		out = append(out, remoteapi.Contact{
			ID: remoteapi.UserID(r.Wxid), Name: r.NickName, Remark: r.Remark,
			AvatarURL: pick(r.BigHeadImg, r.HeadImgUrl),
		})
	}
	return out, nil
}

func (c *Client) GetChat(ctx context.Context, id remoteapi.ChatID) (remoteapi.Chat, error) {
	isGroup := strings.HasSuffix(string(id), "@chatroom")
	if !isGroup {
		ct, err := c.GetContact(ctx, remoteapi.UserID(id))
		if err != nil {
			return remoteapi.Chat{}, err
		}
		return remoteapi.Chat{ID: id, Name: pick(ct.Remark, ct.Name), IsGroup: false}, nil
	}
	c.mu.RLock()
	wxid := c.wxid
	c.mu.RUnlock()
	var r struct {
		NickName string `json:"NickName"`
		Members  []struct {
			Wxid string `json:"Wxid"`
		} `json:"MemberList"`
	}
	if err := c.t.post(ctx, "/Group/GetChatRoomInfo",
		map[string]any{"Wxid": wxid, "QID": string(id)}, nil, &r); err != nil {
		return remoteapi.Chat{}, err
	}
	members := make([]remoteapi.UserID, 0, len(r.Members))
	for _, m := range r.Members {
		members = append(members, remoteapi.UserID(m.Wxid))
	}
	return remoteapi.Chat{ID: id, Name: r.NickName, IsGroup: true, Members: members}, nil
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

func pick(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
