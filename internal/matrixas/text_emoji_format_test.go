package matrixas

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

const smileInlineImage = `<img data-mx-emoticon src="mxc://example.org/smile" alt=":wechat_smile:" title=":wechat_smile:" height="32">`

func TestInlineWechatEmojiExactHTMLAndWhitespace(t *testing.T) {
	images := map[string]id.ContentURIString{"wechat_smile": "mxc://example.org/smile"}
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "single interior spaces", body: "a :wechat_smile: b", want: "a " + smileInlineImage + " b"},
		{name: "leading spaces", body: "  :wechat_smile:", want: "&nbsp;&nbsp;" + smileInlineImage},
		{name: "trailing spaces", body: ":wechat_smile:  ", want: smileInlineImage + "&nbsp;&nbsp;"},
		{name: "repeated interior spaces", body: "a   :wechat_smile:  b", want: "a&nbsp;&nbsp;&nbsp;" + smileInlineImage + "&nbsp;&nbsp;b"},
		{name: "lone carriage return", body: "a\r:wechat_smile:", want: "a<br>" + smileInlineImage},
		{name: "line feed", body: "a\n:wechat_smile:", want: "a<br>" + smileInlineImage},
		{name: "CRLF is one break", body: "a\r\n:wechat_smile:", want: "a<br>" + smileInlineImage},
		{name: "tab is four NBSP", body: "\t:wechat_smile:", want: strings.Repeat("&nbsp;", 4) + smileInlineImage},
		{
			name: "mixed CRLF tab and space runs",
			body: "\r\n\t  :wechat_smile: \rX\n",
			want: "<br>" + strings.Repeat("&nbsp;", 6) + smileInlineImage + "&nbsp;<br>X<br>",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, used := formatInlineWechatEmoji(tt.body, images)
			if !used {
				t.Fatal("formatInlineWechatEmoji used = false, want true")
			}
			if got != tt.want {
				t.Fatalf("formatted HTML mismatch:\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}

func TestInlineWechatEmojiEscapesAttributes(t *testing.T) {
	got, used := formatInlineWechatEmoji(":wechat_smile:", map[string]id.ContentURIString{
		"wechat_smile": "mxc://example.org/media&part",
	})
	if !used {
		t.Fatal("formatInlineWechatEmoji used = false, want true")
	}
	want := `<img data-mx-emoticon src="mxc://example.org/media&amp;part" alt=":wechat_smile:" title=":wechat_smile:" height="32">`
	if got != want {
		t.Fatalf("formatted attribute escaping mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestInlineWechatEmojiEscapesTextAndKeepsUnresolvedShortcodesPlain(t *testing.T) {
	body := `<b>& ":wechat_smile:" :wechat_heart: :wechat_not_registered:`
	images := map[string]id.ContentURIString{
		"wechat_smile": "mxc://example.org/smile",
		"wechat_heart": "not-an-mxc",
	}
	got, used := formatInlineWechatEmoji(body, images)
	if !used {
		t.Fatal("formatInlineWechatEmoji used = false, want true")
	}
	want := `&lt;b&gt;&amp; &#34;` + smileInlineImage + `&#34; :wechat_heart: :wechat_not_registered:`
	if got != want {
		t.Fatalf("formatted HTML mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestInlineWechatEmojiMakesOnlyFormattedTextValidUTF8(t *testing.T) {
	original := append([]byte("before "), 0xff)
	original = append(original, []byte(":wechat_smile:")...)
	original = append(original, 0xfe)
	original = append(original, []byte(" after")...)
	body := string(original)

	got, used := formatInlineWechatEmoji(body, map[string]id.ContentURIString{
		"wechat_smile": "mxc://example.org/smile",
	})
	if !used {
		t.Fatal("formatInlineWechatEmoji used = false, want true")
	}
	want := "before \ufffd" + smileInlineImage + "\ufffd after"
	if got != want {
		t.Fatalf("formatted HTML mismatch:\n got: %q\nwant: %q", got, want)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("formatted HTML is not valid UTF-8: %q", got)
	}
	if !bytes.Equal([]byte(body), original) {
		t.Fatalf("plain body bytes changed: got %v, want %v", []byte(body), original)
	}
}

type inlineEmojiRawState struct {
	mu          sync.Mutex
	states      map[rawPackStateKey]json.RawMessage
	loadErrors  map[rawPackStateKey]error
	calls       []rawPackStateKey
	allDeadline bool
}

func newInlineEmojiRawState() *inlineEmojiRawState {
	return &inlineEmojiRawState{
		states:      make(map[rawPackStateKey]json.RawMessage),
		loadErrors:  make(map[rawPackStateKey]error),
		allDeadline: true,
	}
}

func (state *inlineEmojiRawState) Load(ctx context.Context, room id.RoomID, eventType event.Type, stateKey string) (json.RawMessage, error) {
	key := rawPackStateKey{room: room, eventType: eventType.Type, stateKey: stateKey}
	state.mu.Lock()
	defer state.mu.Unlock()
	state.calls = append(state.calls, key)
	if _, ok := ctx.Deadline(); !ok {
		state.allDeadline = false
	}
	if err := state.loadErrors[key]; err != nil {
		return nil, err
	}
	raw, ok := state.states[key]
	if !ok {
		return nil, errImagePackStateNotFound
	}
	return append(json.RawMessage(nil), raw...), nil
}

func (*inlineEmojiRawState) Save(context.Context, id.RoomID, event.Type, string, json.RawMessage) error {
	return errors.New("unexpected save")
}

func (state *inlineEmojiRawState) set(room id.RoomID, eventType event.Type, stateKey, raw string) {
	state.states[rawPackStateKey{room: room, eventType: eventType.Type, stateKey: stateKey}] = json.RawMessage(raw)
}

type deadlineFallbackRawState struct {
	calls []rawPackStateKey
}

func (state *deadlineFallbackRawState) Load(ctx context.Context, room id.RoomID, eventType event.Type, stateKey string) (json.RawMessage, error) {
	state.calls = append(state.calls, rawPackStateKey{room: room, eventType: eventType.Type, stateKey: stateKey})
	if _, ok := ctx.Deadline(); !ok {
		return nil, errors.New("load context has no deadline")
	}
	if eventType == event.StateImagePack {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return json.RawMessage(`{"images":{"wechat_smile":{"url":"mxc://compat.example/smile"}}}`), nil
}

func (*deadlineFallbackRawState) Save(context.Context, id.RoomID, event.Type, string, json.RawMessage) error {
	return errors.New("unexpected save")
}

func TestBuiltinEmojiResolutionGivesCompatibilityLoadFreshDeadline(t *testing.T) {
	room := id.RoomID("!portal:example.org")
	state := &deadlineFallbackRawState{}
	s := &Server{rawImagePackState: state, matrixOperationTimeout: 5 * time.Millisecond}

	got := s.resolvePortalBuiltinEmoji(context.Background(), room)
	if got["wechat_smile"] != "mxc://compat.example/smile" {
		t.Fatalf("resolved smile = %q, want compatibility image after stable timeout", got["wechat_smile"])
	}
	wantCalls := []rawPackStateKey{
		{room: room, eventType: event.StateImagePack.Type, stateKey: builtinEmojiStateKey},
		{room: room, eventType: event.StateUnstableImagePack.Type, stateKey: ""},
	}
	if len(state.calls) != len(wantCalls) || state.calls[0] != wantCalls[0] || state.calls[1] != wantCalls[1] {
		t.Fatalf("pack reads = %#v, want exactly %#v", state.calls, wantCalls)
	}
}

func TestBuiltinEmojiResolutionPrefersStableAndFallsBackPerShortcode(t *testing.T) {
	room := id.RoomID("!portal:example.org")
	state := newInlineEmojiRawState()
	state.set(room, event.StateImagePack, builtinEmojiStateKey, `{
		"images": {
			"wechat_smile": {"url": "mxc://stable.example/smile"},
			"wechat_heart": null,
			"wechat_let_down": {"url": ""},
			"wechat_awesome": {"url": "https://stable.example/invalid"},
			"wechat_not_registered": {"url": "mxc://stable.example/unknown"}
		}
	}`)
	state.set(room, event.StateUnstableImagePack, "", `{
		"images": {
			"wechat_smile": {"url": "mxc://compat.example/smile"},
			"wechat_heart": {"url": "mxc://compat.example/heart"},
			"wechat_let_down": {"url": "mxc://compat.example/let-down"},
			"wechat_awesome": {"url": "mxc://compat.example/awesome"},
			"wechat_not_registered": {"url": "mxc://compat.example/unknown"}
		}
	}`)
	s := &Server{rawImagePackState: state, matrixOperationTimeout: time.Second}

	got := s.resolvePortalBuiltinEmoji(context.Background(), room)
	want := map[string]id.ContentURIString{
		"wechat_smile":    "mxc://stable.example/smile",
		"wechat_heart":    "mxc://compat.example/heart",
		"wechat_let_down": "mxc://compat.example/let-down",
		"wechat_awesome":  "mxc://compat.example/awesome",
	}
	if len(got) != len(want) {
		t.Fatalf("resolved images = %#v, want %#v", got, want)
	}
	for shortcode, uri := range want {
		if got[shortcode] != uri {
			t.Fatalf("resolved %s = %q, want %q", shortcode, got[shortcode], uri)
		}
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	wantCalls := []rawPackStateKey{
		{room: room, eventType: event.StateImagePack.Type, stateKey: builtinEmojiStateKey},
		{room: room, eventType: event.StateUnstableImagePack.Type, stateKey: ""},
	}
	if len(state.calls) != len(wantCalls) || state.calls[0] != wantCalls[0] || state.calls[1] != wantCalls[1] {
		t.Fatalf("pack reads = %#v, want exactly %#v", state.calls, wantCalls)
	}
	if !state.allDeadline {
		t.Fatal("resolver pack reads did not use a bounded Matrix operation context")
	}
}

func TestBuiltinEmojiResolutionSourceErrorsAndMalformedEventsFallbackIndependently(t *testing.T) {
	room := id.RoomID("!portal:example.org")
	stableKey := rawPackStateKey{room: room, eventType: event.StateImagePack.Type, stateKey: builtinEmojiStateKey}
	compatKey := rawPackStateKey{room: room, eventType: event.StateUnstableImagePack.Type, stateKey: ""}
	tests := []struct {
		name        string
		stable      string
		compat      string
		stableError error
		compatError error
		want        id.ContentURIString
	}{
		{name: "stable generic load error", stableError: context.DeadlineExceeded, compat: `{"images":{"wechat_smile":{"url":"mxc://compat.example/smile"}}}`, want: "mxc://compat.example/smile"},
		{name: "compat generic load error", stable: `{"images":{"wechat_smile":{"url":"mxc://stable.example/smile"}}}`, compatError: errors.New("compat failed"), want: "mxc://stable.example/smile"},
		{name: "malformed stable", stable: `{broken`, compat: `{"images":{"wechat_smile":{"url":"mxc://compat.example/smile"}}}`, want: "mxc://compat.example/smile"},
		{name: "malformed compat", stable: `{"images":{"wechat_smile":{"url":"mxc://stable.example/smile"}}}`, compat: `{broken`, want: "mxc://stable.example/smile"},
		{name: "both malformed", stable: `{broken`, compat: `[also broken`, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := newInlineEmojiRawState()
			if tt.stable != "" {
				state.set(room, event.StateImagePack, builtinEmojiStateKey, tt.stable)
			}
			if tt.compat != "" {
				state.set(room, event.StateUnstableImagePack, "", tt.compat)
			}
			state.loadErrors[stableKey] = tt.stableError
			state.loadErrors[compatKey] = tt.compatError
			got := (&Server{rawImagePackState: state}).resolvePortalBuiltinEmoji(context.Background(), room)
			if got["wechat_smile"] != tt.want {
				t.Fatalf("resolved smile = %q, want %q (all=%#v)", got["wechat_smile"], tt.want, got)
			}
			state.mu.Lock()
			calls := append([]rawPackStateKey(nil), state.calls...)
			state.mu.Unlock()
			if len(calls) != 2 || calls[0] != stableKey || calls[1] != compatKey {
				t.Fatalf("pack reads = %#v, want stable and compat exactly once", calls)
			}
		})
	}
}

func TestBuiltinEmojiResolutionStrictlyRejectsInvalidMXC(t *testing.T) {
	room := id.RoomID("!portal:example.org")
	invalid := []string{
		" mxc://example.org/media",
		"mxc://example.org/media ",
		"https://example.org/media",
		"mxc://example.org/",
		"mxc://example.org/media?query=value",
		"mxc://example.org/media#fragment",
		"mxc://user@example.org/media",
	}
	for _, uri := range invalid {
		t.Run(uri, func(t *testing.T) {
			state := newInlineEmojiRawState()
			state.set(room, event.StateImagePack, builtinEmojiStateKey, `{"images":{"wechat_smile":{"url":`+string(mustJSON(t, uri))+`}}}`)
			got := (&Server{rawImagePackState: state}).resolvePortalBuiltinEmoji(context.Background(), room)
			if _, ok := got["wechat_smile"]; ok {
				t.Fatalf("invalid MXC %q resolved as %q", uri, got["wechat_smile"])
			}
		})
	}
}

func mustJSON(t *testing.T, value string) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type inlineMessageRecorder struct {
	mu       sync.Mutex
	contents []event.MessageEventContent
}

func (recorder *inlineMessageRecorder) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if request.Method == http.MethodPut && strings.Contains(request.URL.Path, "/send/m.room.message/") {
		var content event.MessageEventContent
		if err := json.NewDecoder(request.Body).Decode(&content); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		recorder.mu.Lock()
		recorder.contents = append(recorder.contents, content)
		recorder.mu.Unlock()
	}
	_, _ = w.Write([]byte(`{"event_id":"$inline"}`))
}

func (recorder *inlineMessageRecorder) last(t *testing.T) event.MessageEventContent {
	t.Helper()
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if len(recorder.contents) == 0 {
		t.Fatal("no Matrix message was sent")
	}
	return recorder.contents[len(recorder.contents)-1]
}

func TestInlineWechatEmojiBridgeRemoteTextIntegration(t *testing.T) {
	room := id.RoomID("!portal:example.org")
	state := newInlineEmojiRawState()
	state.set(room, event.StateImagePack, builtinEmojiStateKey, `{"images":{"wechat_smile":{"url":"mxc://example.org/smile"}}}`)
	state.set(room, event.StateUnstableImagePack, "", `{"images":{}}`)
	recorder := &inlineMessageRecorder{}
	homeserver := httptest.NewServer(recorder)
	t.Cleanup(homeserver.Close)
	s := &Server{
		as:                testAppservice(t, homeserver.URL),
		api:               portalCreationAPI{},
		rawImagePackState: state,
		messageEvents:     make(map[string]id.EventID),
	}

	s.bridgeRemoteToPortal(context.Background(), portalRecord{RoomID: room}, &remoteapi.InboundMessage{
		ID:        "text-1",
		Chat:      "wxid_self",
		Sender:    "wxid_self",
		Type:      remoteapi.MsgText,
		Text:      "[Smile] and [Heart]",
		Timestamp: time.Unix(1, 0),
	})

	got := recorder.last(t)
	if got.MsgType != event.MsgText {
		t.Fatalf("msgtype = %q, want %q", got.MsgType, event.MsgText)
	}
	if got.Body != ":wechat_smile: and :wechat_heart:" {
		t.Fatalf("plain body = %q, want canonical shortcodes", got.Body)
	}
	if got.Format != event.FormatHTML {
		t.Fatalf("format = %q, want %q", got.Format, event.FormatHTML)
	}
	wantHTML := smileInlineImage + " and :wechat_heart:"
	if got.FormattedBody != wantHTML {
		t.Fatalf("formatted body = %q, want %q", got.FormattedBody, wantHTML)
	}
}

func TestInlineWechatEmojiBridgeReadsPacksOnlyForKnownCanonicalShortcodes(t *testing.T) {
	room := id.RoomID("!portal:example.org")
	state := newInlineEmojiRawState()
	state.set(room, event.StateImagePack, builtinEmojiStateKey, `{"images":{"wechat_smile":{"url":"mxc://example.org/smile"}}}`)
	state.set(room, event.StateUnstableImagePack, "", `{"images":{}}`)
	recorder := &inlineMessageRecorder{}
	homeserver := httptest.NewServer(recorder)
	t.Cleanup(homeserver.Close)
	s := &Server{
		as:                testAppservice(t, homeserver.URL),
		api:               portalCreationAPI{},
		rawImagePackState: state,
		messageEvents:     make(map[string]id.EventID),
	}

	s.bridgeRemoteToPortal(context.Background(), portalRecord{RoomID: room}, &remoteapi.InboundMessage{
		ID: "ordinary", Chat: "wxid_self", Sender: "wxid_self", Type: remoteapi.MsgText,
		Text: "ordinary text", Timestamp: time.Unix(1, 0),
	})
	state.mu.Lock()
	ordinaryCalls := append([]rawPackStateKey(nil), state.calls...)
	state.mu.Unlock()
	if len(ordinaryCalls) != 0 {
		t.Fatalf("ordinary text read image packs: %#v", ordinaryCalls)
	}

	s.bridgeRemoteToPortal(context.Background(), portalRecord{RoomID: room}, &remoteapi.InboundMessage{
		ID: "unknown", Chat: "wxid_self", Sender: "wxid_self", Type: remoteapi.MsgText,
		Text: "unknown :wechat_not_registered: token", Timestamp: time.Unix(2, 0),
	})
	state.mu.Lock()
	unknownCalls := append([]rawPackStateKey(nil), state.calls...)
	state.mu.Unlock()
	if len(unknownCalls) != 0 {
		t.Fatalf("unknown shortcode text read image packs: %#v", unknownCalls)
	}

	s.bridgeRemoteToPortal(context.Background(), portalRecord{RoomID: room}, &remoteapi.InboundMessage{
		ID: "known", Chat: "wxid_self", Sender: "wxid_self", Type: remoteapi.MsgText,
		Text: "[Smile]", Timestamp: time.Unix(3, 0),
	})
	state.mu.Lock()
	knownCalls := append([]rawPackStateKey(nil), state.calls...)
	state.mu.Unlock()
	if len(knownCalls) != 2 {
		t.Fatalf("known shortcode pack reads = %#v, want exactly stable and compatibility reads", knownCalls)
	}
	if knownCalls[0].eventType != event.StateImagePack.Type || knownCalls[1].eventType != event.StateUnstableImagePack.Type {
		t.Fatalf("known shortcode pack reads = %#v, want stable then compatibility", knownCalls)
	}
}

func TestInlineWechatEmojiBridgeOmitsFormattingWhenUnresolvedAndForNonText(t *testing.T) {
	room := id.RoomID("!portal:example.org")
	state := newInlineEmojiRawState()
	state.set(room, event.StateImagePack, builtinEmojiStateKey, `{"images":{}}`)
	state.set(room, event.StateUnstableImagePack, "", `{"images":{}}`)
	recorder := &inlineMessageRecorder{}
	homeserver := httptest.NewServer(recorder)
	t.Cleanup(homeserver.Close)
	s := &Server{
		as:                testAppservice(t, homeserver.URL),
		api:               portalCreationAPI{},
		rawImagePackState: state,
		messageEvents:     make(map[string]id.EventID),
	}

	s.bridgeRemoteToPortal(context.Background(), portalRecord{RoomID: room}, &remoteapi.InboundMessage{
		ID: "text-unresolved", Chat: "wxid_self", Sender: "wxid_self", Type: remoteapi.MsgText, Text: "[Smile]", Timestamp: time.Unix(1, 0),
	})
	unresolved := recorder.last(t)
	if unresolved.Body != ":wechat_smile:" || unresolved.Format != "" || unresolved.FormattedBody != "" {
		t.Fatalf("unresolved text content = %#v, want plain canonical fallback", unresolved)
	}

	state.mu.Lock()
	state.calls = nil
	state.mu.Unlock()
	s.bridgeRemoteToPortal(context.Background(), portalRecord{RoomID: room}, &remoteapi.InboundMessage{
		ID: "app-message", Chat: "wxid_self", Sender: "wxid_self", Type: remoteapi.MsgApp, Text: "[Smile]", Timestamp: time.Unix(2, 0),
	})
	nonText := recorder.last(t)
	if nonText.Body != "[Smile]" || nonText.Format != "" || nonText.FormattedBody != "" {
		t.Fatalf("non-text content changed by inline formatter: %#v", nonText)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.calls) != 0 {
		t.Fatalf("non-text message read image packs: %#v", state.calls)
	}
}

func TestInlineWechatEmojiReturnsNoOutputWhenNothingResolves(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		images map[string]id.ContentURIString
	}{
		{name: "ordinary text", body: "hello", images: map[string]id.ContentURIString{"wechat_smile": "mxc://example.org/smile"}},
		{name: "unknown shortcode", body: ":wechat_not_registered:", images: map[string]id.ContentURIString{"wechat_not_registered": "mxc://example.org/unknown"}},
		{name: "missing image", body: ":wechat_smile:", images: nil},
		{name: "invalid image", body: ":wechat_smile:", images: map[string]id.ContentURIString{"wechat_smile": `mxc://example.org/media\" onerror=\"alert(1)`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, used := formatInlineWechatEmoji(tt.body, tt.images)
			if used || got != "" {
				t.Fatalf("formatInlineWechatEmoji() = (%q, %v), want (empty, false)", got, used)
			}
		})
	}
}
