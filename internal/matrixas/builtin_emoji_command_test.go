package matrixas

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"maunium.net/go/mautrix/id"

	"github.com/leat/mautrix-go-wechat/internal/config"
	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

type builtinEmojiCommandTestAPI struct {
	remoteapi.PuppetAPI
	loggedIn bool
	err      error
	calls    atomic.Int32
	contexts chan context.Context
}

func (api *builtinEmojiCommandTestAPI) IsLoggedIn(ctx context.Context) (bool, error) {
	api.calls.Add(1)
	if api.contexts != nil {
		api.contexts <- ctx
	}
	return api.loggedIn, api.err
}

type builtinEmojiCommandTestRunner struct {
	started        chan context.Context
	release        <-chan struct{}
	waitForContext bool
	result         builtinEmojiImportResult
	panicValue     any
	progress       builtinEmojiImportProgress
	notice         func(builtinEmojiImportProgress)
}

func (runner *builtinEmojiCommandTestRunner) setProgressNotice(notice func(builtinEmojiImportProgress)) {
	runner.notice = notice
}

func (runner *builtinEmojiCommandTestRunner) Run(ctx context.Context) builtinEmojiImportResult {
	if runner.started != nil {
		runner.started <- ctx
	}
	if runner.notice != nil && runner.progress.Kind != "" {
		runner.notice(runner.progress)
	}
	if runner.waitForContext {
		<-ctx.Done()
	} else if runner.release != nil {
		select {
		case <-runner.release:
		case <-ctx.Done():
		}
	}
	if runner.panicValue != nil {
		panic(runner.panicValue)
	}
	return runner.result
}

type builtinEmojiCommandNotice struct {
	Room id.RoomID
	Body string
}

type builtinEmojiCommandNoticeRecorder struct {
	mu      sync.Mutex
	notices []builtinEmojiCommandNotice
	updates chan struct{}
}

func newBuiltinEmojiCommandNoticeRecorder() *builtinEmojiCommandNoticeRecorder {
	return &builtinEmojiCommandNoticeRecorder{updates: make(chan struct{}, 128)}
}

func (recorder *builtinEmojiCommandNoticeRecorder) record(_ context.Context, room id.RoomID, body string) {
	recorder.mu.Lock()
	recorder.notices = append(recorder.notices, builtinEmojiCommandNotice{Room: room, Body: body})
	recorder.mu.Unlock()
	select {
	case recorder.updates <- struct{}{}:
	default:
	}
}

func (recorder *builtinEmojiCommandNoticeRecorder) snapshot() []builtinEmojiCommandNotice {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]builtinEmojiCommandNotice(nil), recorder.notices...)
}

func (recorder *builtinEmojiCommandNoticeRecorder) waitFor(t *testing.T, room id.RoomID, text string) builtinEmojiCommandNotice {
	t.Helper()
	for {
		for _, notice := range recorder.snapshot() {
			if notice.Room == room && strings.Contains(notice.Body, text) {
				return notice
			}
		}
		select {
		case <-recorder.updates:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for notice in %s containing %q; got %#v", room, text, recorder.snapshot())
		}
	}
}

func TestBuiltinEmojiCommandParsingAcceptsOnlyExactNormalizedAcknowledgment(t *testing.T) {
	tests := []struct {
		input        string
		recognized   bool
		acknowledged bool
	}{
		{input: "import wechat --ack-no-license", recognized: true, acknowledged: true},
		{input: "  import\twechat\n--ack-no-license  ", recognized: true, acknowledged: true},
		{input: "导入 微信 --ack-no-license", recognized: true, acknowledged: true},
		{input: "import wechat", recognized: true},
		{input: "导入 微信", recognized: true},
		{input: "import wechat --ack-license", recognized: true},
		{input: "import wechat --ACK-NO-LICENSE", recognized: true},
		{input: "import wechat --ack-no-license extra", recognized: true},
		{input: "import wechat extra --ack-no-license", recognized: true},
		{input: "import WeChat --ack-no-license"},
		{input: "Import wechat --ack-no-license"},
		{input: "导入 wechat --ack-no-license"},
		{input: "import 微信 --ack-no-license"},
		{input: "import telegram --ack-no-license"},
		{input: "import"},
		{input: ""},
	}
	for _, test := range tests {
		t.Run(strings.ReplaceAll(test.input, " ", "_"), func(t *testing.T) {
			recognized, acknowledged := parseBuiltinEmojiImportCommand(test.input)
			if recognized != test.recognized || acknowledged != test.acknowledged {
				t.Fatalf("parseBuiltinEmojiImportCommand(%q) = (%v, %v), want (%v, %v)", test.input, recognized, acknowledged, test.recognized, test.acknowledged)
			}
		})
	}
}

