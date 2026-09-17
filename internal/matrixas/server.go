// Package matrixas implements a minimal Matrix application service layer on
// top of the protocol-neutral connector. It is intentionally small: it gives us
// a mautrix-telegram-like management bot now, while the remoteapi abstraction
// remains ready for a full bridgev2 portal implementation later.
package matrixas

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
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
	"github.com/leat/mautrix-go-wechat/internal/wechat/textemoji"
)

type appServiceHTTPRuntime struct {
	server         *http.Server
	listener       net.Listener
	done           chan struct{}
	stopped        chan struct{}
	stopOnce       sync.Once
	serveErr       error
	unixSocketPath string
	unixSocketInfo os.FileInfo
}

type remoteConnector interface {
	Run(context.Context) error
	API() remoteapi.PuppetAPI
}

// Server owns the Matrix appservice, the remote connector and the management
// bot command loop.
type Server struct {
	cfg                              *config.Config
	as                               *appservice.AppService
	conn                             remoteConnector
	api                              remoteapi.PuppetAPI
	apiCfg                           remoteapi.Config
	sessionFile                      string
	portals                          *portalStore
	accounts                         *accountStore
	contacts                         *contactStore
	builtinEmojiStore                *builtinEmojiStore
	newConnector                     func(string, remoteapi.Config, connector.EventSink) (remoteConnector, error)
	newBuiltinPackVisibilityClient   func(id.UserID, string) (builtinPackVisibilityClient, error)
	builtinEmojiImporterFactory      func() builtinEmojiImportRunner
	builtinEmojiContextFactory       func(context.Context, time.Duration) (context.Context, context.CancelFunc)
	builtinEmojiNoticeContextFactory func(context.Context, time.Duration) (context.Context, context.CancelFunc)
	builtinEmojiSpaceResolver        func(context.Context, remoteapi.UserID) (id.RoomID, error)
	builtinEmojiNotice               func(context.Context, id.RoomID, string)
	builtinEmojiLogger               builtinEmojiLogSink
	builtinEmojiNow                  func() time.Time
	appServiceListen                 func(network, address string) (net.Listener, error)

	mu                    sync.Mutex
	runMu                 sync.Mutex
	appServiceLifecycleMu sync.Mutex
	appServiceHTTP        *appServiceHTTPRuntime
	accountTransitionMu   sync.Mutex
	importMu              sync.Mutex
	importRunning         bool
	importRuntime         *builtinEmojiRuntime
	// Only solitaire display uses this lock/cache; ordinary inbound delivery
	// and the backend's event queue are unchanged.
	solitaireDeliveryMu    sync.Mutex
	solitaireDeliveries    map[solitaireDeliveryKey]*solitaireDelivery
	solitaireDeliveryOrder []*solitaireDelivery
	// Lock order: spaceMu -> packMu -> accountDataMu -> accountStore.mu /
	// builtin store mutex. Code holding packMu must never acquire spaceMu.
	// Account-data transactions use lock-acquiring wrappers plus non-recursive
	// locked helpers so their read-modify-write sequences cannot interleave.
	portalCreateMu sync.Mutex
	spaceMu        sync.Mutex
	packMu         sync.Mutex
	accountDataMu  sync.Mutex
	// Test barriers invoked after a portal snapshot and immediately before the
	// corresponding image-pack lock.
	afterPortalSnapshot        func([]portalRecord)
	beforePackLock             func()
	beforeAccountDataLock      func()
	beforeRemoteRun            func(uint64)
	beforeRemotePublish        func()
	matrixOperationTimeout     time.Duration
	remoteStopTimeout          time.Duration
	rawImagePackState          rawImagePackState
	rooms                      map[id.RoomID]bool
	messageEvents              map[string]id.EventID
	remoteRun                  bool
	remoteCtx                  context.Context
	remoteStop                 context.CancelFunc
	remoteGeneration           uint64
	remoteDone                 chan struct{}
	remoteTransition           bool
	remoteTransitionGeneration uint64
	activeAccount              remoteapi.UserID
	contactRefresh             map[string]bool
	puppetJoins                map[string]bool
}

const (
	defaultMatrixOperationTimeout = 30 * time.Second
	defaultRemoteStopTimeout      = 30 * time.Second
	appServiceHTTPShutdownTimeout = 5 * time.Second
)

