// Package matrixas implements a minimal Matrix application service layer on
// top of the protocol-neutral connector. It is intentionally small: it gives us
// a mautrix-telegram-like management bot now, while the remoteapi abstraction
// remains ready for a full bridgev2 portal implementation later.
package matrixas

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/leat/mautrix-go-wechat/internal/config"
	"github.com/leat/mautrix-go-wechat/internal/connector"
	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

// Server owns the Matrix appservice, the remote connector and the management
// bot command loop.
type Server struct {
	cfg         *config.Config
	as          *appservice.AppService
	conn        *connector.Connector
	api         remoteapi.PuppetAPI
	apiCfg      remoteapi.Config
	sessionFile string
	portals     *portalStore
	accounts    *accountStore
	contacts    *contactStore

	mu             sync.Mutex
	rooms          map[id.RoomID]bool
	messageEvents  map[string]id.EventID
	remoteRun      bool
	remoteCtx      context.Context
	remoteStop     context.CancelFunc
	contactRefresh map[string]bool
}

// New creates the appservice and remote connector from config + registration.
func New(cfg *config.Config, regPath string, apiCfg remoteapi.Config) (*Server, error) {
	reg, err := appservice.LoadRegistration(regPath)
	if err != nil {
		return nil, fmt.Errorf("load registration: %w", err)
	}

	as, err := appservice.CreateFull(appservice.CreateOpts{
		Registration:     reg,
		HomeserverDomain: cfg.Homeserver.Domain,
		HomeserverURL:    cfg.Homeserver.Address,
		HostConfig: appservice.HostConfig{
			Hostname: cfg.Appservice.Hostname,
			Port:     uint16(cfg.Appservice.Port),
		},
	})
	if err != nil {
		return nil, err
	}
	as.Log = zerolog.New(os.Stdout).With().Timestamp().Str("component", "appservice").Logger()
	as.QueryHandler = queryHandler{bot: id.NewUserID(reg.SenderLocalpart, cfg.Homeserver.Domain), domain: cfg.Homeserver.Domain, prefix: reg.ID + "_"}

	s := &Server{cfg: cfg, as: as, apiCfg: apiCfg, sessionFile: cfg.Bridge.SessionFile, rooms: map[id.RoomID]bool{}, messageEvents: map[string]id.EventID{}, contactRefresh: map[string]bool{}}
	if s.sessionFile == "" {
		s.sessionFile = "/data/session.json"
	}
	portalFile := cfg.Bridge.PortalFile
	if portalFile == "" {
		portalFile = "/data/portals.json"
	}
	s.portals = newPortalStore(portalFile)
	accountFile := cfg.Bridge.AccountFile
	if accountFile == "" {
		accountFile = "/data/accounts.json"
	}
	s.accounts = newAccountStore(accountFile)
	contactFile := cfg.Bridge.ContactFile
	if contactFile == "" {
		contactFile = "/data/contacts.json"
	}
	s.contacts = newContactStore(contactFile)

	conn, err := connector.New(cfg.Network.Protocol, apiCfg, s)
	if err != nil {
		return nil, err
	}
	s.conn = conn
	s.api = conn.API()
	if cfg.Network.Receive.Mode == "push" {
		as.Router.HandleFunc("POST "+cfg.Network.Receive.WebhookPath, s.handleWebhook)
	}
	return s, nil
}

// Run starts the appservice HTTP listener, event loop and remote connector if
// an existing backend session is already valid.
func (s *Server) Run(ctx context.Context) error {
	if err := s.as.BotIntent().EnsureRegistered(ctx); err != nil {
		log.Printf("warning: failed to ensure bot registered: %v", err)
	}
	s.as.Ready = true

	go s.as.Start()
	go s.matrixLoop(ctx)

	if ok, _ := s.api.IsLoggedIn(ctx); ok {
		if account := s.currentAccountID(ctx); account != "" {
			if _, err := s.ensureAccountSpace(ctx, account); err != nil {
				log.Printf("failed to ensure account space for %s: %v", account, err)
			}
		}
		s.startRemote(ctx)
	}

	<-ctx.Done()
	s.as.Ready = false
	s.as.Stop()
	s.stopRemote()
	return ctx.Err()
}

