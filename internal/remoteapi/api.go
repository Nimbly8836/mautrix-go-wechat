// Package remoteapi defines the protocol-neutral contract between the bridge
// core and a concrete chat backend (WeChat, QQ, ...).
//
// The bridge core (internal/connector) depends ONLY on the interfaces and
// types declared here. To support a new protocol you implement PuppetAPI in
// a new package (e.g. internal/qq) and register a Factory; nothing in the
// bridge core needs to change.
//
// Design rules:
//   - Types here must NOT leak protocol-specific concepts (no "wxid", no
//     "synckey"). Use neutral names: UserID, ChatID, cursor handling is the
//     implementation's private concern.
//   - The interface is intentionally small. Backends differ wildly in how
//     they deliver events (polling vs webhook); both are hidden behind the
//     Events() stream.
package remoteapi

import (
	"context"
	"time"
)

// ---------------------------------------------------------------------------
// Identity & registration
// ---------------------------------------------------------------------------

// UserID is the backend-native identifier of an account/contact, treated as an
// opaque string by the bridge core (e.g. a wxid, a QQ number).
type UserID string

// ChatID identifies a conversation: a direct chat (== peer UserID) or a group.
type ChatID string

// Factory builds a PuppetAPI for one logged-in account. Each protocol package
// registers exactly one Factory. The bridge core selects a Factory by name.
type Factory interface {
	// Name is the protocol id, e.g. "wechat", "qq". Used in config and as the
	// bridge network id.
	Name() string

	// New creates a client bound to a single login session. The returned
	// client is not yet connected; call Connect.
	New(cfg Config) (PuppetAPI, error)
}

// Config carries the minimal data a backend needs to construct a client.
// Backend-specific knobs live in Extra (decoded by the implementation).
type Config struct {
	// BaseURL / Token are common for HTTP-style backends. Implementations may
	// ignore them.
	BaseURL     string
	Token       string
	ReceiveMode string
	// Session is the opaque, previously-persisted session blob (see
	// PuppetAPI.DumpSession). Empty on first login.
	Session []byte
	// Extra holds protocol-specific options parsed from bridge config.
	Extra map[string]any
}

// ---------------------------------------------------------------------------
// The contract
// ---------------------------------------------------------------------------

// PuppetAPI is everything the bridge core needs from a backend. Implement this
// to add a protocol. Methods must be safe for concurrent use.
type PuppetAPI interface {
	// --- lifecycle ---

	// Connect starts the client and begins (re)delivering events on Events().
	// For polling backends this spins up the poll loop; for webhook backends it
	// registers/validates the callback. It returns once the client is ready or
	// an error occurs. It must not block for the lifetime of the connection.
	Connect(ctx context.Context) error

	// Disconnect stops event delivery and releases resources. Idempotent.
	Disconnect()

	// Events returns the single event stream for this client. The channel is
	// closed when the client is permanently disconnected.
	Events() <-chan Event

	// --- authentication ---

	// LoginFlows lists the interactive login methods this backend offers
	// (e.g. "qr", "phone"). Mirrors bridgev2's NetworkConnector.GetLoginFlows.
	LoginFlows() []LoginFlow

	// CreateLogin starts an interactive login for the chosen flow id and
	// returns a step-machine handle. Mirrors bridgev2's CreateLogin +
	// LoginProcess. The unified machine covers QR-scan and code-based flows.
	CreateLogin(ctx context.Context, flowID string) (LoginProcess, error)

	// Logout invalidates the current session on the backend.
	Logout(ctx context.Context) error

	// IsLoggedIn reports whether the current session is authenticated.
	IsLoggedIn(ctx context.Context) (bool, error)

	// Self returns the logged-in account's own identity.
	Self(ctx context.Context) (UserID, error)

	// DumpSession serializes resumable session state for persistence. The blob
	// is opaque to the bridge core and fed back via Config.Session.
	DumpSession() ([]byte, error)

	// --- messaging (outbound) ---

	// SendMessage delivers a message to a chat and returns the backend message
	// id (for echo/redaction tracking). Attachments are pre-uploaded by the
	// core where the protocol requires it; see UploadMedia.
	SendMessage(ctx context.Context, chat ChatID, msg OutgoingMessage) (MessageID, error)

	// RedactMessage recalls/deletes a previously sent message.
	RedactMessage(ctx context.Context, chat ChatID, id MessageID) error

	// --- directory / metadata ---

	// GetContact resolves a single user's profile.
	GetContact(ctx context.Context, id UserID) (Contact, error)

	// ListContacts returns the account's contact list.
	ListContacts(ctx context.Context) ([]Contact, error)

	// GetChat returns conversation metadata (group info, members).
	GetChat(ctx context.Context, id ChatID) (Chat, error)
}