func (s *Server) matrixOperationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := s.matrixOperationTimeout
	if timeout <= 0 {
		timeout = defaultMatrixOperationTimeout
	}
	return context.WithTimeout(ctx, timeout)
}

// New creates the appservice and remote connector from config + registration.
func New(cfg *config.Config, regPath string, apiCfg remoteapi.Config) (*Server, error) {
	if err := cfg.ValidateBridgePathCollisions("", regPath); err != nil {
		return nil, err
	}
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

	s := &Server{cfg: cfg, as: as, apiCfg: apiCfg, sessionFile: cfg.Bridge.SessionFile, rooms: map[id.RoomID]bool{}, messageEvents: map[string]id.EventID{}, contactRefresh: map[string]bool{}, puppetJoins: map[string]bool{}}
	s.builtinEmojiStore = newBuiltinEmojiStore(cfg.Bridge.WechatBuiltinEmojiFile)
	s.portals = newPortalStore(cfg.Bridge.PortalFile)
	s.accounts = newAccountStore(cfg.Bridge.AccountFile)
	s.apiCfg = applyStartupSession(s.apiCfg, s.accounts, s.sessionFile)
	if current, ok := s.accounts.current(); ok {
		s.activeAccount = current.ID
	}
	s.contacts = newContactStore(cfg.Bridge.ContactFile)

	conn, err := connector.New(cfg.Network.Protocol, s.apiCfg, s)
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

func applyStartupSession(apiCfg remoteapi.Config, accounts *accountStore, legacySessionFile string) remoteapi.Config {
	if accounts != nil {
		if acc, ok := accounts.current(); ok {
			apiCfg.Session = append([]byte(nil), acc.Session...)
			log.Printf("using authoritative current account session for %s", acc.ID)
			return apiCfg
		}
	}
	if len(apiCfg.Session) > 0 || legacySessionFile == "" {
		return apiCfg
	}
	if session, err := os.ReadFile(legacySessionFile); err == nil {
		apiCfg.Session = session
		log.Printf("using legacy session fallback because no current account exists")
	}
	return apiCfg
}

// Run starts the appservice HTTP listener, event loop and remote connector if
// an existing backend session is already valid.
func (s *Server) Run(ctx context.Context) error {
	if !s.runMu.TryLock() {
		return errors.New("server Run is already active")
	}
	defer s.runMu.Unlock()

	runtime := s.captureBuiltinEmojiRuntime(ctx)
	defer s.clearBuiltinEmojiRuntime(runtime)
	ctx = runtime.ctx

	if err := s.as.BotIntent().EnsureRegistered(ctx); err != nil {
		log.Printf("warning: failed to ensure bot registered: %v", err)
	}
	s.as.Ready = true
	httpRuntime, err := s.startAppServiceHTTP()
	if err != nil {
		s.as.Ready = false
		return err
	}
	defer func() {
		s.stopAppServiceHTTP(httpRuntime)
		s.as.Ready = false
	}()
	defer func() {
		if stopErr := s.stopRemote(ctx); stopErr != nil {
			log.Printf("failed to stop remote connector during server shutdown: %v", stopErr)
		}
	}()

	go s.matrixLoop(ctx)

	loggedIn, _ := s.api.IsLoggedIn(ctx)
	s.bootstrapStartupSpaces(ctx, loggedIn)
	if s.shouldStartRemoteOnRun(loggedIn) {
		s.startRemote(ctx)
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-httpRuntime.done:
		serveErr := s.appServiceHTTPServeError(httpRuntime)
		if serveErr == nil || errors.Is(serveErr, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("appservice HTTP listener stopped: %w", serveErr)
	}
}

func (s *Server) startAppServiceHTTP() (*appServiceHTTPRuntime, error) {
	s.appServiceLifecycleMu.Lock()
	defer s.appServiceLifecycleMu.Unlock()
	if s.appServiceHTTP != nil {
		return nil, errors.New("appservice HTTP listener is already running")
	}
	if s.as == nil {
		return nil, errors.New("appservice is unavailable")
	}

	network := "tcp"
	address := s.as.Host.Address()
	unixSocketPath := ""
	if s.as.Host.IsUnixSocket() {
		network = "unix"
		address = s.as.Host.Hostname
		unixSocketPath = address
		if err := requireUnixSocketPathAbsent(unixSocketPath); err != nil {
			return nil, err
		}
	}
	listen := s.appServiceListen
	if listen == nil {
		listen = net.Listen
	}
	listener, err := listen(network, address)
	if err != nil {
		return nil, fmt.Errorf("listen for appservice HTTP on %s: %w", address, err)
	}

	runtime := &appServiceHTTPRuntime{
		server:         &http.Server{Handler: s.as.Router},
		listener:       listener,
		done:           make(chan struct{}),
		stopped:        make(chan struct{}),
		unixSocketPath: unixSocketPath,
	}
	if unixSocketPath != "" {
		unixListener, ok := listener.(*net.UnixListener)
		if !ok {
			_ = listener.Close()
			return nil, fmt.Errorf("appservice unix listener for %s has unexpected type %T", unixSocketPath, listener)
		}
		unixListener.SetUnlinkOnClose(false)
		runtime.unixSocketInfo, err = boundUnixSocketInfo(unixSocketPath)
		if err != nil {
			_ = listener.Close()
			return nil, err
		}
		s.as.Log.Info().Str("socket", unixSocketPath).Msg("Starting unix socket HTTP listener")
	} else {
		s.as.Log.Info().Str("address", listener.Addr().String()).Msg("Starting HTTP listener")
	}
	s.appServiceHTTP = runtime
	go s.serveAppServiceHTTP(runtime)
	return runtime, nil
}

func (s *Server) serveAppServiceHTTP(runtime *appServiceHTTPRuntime) {
	err := runtime.server.Serve(runtime.listener)
	s.appServiceLifecycleMu.Lock()
	runtime.serveErr = err
	close(runtime.done)
	s.appServiceLifecycleMu.Unlock()
}

func (s *Server) appServiceHTTPServeError(runtime *appServiceHTTPRuntime) error {
	s.appServiceLifecycleMu.Lock()
	defer s.appServiceLifecycleMu.Unlock()
	return runtime.serveErr
}

func (s *Server) stopAppServiceHTTP(runtime *appServiceHTTPRuntime) {
	if runtime == nil {
		return
	}
	runtime.stopOnce.Do(func() {
		defer close(runtime.stopped)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), appServiceHTTPShutdownTimeout)
		err := runtime.server.Shutdown(shutdownCtx)
		cancel()
		if err != nil {
			_ = runtime.server.Close()
		}
		<-runtime.done
		unlinkOwnedUnixSocket(runtime.unixSocketPath, runtime.unixSocketInfo)

		s.appServiceLifecycleMu.Lock()
		if s.appServiceHTTP == runtime {
			s.appServiceHTTP = nil
		}
		s.appServiceLifecycleMu.Unlock()
	})
	<-runtime.stopped
}