func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	receiver, ok := s.api.(remoteapi.WebhookReceiver)
	if !ok {
		http.Error(w, "backend does not support webhooks", http.StatusNotImplemented)
		return
	}
	payload, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		log.Printf("webhook request failed: path=%s read body: %v", r.URL.Path, err)
		http.Error(w, "read request body", http.StatusBadRequest)
		return
	}
	if len(payload) == 0 {
		log.Printf("webhook request ignored empty body: path=%s", r.URL.Path)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
		return
	}
	log.Printf("webhook request: path=%s bytes=%d", r.URL.Path, len(payload))
	if err := receiver.HandleWebhook(r.Context(), payload); err != nil {
		log.Printf("webhook request failed: path=%s bytes=%d error=%v", r.URL.Path, len(payload), err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	log.Printf("webhook request handled: path=%s bytes=%d", r.URL.Path, len(payload))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func (s *Server) matrixLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case evt := <-s.as.Events:
			if evt == nil {
				continue
			}
			s.handleMatrixEvent(ctx, evt)
		}
	}
}

func (s *Server) handleMatrixEvent(ctx context.Context, evt *event.Event) {
	bot := s.as.BotMXID()
	log.Printf("matrix event: room=%s sender=%s type=%s state=%v", evt.RoomID, evt.Sender, evt.Type.String(), evt.StateKey != nil)
	if evt.Sender == bot || s.isBridgeUser(evt.Sender) {
		return
	}

	// If the management room is encrypted, the appservice receives m.room.encrypted
	// and cannot read commands like "help" without an E2EE crypto store. For the
	// current MVP, ask the user to use an unencrypted management room.
	if evt.Type == event.EventEncrypted {
		s.addRoom(evt.RoomID)
		s.notice(ctx, evt.RoomID, "我收到了加密消息，但当前 bridge 还没有启用 Matrix E2EE 解密。请新建一个未加密房间邀请我，或关闭此房间加密后再发送 `help` / `login`。")
		return
	}

	// Accept invites to the bot, then send a management-room greeting.
	if evt.Type == event.StateMember && evt.StateKey != nil && *evt.StateKey == bot.String() {
		mem := evt.Content.AsMember()
		if mem.Membership == event.MembershipInvite {
			if _, err := s.as.BotIntent().JoinRoomByID(ctx, evt.RoomID); err != nil {
				log.Printf("failed to join management room %s: %v", evt.RoomID, err)
				return
			}
			s.addRoom(evt.RoomID)
			s.notice(ctx, evt.RoomID, "你好，我是 mautrix-go-wechat。发送 `help` 查看命令，发送 `login` 开始扫码登录。")
		}
		return
	}

	if evt.Type != event.EventMessage && evt.Type != event.EventSticker || evt.RoomID == "" {
		return
	}
	msg := evt.Content.AsMessage()
	log.Printf("matrix message: room=%s sender=%s eventtype=%s msgtype=%s body=%q", evt.RoomID, evt.Sender, evt.Type.String(), msg.MsgType, msg.Body)
	if rec, ok := s.portals.byRoom(evt.RoomID); ok {
		if evt.Type == event.EventSticker || msg.MsgType.IsMedia() {
			s.handlePortalMediaMessage(ctx, rec, evt.Sender, msg, evt.Type)
			return
		}
		if msg.MsgType != event.MsgText && msg.MsgType != event.MsgNotice {
			return
		}
		s.handlePortalMessage(ctx, rec, evt.Sender, strings.TrimSpace(msg.Body))
		return
	}
	if msg.MsgType != event.MsgText && msg.MsgType != event.MsgNotice {
		return
	}
	body := strings.TrimSpace(msg.Body)
	s.addRoom(evt.RoomID)
	s.handleCommand(ctx, evt.RoomID, evt.Sender, body)
}

func (s *Server) addRoom(roomID id.RoomID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rooms[roomID] = true
}

func (s *Server) managementRooms() []id.RoomID {
	s.mu.Lock()
	defer s.mu.Unlock()
	rooms := make([]id.RoomID, 0, len(s.rooms))
	for room := range s.rooms {
		rooms = append(rooms, room)
	}
	return rooms
}

func (s *Server) notice(ctx context.Context, room id.RoomID, body string) {
	_, err := s.as.BotIntent().SendMessageEvent(ctx, room, event.EventMessage, &event.MessageEventContent{MsgType: event.MsgNotice, Body: body})
	if err != nil {
		log.Printf("failed to send notice to %s: %v", room, err)
	}
}

func (s *Server) notifyManagement(ctx context.Context, body string) {
	rooms := s.managementRooms()
	if len(rooms) == 0 {
		log.Printf("management notification with no management rooms: %s", body)
		return
	}
	for _, room := range rooms {
		s.notice(ctx, room, body)
	}
}