// WebhookReceiver is an optional backend capability for push-based delivery.
// The bridge calls this from an HTTP handler when the backend posts new events.
type WebhookReceiver interface {
	HandleWebhook(ctx context.Context, payload []byte) error
}

// MessageID is the backend-native id of a delivered message.
type MessageID string

// MessageSendResult retains backend-qualified aliases from the actual send
// response. An unqualified ID alone does not authorize a native quote.
type MessageSendResult struct {
	ID    MessageID
	Keys  []string
	Quote *SentMessageQuote // Backend-normalized preview of what was actually sent.
}

// SentMessageQuote can differ from the user's input (e.g. a text reply is sent
// as an app message). Text is a bounded quote preview, not a raw secret-bearing
// media payload or the Matrix Markdown representation.
type SentMessageQuote struct {
	Type MessageType
	Text string // Backend-native reference content.
	Body string // Readable fallback without protocol markup.
}

// QualifiedMessageSender returns provenance-qualified IDs from a successful send.
// Implementations must not retry an uncertain send (which can duplicate it).
type QualifiedMessageSender interface {
	SendMessageWithResult(context.Context, ChatID, OutgoingMessage) (MessageSendResult, error)
}

// LoginFlow describes one interactive login method, mirroring
// bridgev2.LoginFlow. Presented to the user to choose from.
type LoginFlow struct {
	ID          string // e.g. "qr", "phone"
	Name        string // human label, e.g. "Scan QR code"
	Description string
}

// LoginProcess is a step machine, mirroring bridgev2.LoginProcess. The bridge
// core drives it by calling Start once, then Submit* in response to each
// returned step, until a step has Type==LoginComplete or LoginError.
//
// This is exactly how mautrix-telegram (bridgev2) drives login from the
// management room: the bot renders each step and feeds the user's reply back.
type LoginProcess interface {
	// Start returns the first step.
	Start(ctx context.Context) (*LoginStep, error)

	// SubmitUserInput advances a LoginInputs step with field values keyed by
	// LoginInputField.ID (e.g. {"code": "123456"}).
	SubmitUserInput(ctx context.Context, input map[string]string) (*LoginStep, error)

	// Wait advances a LoginDisplayAndWait step: it blocks until the backend
	// makes progress (e.g. the QR is scanned and confirmed) and returns the
	// next step. Equivalent to bridgev2's display-and-wait polling.
	Wait(ctx context.Context) (*LoginStep, error)

	// Cancel aborts the login and releases resources.
	Cancel()
}

// LoginStepType discriminates a LoginStep, matching bridgev2 step types.
type LoginStepType string

const (
	LoginInputs         LoginStepType = "user_input"       // ask the user for fields
	LoginDisplayAndWait LoginStepType = "display_and_wait" // show something (QR) and wait
	LoginComplete       LoginStepType = "complete"         // success; Self valid
	LoginError          LoginStepType = "error"            // failed; Err set
)

// LoginStep is one node of the login state machine, mirroring bridgev2.LoginStep.
type LoginStep struct {
	Type         LoginStepType
	StepID       string // stable id for this step, e.g. "wechat.qr"
	Instructions string // human-readable text shown to the user

	// Inputs is set when Type==LoginInputs.
	Inputs []LoginInputField
	// Display is set when Type==LoginDisplayAndWait (e.g. a QR code).
	Display *LoginDisplay
	// Self is set when Type==LoginComplete.
	Self UserID
	// Err is set when Type==LoginError.
	Err error
}

// LoginInputField mirrors bridgev2.LoginInputDataField.
type LoginInputField struct {
	ID          string // key used in SubmitUserInput
	Name        string // label
	Description string
	Type        string // "number", "text", "password", "phone_number", ...
}

// LoginDisplay is content to render and wait on (a QR code or a URL/token).
type LoginDisplay struct {
	Type string // "qr" | "code" | "url"
	Data string // QR payload / image data-URL / code / url
}

// ---------------------------------------------------------------------------
// Events (inbound)
// ---------------------------------------------------------------------------

// EventKind discriminates the Event union.
type EventKind int