func requireUnixSocketPathAbsent(socketPath string) error {
	_, err := os.Lstat(socketPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect appservice unix socket path %s: %w", socketPath, err)
	}
	return fmt.Errorf("appservice unix socket path %s already exists; remove it manually after confirming no process is using it", socketPath)
}

func boundUnixSocketInfo(socketPath string) (os.FileInfo, error) {
	owned, err := os.Lstat(socketPath)
	if err != nil {
		return nil, fmt.Errorf("inspect bound appservice unix socket %s: %w", socketPath, err)
	}
	if owned.Mode()&os.ModeSocket == 0 {
		return nil, fmt.Errorf("bound appservice unix socket path %s is not a socket", socketPath)
	}
	return owned, nil
}

func unlinkOwnedUnixSocket(socketPath string, owned os.FileInfo) {
	if socketPath == "" || owned == nil {
		return
	}
	current, err := os.Lstat(socketPath)
	if err == nil && os.SameFile(owned, current) {
		_ = os.Remove(socketPath)
	}
}

func (s *Server) bootstrapStartupSpaces(ctx context.Context, loggedIn bool) {
	if loggedIn {
		if account := s.currentAccountID(ctx); account != "" {
			transition, err := s.ensureAccountSpaceTransition(ctx, account)
			if err != nil {
				log.Printf("failed to ensure account space for %s: %v (%s)", account, err, formatSpaceTransitionStatus(transition))
			} else {
				log.Printf("startup account space ready account=%s %s", account, formatSpaceTransitionStatus(transition))
			}
			return
		}
	}
	builtinPack, accountData := s.reconcileBuiltinEmojiPacks(ctx)
	warnings := append(append([]string(nil), builtinPack.Warnings...), accountData.Warnings...)
	log.Printf("startup built-in emoji bootstrap pack=%s rooms=%d/%d account_data=%s warnings=%s", builtinPack.Status, builtinPack.ReconciledRooms, builtinPack.AttemptedRooms, accountData.Status, strings.Join(warnings, "; "))
}