func trimPreview(s string) string {
	s = strings.TrimSpace(s)
	if len([]rune(s)) <= 160 {
		return s
	}
	r := []rune(s)
	return string(r[:160]) + "…"
}

func (s *Server) text(ctx context.Context, room id.RoomID, body string) {
	_, err := s.as.BotIntent().SendMessageEvent(ctx, room, event.EventMessage, &event.MessageEventContent{MsgType: event.MsgText, Body: body})
	if err != nil {
		log.Printf("failed to send text to %s: %v", room, err)
	}
}

func (s *Server) image(ctx context.Context, room id.RoomID, data []byte, body string) error {
	resp, err := s.as.BotIntent().UploadBytesWithName(ctx, data, http.DetectContentType(data), "login-qr.png")
	if err != nil {
		return err
	}
	_, err = s.as.BotIntent().SendMessageEvent(ctx, room, event.EventMessage, &event.MessageEventContent{
		MsgType: event.MsgImage,
		Body:    body,
		URL:     resp.ContentURI.CUString(),
	})
	return err
}

// EventSink implementation: remote -> Matrix portal rooms.
func (s *Server) OnMessage(ctx context.Context, m *remoteapi.InboundMessage) {
	log.Printf("remote message: chat=%s sender=%s type=%d text=%q", m.Chat, m.Sender, m.Type, m.Text)
	if m.Chat == "" {
		log.Printf("dropping remote message with empty chat: sender=%s type=%d text=%q", m.Sender, m.Type, m.Text)
		return
	}
	if rec, ok := s.portals.get(m.Chat); ok && rec.RoomID != "" {
		s.bridgeRemoteToPortal(ctx, rec, m)
		return
	}
	if s.portalCreationMode() == "manual" {
		s.addPendingPortal(ctx, m)
		return
	}
	rec, err := s.ensurePortal(ctx, m.Chat)
	if err != nil {
		log.Printf("failed to ensure portal for %s: %v", m.Chat, err)
		for _, room := range s.managementRooms() {
			s.notice(ctx, room, fmt.Sprintf("为 %s 创建 portal 失败: %v", m.Chat, err))
		}
		return
	}

	s.notifyManagement(ctx, fmt.Sprintf("已自动创建 WeChat portal:\n- chat: %s\n- room: %s\n- name: %s", rec.ChatID, rec.RoomID, rec.Name))
	s.bridgeRemoteToPortal(ctx, rec, m)
}

func (s *Server) bridgeRemoteToPortal(ctx context.Context, rec portalRecord, m *remoteapi.InboundMessage) {
	sender := m.Sender
	if sender == "" {
		sender = remoteapi.UserID(m.Chat)
	}
	client := s.as.BotIntent().Client
	if shouldJoinPuppet(s.currentAccountID(ctx), sender) {
		client = s.ensurePuppetJoined(ctx, sender, rec.RoomID)
	}
	content := matrixContentFromRemote(m)
	eventType := matrixEventTypeFromRemote(m)
	if m.Media != nil && len(m.Media.Data) > 0 && content.MsgType.IsMedia() {
		mimeType := m.Media.MimeType
		if mimeType == "" {
			mimeType = http.DetectContentType(m.Media.Data)
		}
		fileName := m.Media.FileName
		if fileName == "" {
			fileName = "wechat-media"
		}
		resp, err := s.as.BotIntent().UploadBytesWithName(ctx, m.Media.Data, mimeType, fileName)
		if err != nil {
			log.Printf("failed to upload remote media %s to Matrix room %s: %v", m.ID, rec.RoomID, err)
			content = &event.MessageEventContent{MsgType: event.MsgText, Body: fmt.Sprintf("%s（媒体上传失败: %v）", m.Text, err)}
			eventType = event.EventMessage
		} else {
			content.URL = resp.ContentURI.CUString()
			content.Info = &event.FileInfo{MimeType: mimeType, Size: len(m.Media.Data), Width: intFromExtra(m.Extra, "width"), Height: intFromExtra(m.Extra, "height")}
			if m.Type == remoteapi.MsgEmoji {
				s.addRemoteEmojiToSpaceStickerPack(ctx, m, content.URL)
			}
		}
	}
	resp, err := client.SendMessageEvent(ctx, rec.RoomID, eventType, content, mautrix.ReqSendEvent{Timestamp: m.Timestamp.UnixMilli()})
	if err != nil {
		log.Printf("failed to bridge remote msg %s to %s: %v", m.ID, rec.RoomID, err)
		s.sendPortalNotice(ctx, rec.RoomID, fmt.Sprintf("桥接消息失败: %v", err))
		return
	}
	if m.ID != "" && resp != nil && resp.EventID != "" {
		s.rememberMessageEvent(m.Chat, m.ID, resp.EventID)
	}
}