func TestImportPermissionRequiresGeneralGateThenImmutablePersonalOrExactAdmin(t *testing.T) {
	sender := id.UserID("@alice:example.org")
	tests := []struct {
		name        string
		permissions map[string]string
		personal    string
		owner       id.UserID
		want        bool
	}{
		{name: "empty permissions disabled"},
		{name: "configured personal", personal: sender.String(), want: true},
		{name: "different personal", personal: "@other:example.org"},
		{name: "exact user admin", permissions: map[string]string{sender.String(): "admin"}, want: true},
		{name: "exact domain admin", permissions: map[string]string{"example.org": "admin"}, want: true},
		{name: "admin is case sensitive", permissions: map[string]string{sender.String(): "Admin"}},
		{name: "exact relay overrides domain admin", permissions: map[string]string{sender.String(): "relay", "example.org": "admin"}},
		{name: "exact user denial overrides domain admin", permissions: map[string]string{sender.String(): "user", "example.org": "admin"}},
		{name: "exact empty denial overrides domain admin", permissions: map[string]string{sender.String(): "", "example.org": "admin"}},
		{name: "domain relay", permissions: map[string]string{"example.org": "relay"}},
		{name: "domain user is insufficient", permissions: map[string]string{"example.org": "user"}},
		{name: "exact admin overrides domain relay", permissions: map[string]string{sender.String(): "admin", "example.org": "relay"}, want: true},
		{name: "personal still passes non-relay general level", permissions: map[string]string{sender.String(): "user"}, personal: sender.String(), want: true},
		{name: "general relay denies personal", permissions: map[string]string{sender.String(): "relay"}, personal: sender.String()},
		{name: "mutable portal owner is not authority", owner: sender},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := &Server{
				cfg: &config.Config{Bridge: config.Bridge{
					Permissions:           test.permissions,
					PersonalStickerUserID: test.personal,
				}},
				portals: &portalStore{data: portalState{Owner: test.owner}},
			}
			if got := s.allowedBuiltinEmojiImport(sender); got != test.want {
				t.Fatalf("allowedBuiltinEmojiImport() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestImportPermissionRejectsMalformedSender(t *testing.T) {
	s := &Server{cfg: &config.Config{Bridge: config.Bridge{Permissions: map[string]string{"example.org": "admin"}}}}
	if s.allowedBuiltinEmojiImport("not-a-matrix-user") {
		t.Fatal("malformed sender was authorized by domain permission")
	}
}

func TestBuiltinEmojiCommandUnauthorizedAndUnacknowledgedDoNoWorkOrReset(t *testing.T) {
	room := id.RoomID("!management:example.org")
	sender := id.UserID("@alice:example.org")
	for _, test := range []struct {
		name        string
		permissions map[string]string
		rest        string
		wantNotice  string
	}{
		{name: "unauthorized exact command", rest: "import wechat --ack-no-license", wantNotice: "personal_sticker_user_id"},
		{name: "general denial checked in helper", permissions: map[string]string{sender.String(): "relay", "example.org": "admin"}, rest: "import wechat --ack-no-license", wantNotice: "personal_sticker_user_id"},
		{name: "missing acknowledgment", permissions: map[string]string{sender.String(): "admin"}, rest: "import wechat", wantNotice: builtinEmojiSourceCommit},
		{name: "changed acknowledgment", permissions: map[string]string{sender.String(): "admin"}, rest: "import wechat --ack-license", wantNotice: "no explicit license"},
		{name: "extra argument", permissions: map[string]string{sender.String(): "admin"}, rest: "import wechat --ack-no-license extra", wantNotice: "copy"},
		{name: "unknown source", permissions: map[string]string{sender.String(): "admin"}, rest: "import telegram --ack-no-license", wantNotice: "unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := newBuiltinEmojiCommandNoticeRecorder()
			store := commandTestBuiltinEmojiStore(t)
			var persists atomic.Int32
			store.persist = func(string, []byte, os.FileMode) error {
				persists.Add(1)
				return nil
			}
			var factories atomic.Int32
			s := &Server{
				cfg:                &config.Config{Bridge: config.Bridge{Permissions: test.permissions}},
				builtinEmojiStore:  store,
				builtinEmojiNotice: recorder.record,
				builtinEmojiImporterFactory: func() builtinEmojiImportRunner {
					factories.Add(1)
					return &builtinEmojiCommandTestRunner{}
				},
			}

			s.handleBuiltinEmojiImportCommand(context.Background(), room, sender, test.rest)

			if factories.Load() != 0 {
				t.Fatalf("importer factory called %d times", factories.Load())
			}
			if persists.Load() != 0 {
				t.Fatalf("canonical store reset/persist called %d times", persists.Load())
			}
			recorder.waitFor(t, room, test.wantNotice)
		})
	}
}

func TestBuiltinEmojiCommandStartNoticeCapturesTargetAndRejectsConcurrentRetarget(t *testing.T) {
	recorder := newBuiltinEmojiCommandNoticeRecorder()
	runParent, cancelRunParent := context.WithCancel(context.Background())
	defer cancelRunParent()
	api := &builtinEmojiCommandTestAPI{loggedIn: true, contexts: make(chan context.Context, 1)}
	release := make(chan struct{})
	runner := &builtinEmojiCommandTestRunner{
		started:  make(chan context.Context, 1),
		release:  release,
		progress: builtinEmojiImportProgress{Kind: builtinEmojiImportProgressBatchCommitted, BatchNumber: 1, Uploaded: 16, Persisted: 16},
		result: builtinEmojiImportResult{
			TotalConsidered: 109,
			Imported:        109,
			Persisted:       109,
			Persistence:     builtinEmojiPersistenceSuccess,
		},
	}
	s := commandTestServer(t, api, recorder)
	runtime := s.captureBuiltinEmojiRuntime(runParent)
	defer s.clearBuiltinEmojiRuntime(runtime)
	var gotParent context.Context
	var gotTimeout time.Duration
	s.builtinEmojiContextFactory = func(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
		gotParent = parent
		gotTimeout = timeout
		return context.WithCancel(parent)
	}
	var factories atomic.Int32
	s.builtinEmojiImporterFactory = func() builtinEmojiImportRunner {
		factories.Add(1)
		return runner
	}

	initiatingRoom := id.RoomID("!initiating:example.org")
	sender := id.UserID("@admin:example.org")
	s.handleBuiltinEmojiImportCommand(context.Background(), initiatingRoom, sender, "import wechat --ack-no-license")

	start := recorder.waitFor(t, initiatingRoom, "109")
	for _, want := range []string{builtinEmojiSourceRepository, builtinEmojiSourceCommit, builtinEmojiSourcePath, "no explicit license", "wxid_a", "!space-a:example.org"} {
		if !strings.Contains(start.Body, want) {
			t.Fatalf("start notice %q does not contain %q", start.Body, want)
		}
	}
	childCtx := waitBuiltinEmojiCommandContext(t, runner.started)
	if gotParent != runtime.ctx {
		t.Fatal("import timeout context was not derived from the captured Run context")
	}
	if gotTimeout != 10*time.Minute {
		t.Fatalf("import timeout = %s, want 10m", gotTimeout)
	}
	if childCtx == context.Background() || childCtx == context.TODO() {
		t.Fatal("importer received a detached context")
	}
	if got := waitBuiltinEmojiCommandContext(t, api.contexts); got != runtime.ctx {
		t.Fatal("IsLoggedIn did not use the captured Run context")
	}

	if err := s.accounts.put(savedAccount{ID: "wxid_b", SpaceRoom: "!space-b:example.org"}); err != nil {
		t.Fatal(err)
	}
	s.api = &builtinEmojiCommandTestAPI{loggedIn: false}
	concurrentRoom := id.RoomID("!concurrent:example.org")
	s.handleBuiltinEmojiImportCommand(context.Background(), concurrentRoom, sender, "导入 微信 --ack-no-license")
	recorder.waitFor(t, concurrentRoom, "already running")
	if factories.Load() != 1 {
		t.Fatalf("concurrent command built %d importers, want 1", factories.Load())
	}

	close(release)
	recorder.waitFor(t, initiatingRoom, "completed")
	progress := recorder.waitFor(t, initiatingRoom, "batch 1")
	if progress.Room != initiatingRoom {
		t.Fatalf("progress was retargeted to %s", progress.Room)
	}
	completion := recorder.waitFor(t, initiatingRoom, "wxid_a")
	if !strings.Contains(completion.Body, "!space-a:example.org") || strings.Contains(completion.Body, "wxid_b") || strings.Contains(completion.Body, "!space-b:example.org") {
		t.Fatalf("completion notice was retargeted: %q", completion.Body)
	}
	assertBuiltinEmojiCommandSlotReleased(t, s)
}

func TestImportLifecyclePreflightFailuresReleaseGlobalSlot(t *testing.T) {
	injected := errors.New("injected preflight failure")
	for _, test := range []struct {
		name      string
		configure func(*Server, *builtinEmojiCommandTestAPI)
	}{
		{name: "Run context unavailable", configure: func(s *Server, _ *builtinEmojiCommandTestAPI) { s.clearBuiltinEmojiRuntime(s.importRuntime) }},
		{name: "login check error", configure: func(_ *Server, api *builtinEmojiCommandTestAPI) { api.err = injected }},
		{name: "logged out", configure: func(_ *Server, api *builtinEmojiCommandTestAPI) { api.loggedIn = false }},
		{name: "current account unavailable", configure: func(s *Server, _ *builtinEmojiCommandTestAPI) {
			s.accounts = newAccountStore(filepath.Join(t.TempDir(), "empty-accounts.json"))
		}},
		{name: "Space unavailable", configure: func(s *Server, _ *builtinEmojiCommandTestAPI) {
			s.builtinEmojiSpaceResolver = func(context.Context, remoteapi.UserID) (id.RoomID, error) { return "", injected }
		}},
		{name: "importer factory unavailable", configure: func(s *Server, _ *builtinEmojiCommandTestAPI) {
			s.builtinEmojiImporterFactory = func() builtinEmojiImportRunner { return nil }
		}},
		{name: "context factory unavailable", configure: func(s *Server, _ *builtinEmojiCommandTestAPI) {
			s.builtinEmojiContextFactory = func(context.Context, time.Duration) (context.Context, context.CancelFunc) { return nil, nil }
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := newBuiltinEmojiCommandNoticeRecorder()
			api := &builtinEmojiCommandTestAPI{loggedIn: true}
			s := commandTestServer(t, api, recorder)
			runtime := s.captureBuiltinEmojiRuntime(context.Background())
			t.Cleanup(func() { s.clearBuiltinEmojiRuntime(runtime) })
			var factories atomic.Int32
			s.builtinEmojiImporterFactory = func() builtinEmojiImportRunner {
				factories.Add(1)
				return &builtinEmojiCommandTestRunner{}
			}
			test.configure(s, api)

			s.handleBuiltinEmojiImportCommand(context.Background(), "!management:example.org", "@admin:example.org", "import wechat --ack-no-license")

			assertBuiltinEmojiCommandSlotReleased(t, s)
			if test.name != "importer factory unavailable" && test.name != "context factory unavailable" && factories.Load() != 0 {
				t.Fatalf("preflight failure built %d importers", factories.Load())
			}
		})
	}
}

func TestBuiltinEmojiCommandAcknowledgedImportResetsOnlyInvalidStoreAndAbortsOnQuarantineFailure(t *testing.T) {
	for _, test := range []struct {
		name           string
		store          func(*testing.T) *builtinEmojiStore
		wantPersist    bool
		wantStart      bool
		failQuarantine bool
	}{
		{name: "missing store is not reset", store: func(t *testing.T) *builtinEmojiStore {
			return newBuiltinEmojiStore(filepath.Join(t.TempDir(), "missing.json"))
		}, wantStart: true},
		{name: "valid store is not reset", store: func(t *testing.T) *builtinEmojiStore {
			path := filepath.Join(t.TempDir(), "valid.json")
			data, err := marshalBuiltinEmojiStoreData(emptyBuiltinEmojiStoreData())
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			return newBuiltinEmojiStore(path)
		}, wantStart: true},
		{name: "invalid store is quarantined and reset", store: func(t *testing.T) *builtinEmojiStore {
			path := filepath.Join(t.TempDir(), "invalid.json")
			if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
			return newBuiltinEmojiStore(path)
		}, wantPersist: true, wantStart: true},
		{name: "quarantine failure aborts", store: func(t *testing.T) *builtinEmojiStore {
			path := filepath.Join(t.TempDir(), "invalid.json")
			if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
			return newBuiltinEmojiStore(path)
		}, failQuarantine: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := newBuiltinEmojiCommandNoticeRecorder()
			s := commandTestServer(t, &builtinEmojiCommandTestAPI{loggedIn: true}, recorder)
			runtime := s.captureBuiltinEmojiRuntime(context.Background())
			defer s.clearBuiltinEmojiRuntime(runtime)
			store := test.store(t)
			originalPersist := store.persist
			var persists atomic.Int32
			store.persist = func(path string, data []byte, mode os.FileMode) error {
				persists.Add(1)
				return originalPersist(path, data, mode)
			}
			if test.failQuarantine {
				store.rename = func(string, string) error { return errors.New("rename failed") }
			}
			s.builtinEmojiStore = store
			started := make(chan context.Context, 1)
			var factories atomic.Int32
			s.builtinEmojiImporterFactory = func() builtinEmojiImportRunner {
				factories.Add(1)
				return &builtinEmojiCommandTestRunner{started: started, result: builtinEmojiImportResult{TotalConsidered: 109}}
			}

			s.handleBuiltinEmojiImportCommand(context.Background(), "!management:example.org", "@admin:example.org", "import wechat --ack-no-license")

			if test.wantStart {
				_ = waitBuiltinEmojiCommandContext(t, started)
				recorder.waitFor(t, "!management:example.org", "completed")
			} else {
				if factories.Load() != 0 {
					t.Fatal("quarantine failure started importer")
				}
				recorder.waitFor(t, "!management:example.org", "quarantine")
			}
			if got := persists.Load() > 0; got != test.wantPersist {
				t.Fatalf("store reset persistence = %v, want %v", got, test.wantPersist)
			}
			if test.wantPersist && store.LoadError() != nil {
				t.Fatalf("reset store still has load error: %v", store.LoadError())
			}
			assertBuiltinEmojiCommandSlotReleased(t, s)
		})
	}
}

func TestImportLifecycleCleanupAfterSuccessErrorTimeoutCancellationAndPanic(t *testing.T) {
	for _, test := range []struct {
		name       string
		result     builtinEmojiImportResult
		waitCtx    bool
		cancelRun  bool
		panicValue any
		wantNotice string
		wantState  string
	}{
		{name: "success", result: builtinEmojiImportResult{TotalConsidered: 109, Imported: 109}, wantNotice: "completed", wantState: "finished"},
		{name: "error", result: builtinEmojiImportResult{TotalConsidered: 109, Failures: []builtinEmojiImportFailure{{RegistryIndex: 1, Code: builtinEmojiImportCodeFetchFailed}}}, wantNotice: "completed", wantState: "finished"},
		{name: "timeout", waitCtx: true, wantNotice: "completed", wantState: "canceled or timed out"},
		{name: "shutdown cancellation", waitCtx: true, cancelRun: true, wantNotice: "completed", wantState: "canceled or timed out"},
		{name: "panic", panicValue: strings.Repeat("secret-panic-value", 1000), wantNotice: "panic"},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := newBuiltinEmojiCommandNoticeRecorder()
			s := commandTestServer(t, &builtinEmojiCommandTestAPI{loggedIn: true}, recorder)
			runtime := s.captureBuiltinEmojiRuntime(context.Background())
			var cancelImport context.CancelFunc
			s.builtinEmojiContextFactory = func(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
				if timeout != 10*time.Minute {
					t.Errorf("timeout = %s", timeout)
				}
				ctx, cancel := context.WithCancel(parent)
				cancelImport = cancel
				return ctx, cancel
			}
			started := make(chan context.Context, 1)
			s.builtinEmojiImporterFactory = func() builtinEmojiImportRunner {
				return &builtinEmojiCommandTestRunner{started: started, waitForContext: test.waitCtx, result: test.result, panicValue: test.panicValue}
			}

			s.handleBuiltinEmojiImportCommand(context.Background(), "!management:example.org", "@admin:example.org", "导入 微信 --ack-no-license")
			_ = waitBuiltinEmojiCommandContext(t, started)
			if test.waitCtx {
				if test.cancelRun {
					s.clearBuiltinEmojiRuntime(runtime)
				} else {
					cancelImport()
				}
			} else {
				defer s.clearBuiltinEmojiRuntime(runtime)
			}
			notice := recorder.waitFor(t, "!management:example.org", test.wantNotice)
			if test.wantState != "" && !strings.Contains(notice.Body, "("+test.wantState+")") {
				t.Fatalf("completion state notice = %q, want %q", notice.Body, test.wantState)
			}
			if test.panicValue != nil {
				if len(notice.Body) > builtinEmojiCommandNoticeLimit {
					t.Fatalf("panic notice length = %d, limit %d", len(notice.Body), builtinEmojiCommandNoticeLimit)
				}
				if strings.Contains(notice.Body, "secret-panic-value") {
					t.Fatal("panic notice exposed panic value")
				}
			}
			assertBuiltinEmojiCommandSlotReleased(t, s)
			if test.cancelRun && s.snapshotBuiltinEmojiRuntime() != nil {
				t.Fatal("Run context was not cleared on shutdown")
			}
		})
	}
}