func (s *Server) shouldStartRemoteOnRun(loggedIn bool) bool {
	if loggedIn {
		return true
	}
	return s != nil && s.cfg != nil && s.cfg.Network.Receive.Mode == "push"
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
			go s.handlePortalMediaMessage(context.WithoutCancel(ctx), rec, evt.Sender, msg, evt.Type)
			return
		}
		if msg.MsgType != event.MsgText && msg.MsgType != event.MsgNotice {
			return
		}
		if entry, ok := parseSolitaireCommand(msg, s.commandPrefix()); ok {
			s.handleSolitaireReply(ctx, rec, evt, entry)
			return
		}
		s.handlePortalMessage(ctx, rec, evt.Sender, msg)
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
	if s.shouldRejectOfficialAccount(ctx, m.Chat) {
		log.Printf("dropping official account message because official_account_mode=reject: chat=%s sender=%s text=%q", m.Chat, m.Sender, trimPreview(m.Text))
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

func hasKnownWechatEmojiShortcode(body string) bool {
	known := false
	textemoji.ScanShortcodes(body, func(_, _ int, _ textemoji.Entry) {
		known = true
	})
	return known
}

func (s *Server) bridgeRemoteToPortal(ctx context.Context, rec portalRecord, m *remoteapi.InboundMessage) {
	if m.Solitaire != nil {
		if err := s.deliverSolitaireToPortal(ctx, rec, m); err != nil {
			log.Printf("failed to bridge solitaire %s to %s: %v", m.ID, rec.RoomID, err)
			s.sendPortalNotice(ctx, rec.RoomID, fmt.Sprintf("桥接接龙失败: %v", err))
		}
		return
	}
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
	if m.Type == remoteapi.MsgText && hasKnownWechatEmojiShortcode(content.Body) {
		images := s.resolvePortalBuiltinEmoji(ctx, rec.RoomID)
		if formatted, used := formatInlineWechatEmoji(content.Body, images); used {
			content.MsgType = event.MsgText
			content.Format = event.FormatHTML
			content.FormattedBody = formatted
		}
	}
	if shouldUploadRemoteMedia(m, eventType, content) {
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
			content.Info = &event.FileInfo{MimeType: mimeType, Size: len(m.Media.Data), Width: intFromExtra(m.Extra, "width"), Height: intFromExtra(m.Extra, "height"), Duration: intFromExtra(m.Extra, "duration")}
			if m.Type == remoteapi.MsgEmoji {
				s.addRemoteEmojiToStickerPacks(ctx, rec.RoomID, m, content.URL)
			}
		}
	}
	resp, err := client.SendMessageEvent(ctx, rec.RoomID, eventType, content, mautrix.ReqSendEvent{Timestamp: m.Timestamp.UnixMilli()})
	if err != nil {
		log.Printf("failed to bridge remote msg %s to %s: %v", m.ID, rec.RoomID, err)
		s.sendPortalNotice(ctx, rec.RoomID, fmt.Sprintf("桥接消息失败: %v", err))
		return
	}
	if resp != nil && resp.EventID != "" {
		for _, msgID := range messageIDAliases(m) {
			s.rememberMessageEvent(m.Chat, msgID, resp.EventID)
		}
	}
}