func (s *Server) addRemoteEmojiToSpaceStickerPack(ctx context.Context, m *remoteapi.InboundMessage, mxcURL id.ContentURIString) {
	sticker, ok := wechatStickerImageFromRemoteEmoji(m, mxcURL)
	if !ok {
		return
	}
	account := s.currentAccountID(ctx)
	if account == "" {
		log.Printf("skip adding WeChat emoji sticker %s to Space pack: current account is unknown", m.ID)
		return
	}
	space, err := s.ensureAccountSpace(ctx, account)
	if err != nil {
		log.Printf("failed to ensure account Space for WeChat emoji sticker %s account=%s: %v", m.ID, account, err)
		return
	}
	if _, err := s.addStickerToAccountSpace(ctx, account, space, sticker); err != nil {
		log.Printf("failed to add WeChat emoji sticker %s to Space pack account=%s space=%s: %v", m.ID, account, space, err)
	}
}

func messageEventKey(chat remoteapi.ChatID, msg remoteapi.MessageID) string {
	return string(chat) + "\x00" + string(msg)
}

func (s *Server) rememberMessageEvent(chat remoteapi.ChatID, msg remoteapi.MessageID, eventID id.EventID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.messageEvents == nil {
		s.messageEvents = map[string]id.EventID{}
	}
	s.messageEvents[messageEventKey(chat, msg)] = eventID
	if len(s.messageEvents) > 5000 {
		s.messageEvents = map[string]id.EventID{messageEventKey(chat, msg): eventID}
	}
}

func (s *Server) getMessageEvent(chat remoteapi.ChatID, msg remoteapi.MessageID) id.EventID {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.messageEvents[messageEventKey(chat, msg)]
}

func intFromExtra(extra map[string]any, key string) int {
	if extra == nil {
		return 0
	}
	switch v := extra[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}

func (s *Server) addPendingPortal(ctx context.Context, m *remoteapi.InboundMessage) {
	name := string(m.Chat)
	if chat, err := s.api.GetChat(ctx, m.Chat); err == nil && chat.Name != "" {
		name = chat.Name
	}
	isNew, count := s.portals.addPending(pendingPortal{
		ChatID:      m.Chat,
		Sender:      m.Sender,
		Name:        name,
		LastMessage: m.Text,
		IsGroup:     strings.HasSuffix(string(m.Chat), "@chatroom"),
	})
	if isNew {
		s.notifyManagement(ctx, fmt.Sprintf("发现新的 WeChat 会话，等待确认创建 portal:\n- chat: %s\n- name: %s\n- sender: %s\n- preview: %s\n\n发送 `create %s` 创建房间，或 `create all` 创建全部待处理会话。", m.Chat, name, m.Sender, trimPreview(m.Text), m.Chat))
	} else if count%10 == 0 {
		s.notifyManagement(ctx, fmt.Sprintf("WeChat 会话 %s 仍在等待创建 portal，累计 %d 条消息。发送 `create %s` 创建。", m.Chat, count, m.Chat))
	}
}
func (s *Server) OnRedaction(ctx context.Context, r *remoteapi.Redaction) {
	if rec, ok := s.portals.get(r.Chat); ok {
		body := strings.TrimSpace(r.Text)
		if body == "" {
			who := string(r.By)
			if display := s.contactDisplayName(ctx, r.By); display != "" {
				who = display
			}
			if who == "" {
				who = "有人"
			}
			body = fmt.Sprintf("%s 撤回了一条消息", who)
		}
		if r.Target != "" {
			if target := s.getMessageEvent(r.Chat, r.Target); target != "" {
				content := &event.MessageEventContent{MsgType: event.MsgNotice, Body: body}
				content.RelatesTo = (&event.RelatesTo{}).SetReplyTo(target)
				if _, err := s.as.BotIntent().SendMessageEvent(ctx, rec.RoomID, event.EventMessage, content); err != nil {
					log.Printf("failed to send revoke reply to %s: %v", rec.RoomID, err)
				}
				return
			}
			body = fmt.Sprintf("%s（消息ID: %s）", body, r.Target)
		}
		s.sendPortalNotice(ctx, rec.RoomID, body)
	}
}
func (s *Server) OnReceipt(context.Context, *remoteapi.Receipt)       {}
func (s *Server) OnContactUpdate(context.Context, *remoteapi.Contact) {}
func (s *Server) OnConnected(ctx context.Context) {
	for _, room := range s.managementRooms() {
		s.notice(ctx, room, "WeChat backend connected.")
	}
}
func (s *Server) OnDisconnected(ctx context.Context, err error) {
	for _, room := range s.managementRooms() {
		s.notice(ctx, room, fmt.Sprintf("WeChat backend disconnected: %v", err))
	}
}

func (s *Server) startRemote(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.remoteRun {
		return
	}
	rctx, cancel := context.WithCancel(ctx)
	s.remoteCtx = rctx
	s.remoteStop = cancel
	s.remoteRun = true
	go func() {
		defer func() {
			s.mu.Lock()
			s.remoteRun = false
			s.mu.Unlock()
		}()
		if err := s.conn.Run(rctx); err != nil && rctx.Err() == nil {
			log.Printf("remote connector stopped: %v", err)
		}
	}()
}

func (s *Server) stopRemote() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.remoteStop != nil {
		s.remoteStop()
	}
	s.remoteRun = false
}