func TestImportLifecycleRuntimeClearCancelsDerivedImportWithoutDetachedContext(t *testing.T) {
	recorder := newBuiltinEmojiCommandNoticeRecorder()
	s := commandTestServer(t, &builtinEmojiCommandTestAPI{loggedIn: true}, recorder)
	parent, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	runtime := s.captureBuiltinEmojiRuntime(parent)
	started := make(chan context.Context, 1)
	s.builtinEmojiImporterFactory = func() builtinEmojiImportRunner {
		return &builtinEmojiCommandTestRunner{started: started, waitForContext: true}
	}

	s.handleBuiltinEmojiImportCommand(context.Background(), "!room:example.org", "@admin:example.org", "import wechat --ack-no-license")
	importCtx := waitBuiltinEmojiCommandContext(t, started)
	if importCtx.Err() != nil {
		t.Fatalf("import context canceled before shutdown: %v", importCtx.Err())
	}
	s.clearBuiltinEmojiRuntime(runtime)
	recorder.waitFor(t, "!room:example.org", "completed")
	if !errors.Is(importCtx.Err(), context.Canceled) {
		t.Fatalf("import context error = %v, want cancellation from Run context", importCtx.Err())
	}
	assertBuiltinEmojiCommandSlotReleased(t, s)
}