const (
	EventMessage       EventKind = iota // a new inbound message (Message set)
	EventRedaction                      // a message was recalled (Redaction set)
	EventReceipt                        // read/delivery receipt (Receipt set)
	EventContactUpdate                  // contact/group metadata changed
	EventConnected                      // backend session became healthy
	EventDisconnected                   // backend session dropped (Error maybe set)
)

// Event is a tagged union of everything pushed up from the backend.
type Event struct {
	Kind      EventKind
	Message   *InboundMessage
	Redaction *Redaction
	Receipt   *Receipt
	Contact   *Contact
	Error     error
}

// ---------------------------------------------------------------------------
// Message model (neutral)
// ---------------------------------------------------------------------------

// MessageType is a neutral content classification.
type MessageType int

const (
	MsgText MessageType = iota
	MsgImage
	MsgVideo
	MsgAudio
	MsgFile
	MsgLocation
	MsgContactCard
	MsgLink
	MsgEmoji
	MsgApp // catch-all rich app message
)

// OutgoingMessage is a message the bridge wants to send.
type OutgoingMessage struct {
	Reply    *Reply // Verified local reference for a native quote, never arbitrary event metadata.
	Type     MessageType
	Text     string   // body for text / caption; raw appmsg XML for outbound MsgApp
	Mentions []UserID // @-mentions
	Media    *Media   // for image/video/audio/file
	Extra    map[string]any
}

// InboundMessage is a message received from the backend.
type InboundMessage struct {
	Account UserID // Optional backend account provenance, not the message sender.
	ID      MessageID
	Chat    ChatID
	Sender  UserID
	Type    MessageType
	Text    string
	Media   *Media
	// FetchMedia resolves a file after callback acknowledgement. It captures
	// private backend metadata, never the callback request's context. The core
	// supplies the connector lifetime and a bounded operation timeout.
	FetchMedia func(context.Context) (*Media, error) `json:"-"`
	Location   *Location                             // Optional coordinates: nil is distinct from a valid (0, 0).
	Timestamp  time.Time
	Extra      map[string]any
	Solitaire  *SolitaireSource  // Exact source version for opt-in reply participation.
	Reply      *Reply            // Optional original message and readable quote.
	Record     *ForwardedRecord  // Quoted history, not new messages from its named authors.
	Quote      *SentMessageQuote // Optional safe backend preview for future outbound references.
	// DeduplicationKeys are backend-defined, type-qualified ID aliases, stable
	// across send responses and inbound delivery, scoped to an account/chat.
	// They are internal delivery hints, not client-supplied source metadata.
	DeduplicationKeys []string
	// DeliveryOrder is assigned by the connector before concurrent dispatch.
	// It lets a sink correlate adjacent events without trusting completion order.
	DeliveryOrder uint64 `json:"-"`
}

// Media is a binary attachment. Backends fill either Data (already fetched) or
// URL+a fetch hook is implied via the implementation.
type Media struct {
	MimeType string
	FileName string
	Data     []byte // inline bytes (may be nil if URL is set)
	URL      string // remote/CDN URL the implementation can resolve
	Size     int64
}

// Reply describes an incoming quoted message. Target may be unavailable while
// the original sender and text remain useful for a readable fallback.
type Reply struct {
	Type       MessageType // Neutral type of the original message.
	TargetKey  string      // Backend-qualified ID, e.g. new:123; never an untyped fallback.
	Target     MessageID
	Sender     UserID
	SenderName string
	Text       string
}

// Redaction reports a recalled message.
type Redaction struct {
	Account   UserID
	TargetKey string // Optional backend-qualified ID namespace.
	Chat      ChatID
	Target    MessageID
	By        UserID
	Text      string
	Timestamp time.Time
}

// Receipt reports read state.
type Receipt struct {
	Chat      ChatID
	By        UserID
	Upto      MessageID
	Timestamp time.Time
}

// ---------------------------------------------------------------------------
// Directory model (neutral)
// ---------------------------------------------------------------------------

// Contact is a user profile.
type Contact struct {
	ID        UserID
	Name      string
	Remark    string // user-set alias
	AvatarURL string
	Extra     map[string]any
}

// Chat is conversation metadata.
type Chat struct {
	ID      ChatID
	Name    string
	IsGroup bool
	Members []UserID
	Avatar  string
	Extra   map[string]any
}

// (login step types are defined above as the bridgev2-aligned state machine)