func (s *Server) currentProtocol() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Network.Protocol
}

func (s *Server) switchProtocol(protocol string) error {
	if protocol == "" || protocol == s.currentProtocol() {
		return nil
	}
	return s.rebuildConnector(protocol, nil)
}

func (s *Server) rebuildConnector(protocol string, session []byte) error {
	s.stopRemote()
	cfg := s.apiCfg
	if session != nil {
		cfg.Session = session
	}
	conn, err := connector.New(protocol, cfg, s)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.cfg.Network.Protocol = protocol
	s.apiCfg = cfg
	s.conn = conn
	s.api = conn.API()
	s.mu.Unlock()
	return nil
}

func (s *Server) useSavedAccount(id remoteapi.UserID) error {
	acc, ok := s.accounts.get(id)
	if !ok {
		return fmt.Errorf("unknown account %s", id)
	}
	protocol := acc.Protocol
	if protocol == "" {
		protocol = s.cfg.Network.Protocol
	}
	if err := s.rebuildConnector(protocol, acc.Session); err != nil {
		return err
	}
	s.accounts.setCurrent(id)
	if err := os.WriteFile(s.sessionFile, acc.Session, 0o600); err != nil {
		log.Printf("write current session %s: %v", s.sessionFile, err)
	}
	return nil
}

func (s *Server) saveSession(loginFlow string) {
	blob, err := s.api.DumpSession()
	if err != nil {
		log.Printf("dump session: %v", err)
		return
	}
	if err := os.WriteFile(s.sessionFile, blob, 0o600); err != nil {
		log.Printf("write session %s: %v", s.sessionFile, err)
	}
	self, err := s.api.Self(context.Background())
	if err != nil || self == "" {
		return
	}
	s.accounts.put(savedAccount{ID: self, Protocol: s.cfg.Network.Protocol, LoginFlow: loginFlow, Session: blob})
}

func decodeQR(data string) ([]byte, bool) {
	if idx := strings.Index(data, ","); strings.HasPrefix(data, "data:image/") && idx > 0 {
		data = data[idx+1:]
	}
	b, err := base64.StdEncoding.DecodeString(data)
	if err != nil || len(b) < 32 {
		return nil, false
	}
	return b, true
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (s *Server) isBridgeUser(user id.UserID) bool {
	localpart, domain, err := user.Parse()
	if err != nil || domain != s.cfg.Homeserver.Domain {
		return false
	}
	return strings.HasPrefix(localpart, s.cfg.Appservice.ID+"_")
}

type queryHandler struct {
	bot    id.UserID
	domain string
	prefix string
}

func (q queryHandler) QueryAlias(alias id.RoomAlias) bool { return false }
func (q queryHandler) QueryUser(userID id.UserID) bool {
	if userID == q.bot {
		return true
	}
	localpart, homeserver, err := userID.Parse()
	return err == nil && homeserver == q.domain && strings.HasPrefix(localpart, q.prefix)
}

var _ connector.EventSink = (*Server)(nil)
var _ appservice.QueryHandler = queryHandler{}
var _ = mautrix.MForbidden