func TestImportLifecycleBlockingStartAndProgressNoticesAreIndependentlyBounded(t *testing.T) {
	s := commandTestServer(t, &builtinEmojiCommandTestAPI{loggedIn: true}, newBuiltinEmojiCommandNoticeRecorder())
	runtime := s.captureBuiltinEmojiRuntime(context.Background())
	defer s.clearBuiltinEmojiRuntime(runtime)

	const noticeTimeout = 25 * time.Millisecond
	var contextsMu sync.Mutex
	var noticeContexts []context.Context
	var noticeParents []context.Context
	s.builtinEmojiNoticeContextFactory = func(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
		if timeout != builtinEmojiCommandNoticeTimeout {
			t.Errorf("notice timeout = %s, want %s", timeout, builtinEmojiCommandNoticeTimeout)
		}
		contextsMu.Lock()
		noticeParents = append(noticeParents, parent)
		contextsMu.Unlock()
		return context.WithTimeout(parent, noticeTimeout)
	}
	startEntered := make(chan struct{}, 1)
	progressEntered := make(chan struct{}, 1)
	completion := make(chan struct{}, 1)
	s.builtinEmojiNotice = func(ctx context.Context, room id.RoomID, body string) {
		if room != "!initiating:example.org" {
			t.Errorf("notice room = %s, want initiating room", room)
		}
		contextsMu.Lock()
		noticeContexts = append(noticeContexts, ctx)
		contextsMu.Unlock()
		switch {
		case strings.Contains(body, "Starting"):
			startEntered <- struct{}{}
			<-ctx.Done()
		case strings.Contains(body, "progress"):
			progressEntered <- struct{}{}
			<-ctx.Done()
		case strings.Contains(body, "completed"):
			completion <- struct{}{}
		}
	}

	runnerStarted := make(chan context.Context, 1)
	s.builtinEmojiImporterFactory = func() builtinEmojiImportRunner {
		return &builtinEmojiCommandTestRunner{
			started:  runnerStarted,
			progress: builtinEmojiImportProgress{Kind: builtinEmojiImportProgressBatchCommitted, BatchNumber: 1},
		}
	}
	commandDone := make(chan struct{})
	go func() {
		s.handleBuiltinEmojiImportCommand(context.Background(), "!initiating:example.org", "@admin:example.org", "import wechat --ack-no-license")
		close(commandDone)
	}()

	select {
	case <-startEntered:
	case <-time.After(time.Second):
		t.Fatal("start notice was not attempted")
	}
	select {
	case <-runnerStarted:
		t.Fatal("importer started before the synchronous start notice returned")
	default:
	}
	select {
	case <-commandDone:
	case <-time.After(time.Second):
		t.Fatal("bounded start notice did not return at its deadline")
	}
	importCtx := waitBuiltinEmojiCommandContext(t, runnerStarted)
	select {
	case <-progressEntered:
	case <-time.After(time.Second):
		t.Fatal("progress notice was not attempted")
	}
	select {
	case <-completion:
	case <-time.After(time.Second):
		t.Fatal("bounded progress notice held the importer indefinitely")
	}
	assertBuiltinEmojiCommandSlotReleased(t, s)

	contextsMu.Lock()
	gotContexts := append([]context.Context(nil), noticeContexts...)
	gotParents := append([]context.Context(nil), noticeParents...)
	contextsMu.Unlock()
	if len(gotContexts) != 3 {
		t.Fatalf("notice context count = %d, want start, progress and completion", len(gotContexts))
	}
	if len(gotParents) != 3 {
		t.Fatalf("notice parent count = %d, want start, progress and completion", len(gotParents))
	}
	if gotParents[1] != importCtx {
		t.Fatal("progress notice was not derived from the overall bounded import context")
	}
	for index, noticeCtx := range gotContexts {
		if _, ok := noticeCtx.Deadline(); !ok {
			t.Fatalf("notice context %d has no deadline", index)
		}
		for previous := 0; previous < index; previous++ {
			if noticeCtx == gotContexts[previous] {
				t.Fatalf("notice contexts %d and %d were reused", previous, index)
			}
		}
	}
}