func (s *Server) addRemoteEmojiToStickerPacks(ctx context.Context, portal id.RoomID, m *remoteapi.InboundMessage, mxcURL id.ContentURIString) {
	sticker, ok := wechatStickerImageFromRemoteEmoji(m, mxcURL)
	if !ok {
		return
	}
	account := s.currentAccountID(ctx)
	if account == "" {
		log.Printf("skip adding WeChat emoji sticker %s to sticker packs: current account is unknown", m.ID)
		return
	}
	var space id.RoomID
	var err error
	if owner := s.portals.owner(); owner != "" {
		space, _, err = s.ensureAccountSpaceVisibleTo(ctx, account, owner)
	} else {
		space, err = s.ensureAccountSpace(ctx, account)
	}
	if err != nil {
		log.Printf("failed to ensure account Space for WeChat emoji sticker %s account=%s: %v", m.ID, account, err)
		return
	}
	stateKey := wechatStickerPackStateKey(account)
	owner := s.portals.owner()
	for _, room := range stickerPackCleanupRooms(space, portal) {
		s.removeStickerPackFromRoom(ctx, account, room)
	}
	for _, room := range stickerPackTargetRooms(space, portal) {
		count, err := s.addStickerToAccountSpace(ctx, account, room, sticker)
		if owner != "" && room == space {
			s.ensureUserSeesImagePackRoom(ctx, owner, room, stateKey)
		}
		if err != nil {
			log.Printf("failed to add WeChat emoji sticker %s to sticker pack account=%s room=%s: %v", m.ID, account, room, err)
			continue
		}
		log.Printf("added WeChat emoji sticker %s to sticker pack account=%s room=%s count=%d shortcode=%s", m.ID, account, room, count, sticker.Shortcode)
	}
	mirrored := s.syncStickerToElementPortals(ctx, account, sticker)
	log.Printf("mirrored WeChat emoji sticker %s to Element portal packs account=%s rooms=%d shortcode=%s", m.ID, account, mirrored, sticker.Shortcode)
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
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	if s.remoteRun || s.remoteTransition || s.conn == nil {
		s.mu.Unlock()
		return
	}
	conn := s.conn
	s.remoteGeneration++
	generation := s.remoteGeneration
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	beforeRun := s.beforeRemoteRun
	s.remoteCtx = rctx
	s.remoteStop = cancel
	s.remoteDone = done
	s.remoteRun = true
	s.mu.Unlock()

	go func() {
		defer func() {
			s.mu.Lock()
			if s.remoteGeneration == generation && s.remoteDone == done {
				s.remoteRun = false
				s.remoteCtx = nil
				s.remoteStop = nil
				s.remoteDone = nil
			}
			s.mu.Unlock()
			close(done)
		}()
		if beforeRun != nil {
			beforeRun(generation)
		}
		if err := conn.Run(rctx); err != nil && rctx.Err() == nil {
			log.Printf("remote connector generation %d stopped: %v", generation, err)
		}
	}()
}

func (s *Server) stopRemote(ctx context.Context) error {
	s.mu.Lock()
	if !s.remoteRun || s.remoteDone == nil {
		s.mu.Unlock()
		return nil
	}
	generation := s.remoteGeneration
	cancel := s.remoteStop
	done := s.remoteDone
	timeout := s.remoteStopTimeout
	if timeout <= 0 {
		timeout = defaultRemoteStopTimeout
	}
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if ctx == nil {
		ctx = context.Background()
	} else {
		ctx = context.WithoutCancel(ctx)
	}
	waitCtx, cancelWait := context.WithTimeout(ctx, timeout)
	defer cancelWait()
	select {
	case <-done:
		return nil
	case <-waitCtx.Done():
		return fmt.Errorf("stop remote connector generation %d: %w", generation, waitCtx.Err())
	}
}

func (s *Server) beginRemoteTransition() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.remoteTransitionGeneration++
	s.remoteTransition = true
	return s.remoteTransitionGeneration
}

func (s *Server) clearRemoteTransition(generation uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.remoteTransitionGeneration == generation {
		s.remoteTransition = false
	}
}

func (s *Server) currentProtocol() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Network.Protocol
}

func (s *Server) switchProtocol(ctx context.Context, protocol string) error {
	if protocol == "" || protocol == s.currentProtocol() {
		return nil
	}
	return s.rebuildConnector(ctx, protocol, nil)
}

func (s *Server) buildConnector(protocol string, cfg remoteapi.Config) (remoteConnector, error) {
	if s.newConnector != nil {
		return s.newConnector(protocol, cfg, s)
	}
	return connector.New(protocol, cfg, s)
}