func TestImportLifecycleCanceledRuntimeCompletionAndPanicNoticesAreBoundedBestEffortAfterSlotRelease(t *testing.T) {
	for _, test := range []struct {
		name       string
		panicValue any
		wantBody   string
	}{
		{name: "completion", wantBody: "completed"},
		{name: "panic", panicValue: "private panic", wantBody: "failed safely"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := commandTestServer(t, &builtinEmojiCommandTestAPI{loggedIn: true}, newBuiltinEmojiCommandNoticeRecorder())
			runtime := s.captureBuiltinEmojiRuntime(context.Background())
			const noticeTimeout = 25 * time.Millisecond
			s.builtinEmojiNoticeContextFactory = func(parent context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
				return context.WithTimeout(parent, noticeTimeout)
			}
			finalEntered := make(chan context.Context, 1)
			finalReturned := make(chan struct{}, 1)
			s.builtinEmojiNotice = func(ctx context.Context, _ id.RoomID, body string) {
				if strings.Contains(body, test.wantBody) {
					if err := ctx.Err(); err != nil {
						t.Errorf("final notice context was already canceled on entry: %v", err)
					}
					s.importMu.Lock()
					running := s.importRunning
					s.importMu.Unlock()
					if running {
						t.Error("global import slot was held during final notice delivery")
					}
					finalEntered <- ctx
					<-ctx.Done()
					finalReturned <- struct{}{}
				}
			}
			started := make(chan context.Context, 1)
			s.builtinEmojiImporterFactory = func() builtinEmojiImportRunner {
				return &builtinEmojiCommandTestRunner{started: started, waitForContext: true, panicValue: test.panicValue}
			}

			s.handleBuiltinEmojiImportCommand(context.Background(), "!room:example.org", "@admin:example.org", "import wechat --ack-no-license")
			_ = waitBuiltinEmojiCommandContext(t, started)
			s.clearBuiltinEmojiRuntime(runtime)
			var finalCtx context.Context
			select {
			case finalCtx = <-finalEntered:
			case <-time.After(time.Second):
				t.Fatal("final notice was not attempted after runtime cancellation")
			}
			if _, ok := finalCtx.Deadline(); !ok {
				t.Fatal("final notice context has no deadline")
			}
			select {
			case <-finalReturned:
			case <-time.After(time.Second):
				t.Fatal("final notice did not unblock at its deadline")
			}
			assertBuiltinEmojiCommandSlotReleased(t, s)
		})
	}
}

func TestBuiltinEmojiCommandIntegratesExactEnglishAndChineseStickersSubcommands(t *testing.T) {
	for _, command := range []string{"import wechat --ack-no-license", "导入 微信 --ack-no-license"} {
		t.Run(command, func(t *testing.T) {
			recorder := newBuiltinEmojiCommandNoticeRecorder()
			s := commandTestServer(t, &builtinEmojiCommandTestAPI{loggedIn: true}, recorder)
			runtime := s.captureBuiltinEmojiRuntime(context.Background())
			defer s.clearBuiltinEmojiRuntime(runtime)
			started := make(chan context.Context, 1)
			s.builtinEmojiImporterFactory = func() builtinEmojiImportRunner {
				return &builtinEmojiCommandTestRunner{started: started, result: builtinEmojiImportResult{TotalConsidered: 109}}
			}

			s.handleStickersCommand(context.Background(), "!management:example.org", "@admin:example.org", command)

			_ = waitBuiltinEmojiCommandContext(t, started)
			recorder.waitFor(t, "!management:example.org", "completed")
			assertBuiltinEmojiCommandSlotReleased(t, s)
		})
	}
}

func TestImportLifecycleServerRunCapturesAndClearsCancelableBuiltinEmojiRuntimeContext(t *testing.T) {
	accounts := newAccountStore(filepath.Join(t.TempDir(), "accounts.json"))
	s, _ := newSpacePersistenceTestServerWithRecorder(t, accounts)
	api := &builtinEmojiCommandTestAPI{loggedIn: false, contexts: make(chan context.Context, 1)}
	s.api = api
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	apiCtx := waitBuiltinEmojiCommandContext(t, api.contexts)
	runtime := s.snapshotBuiltinEmojiRuntime()
	if runtime == nil || runtime.ctx != apiCtx {
		t.Fatalf("Run did not publish the context used by the API: runtime=%#v api=%#v", runtime, apiCtx)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run error = %v, want context cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after context cancellation")
	}
	if s.snapshotBuiltinEmojiRuntime() != nil {
		t.Fatal("Run did not clear the captured runtime context")
	}
	if !errors.Is(runtime.ctx.Err(), context.Canceled) {
		t.Fatalf("captured Run context error = %v, want canceled", runtime.ctx.Err())
	}
}