func (s *Server) rebuildConnector(ctx context.Context, protocol string, session []byte) error {
	s.accountTransitionMu.Lock()
	defer s.accountTransitionMu.Unlock()

	s.mu.Lock()
	cfg := s.apiCfg
	s.mu.Unlock()
	if session != nil {
		cfg.Session = append([]byte(nil), session...)
	}
	conn, err := s.buildConnector(protocol, cfg)
	if err != nil {
		return err
	}
	transitionGeneration := s.beginRemoteTransition()
	published := false
	defer func() {
		if !published {
			s.clearRemoteTransition(transitionGeneration)
		}
	}()
	if err = s.stopRemote(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	beforePublish := s.beforeRemotePublish
	s.mu.Unlock()
	if beforePublish != nil {
		beforePublish()
	}
	s.mu.Lock()
	s.cfg.Network.Protocol = protocol
	s.apiCfg = cfg
	s.conn = conn
	s.api = conn.API()
	if s.remoteTransitionGeneration == transitionGeneration {
		s.remoteTransition = false
	}
	s.mu.Unlock()
	published = true
	return nil
}

func (s *Server) useSavedAccount(ctx context.Context, id remoteapi.UserID) error {
	s.accountTransitionMu.Lock()
	defer s.accountTransitionMu.Unlock()

	acc, ok := s.accounts.get(id)
	if !ok {
		return fmt.Errorf("unknown account %s", id)
	}
	protocol := acc.Protocol
	if protocol == "" {
		protocol = s.currentProtocol()
	}
	s.mu.Lock()
	cfg := s.apiCfg
	s.mu.Unlock()
	cfg.Session = append([]byte(nil), acc.Session...)
	conn, err := s.buildConnector(protocol, cfg)
	if err != nil {
		return err
	}
	previous, hadPrevious := s.accounts.current()
	changed, err := s.accounts.setCurrent(id)
	if err != nil {
		return fmt.Errorf("persist current account %s: %w", id, err)
	}
	if !changed {
		return fmt.Errorf("unknown account %s", id)
	}
	// Current is durable before the live connector is stopped or replaced.
	transitionGeneration := s.beginRemoteTransition()
	published := false
	defer func() {
		if !published {
			s.clearRemoteTransition(transitionGeneration)
		}
	}()
	if err = s.stopRemote(ctx); err != nil {
		if hadPrevious {
			_, rollbackErr := s.accounts.setCurrent(previous.ID)
			if rollbackErr != nil {
				return errors.Join(err, fmt.Errorf("restore current account %s: %w", previous.ID, rollbackErr))
			}
		}
		return err
	}
	s.mu.Lock()
	beforePublish := s.beforeRemotePublish
	s.mu.Unlock()
	if beforePublish != nil {
		beforePublish()
	}
	s.mu.Lock()
	s.cfg.Network.Protocol = protocol
	s.apiCfg = cfg
	s.conn = conn
	s.api = conn.API()
	s.activeAccount = id
	if s.remoteTransitionGeneration == transitionGeneration {
		s.remoteTransition = false
	}
	s.mu.Unlock()
	published = true
	return nil
}

func (s *Server) saveSession(ctx context.Context, loginFlow string) error {
	s.accountTransitionMu.Lock()
	defer s.accountTransitionMu.Unlock()
	return s.saveSessionLocked(ctx, loginFlow)
}

// saveSessionLocked requires accountTransitionMu to be held exactly once by
// the caller, including callers persisting the result of login/relogin work.
func (s *Server) saveSessionLocked(ctx context.Context, loginFlow string) error {
	// Capture the live API and protocol together once. Holding the account
	// transition lock through API reads and persistence prevents durable Current
	// from referring to a different account than the published connector.
	s.mu.Lock()
	api := s.api
	protocol := s.cfg.Network.Protocol
	s.mu.Unlock()
	if api == nil {
		return fmt.Errorf("dump session: live API is unavailable")
	}
	blob, err := api.DumpSession()
	if err != nil {
		return fmt.Errorf("dump session: %w", err)
	}
	selfCtx, cancelSelf := s.matrixOperationContext(ctx)
	self, err := api.Self(selfCtx)
	cancelSelf()
	if err != nil {
		return fmt.Errorf("get current account for session: %w", err)
	}
	if self == "" {
		return fmt.Errorf("get current account for session: empty account id")
	}
	snapshot := savedAccount{ID: self, Protocol: protocol, LoginFlow: loginFlow, Session: append([]byte(nil), blob...)}
	if err := s.accounts.put(snapshot); err != nil {
		return fmt.Errorf("persist session for %s: %w", self, err)
	}
	s.mu.Lock()
	s.activeAccount = self
	s.mu.Unlock()
	return nil
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