func TestImportLifecycleServerRunWithAlreadyCanceledContextStopsCleanly(t *testing.T) {
	accounts := newAccountStore(filepath.Join(t.TempDir(), "accounts.json"))
	s, _ := newSpacePersistenceTestServerWithRecorder(t, accounts)
	s.api = &builtinEmojiCommandTestAPI{loggedIn: false}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := s.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context cancellation", err)
	}
	if s.snapshotBuiltinEmojiRuntime() != nil {
		t.Fatal("Run retained import runtime after immediate cancellation")
	}
}

func TestImportLifecycleConcurrentServerRunStartStopIsSerialized(t *testing.T) {
	accounts := newAccountStore(filepath.Join(t.TempDir(), "accounts.json"))
	s, _ := newSpacePersistenceTestServerWithRecorder(t, accounts)
	s.api = &builtinEmojiCommandTestAPI{loggedIn: false}
	listenEntered := make(chan struct{})
	releaseListen := make(chan struct{})
	s.appServiceListen = func(network, address string) (net.Listener, error) {
		close(listenEntered)
		<-releaseListen
		return net.Listen(network, address)
	}

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	firstDone := make(chan error, 1)
	go func() { firstDone <- s.Run(firstCtx) }()
	select {
	case <-listenEntered:
	case <-time.After(time.Second):
		t.Fatal("first Run did not reach synchronous listen")
	}

	secondCtx, cancelSecond := context.WithCancel(context.Background())
	cancelSecond()
	secondDone := make(chan error, 1)
	go func() { secondDone <- s.Run(secondCtx) }()
	select {
	case err := <-secondDone:
		if err == nil {
			t.Fatal("concurrent Run unexpectedly succeeded")
		}
	case <-time.After(100 * time.Millisecond):
		close(releaseListen)
		select {
		case <-secondDone:
		case <-time.After(time.Second):
		}
		t.Fatal("concurrent Run blocked behind listener startup instead of being rejected")
	}

	close(releaseListen)
	cancelFirst()
	select {
	case err := <-firstDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("first Run error = %v, want cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("first Run did not stop")
	}
}

func TestBuiltinEmojiCommandHelpMentionsExactAcknowledgmentAndLicenseRisk(t *testing.T) {
	accounts := newAccountStore(filepath.Join(t.TempDir(), "accounts.json"))
	s, matrix := newSpacePersistenceTestServerWithRecorder(t, accounts)
	s.handleStickersCommand(context.Background(), "!management:example.org", "@admin:example.org", "help")
	notices := matrix.noticeBodies()
	if len(notices) != 1 {
		t.Fatalf("help notices = %d, want 1", len(notices))
	}
	for _, want := range []string{"stickers import wechat --ack-no-license", "license", "copy"} {
		if !strings.Contains(notices[0], want) {
			t.Fatalf("stickers help %q does not contain %q", notices[0], want)
		}
	}
}

func commandTestBuiltinEmojiStore(t *testing.T) *builtinEmojiStore {
	t.Helper()
	return newBuiltinEmojiStore(filepath.Join(t.TempDir(), "builtin-emoji.json"))
}

func commandTestServer(t *testing.T, api *builtinEmojiCommandTestAPI, recorder *builtinEmojiCommandNoticeRecorder) *Server {
	t.Helper()
	accounts := newAccountStore(filepath.Join(t.TempDir(), "accounts.json"))
	if err := accounts.put(savedAccount{ID: "wxid_a", SpaceRoom: "!space-a:example.org"}); err != nil {
		t.Fatal(err)
	}
	s := &Server{
		cfg:                &config.Config{Bridge: config.Bridge{Permissions: map[string]string{"@admin:example.org": "admin"}}},
		api:                api,
		accounts:           accounts,
		portals:            &portalStore{data: portalState{Portals: map[string]portalRecord{}, Pending: map[string]pendingPortal{}}},
		builtinEmojiStore:  commandTestBuiltinEmojiStore(t),
		builtinEmojiNotice: recorder.record,
	}
	s.builtinEmojiSpaceResolver = func(_ context.Context, account remoteapi.UserID) (id.RoomID, error) {
		stored, ok := accounts.get(account)
		if !ok || stored.SpaceRoom == "" {
			return "", errors.New("Space unavailable")
		}
		return id.RoomID(stored.SpaceRoom), nil
	}
	return s
}

func waitBuiltinEmojiCommandContext(t *testing.T, contexts <-chan context.Context) context.Context {
	t.Helper()
	select {
	case ctx := <-contexts:
		return ctx
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for context")
		return nil
	}
}

func assertBuiltinEmojiCommandSlotReleased(t *testing.T, s *Server) {
	t.Helper()
	s.importMu.Lock()
	defer s.importMu.Unlock()
	if s.importRunning {
		t.Fatal("global built-in emoji import slot was not released")
	}
}
