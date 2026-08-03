package matrixas

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"maunium.net/go/mautrix/id"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"github.com/leat/mautrix-go-wechat/internal/wechat/textemoji"
)

func TestTruncateUTF8BytesReturnsValidBoundedPrefixes(t *testing.T) {
	tests := []struct {
		name  string
		input string
		limit int
		want  string
	}{
		{name: "negative", input: "abc", limit: -1, want: ""},
		{name: "zero", input: "abc", limit: 0, want: ""},
		{name: "ASCII boundary", input: "abcdef", limit: 4, want: "abcd"},
		{name: "exact multibyte boundary", input: "A微信B", limit: 7, want: "A微信"},
		{name: "does not split rune", input: "A微信B", limit: 6, want: "A微"},
		{name: "invalid input becomes valid", input: "A\xffB", limit: 8, want: "A\uFFFDB"},
		{name: "invalid replacement does not exceed limit", input: "A\xffB", limit: 3, want: "A"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := truncateUTF8Bytes(test.input, test.limit)
			if got != test.want {
				t.Fatalf("truncateUTF8Bytes(%q, %d) = %q, want %q", test.input, test.limit, got, test.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("truncateUTF8Bytes() returned invalid UTF-8: %q", got)
			}
			if len(got) > max(test.limit, 0) {
				t.Fatalf("truncateUTF8Bytes() returned %d bytes for limit %d", len(got), test.limit)
			}
		})
	}
}

func TestBuiltinEmojiImportResultIsDeterministicSortedBoundedAndComplete(t *testing.T) {
	entries := textemoji.Entries()
	failures := make([]builtinEmojiImportFailure, 0, 22)
	unreferenced := make([]builtinEmojiUploadedUnreferenced, 0, 3)
	for registryIndex := 22; registryIndex >= 1; registryIndex-- {
		entry := entries[registryIndex-1]
		failures = append(failures, builtinEmojiImportFailure{
			RegistryIndex: registryIndex,
			Shortcode:     entry.Shortcode,
			Stage:         builtinEmojiImportStageFetch,
			Code:          builtinEmojiImportCodeFetchFailed,
			Reason:        builtinEmojiSafeReason(builtinEmojiFetchHTTPStatus),
			HTTPStatus:    503,
		})
		if registryIndex <= 3 {
			unreferenced = append(unreferenced, builtinEmojiUploadedUnreferenced{
				RegistryIndex: registryIndex,
				Shortcode:     entry.Shortcode,
				MXC:           id.ContentURIString(fmt.Sprintf("mxc://secret.example/%d", registryIndex)),
				Reason:        builtinEmojiImportCodePersistFailed,
			})
		}
	}
	result := builtinEmojiImportResult{
		CommandStatus:        builtinEmojiCommandStatusFinished,
		Account:              "wxid_result",
		Space:                "!space:example.org",
		TotalConsidered:      109,
		Uploaded:             24,
		Imported:             18,
		Persisted:            18,
		Skipped:              69,
		Failures:             failures,
		UploadedUnreferenced: unreferenced,
		Persistence:          builtinEmojiPersistencePartial,
		FinalReconciliation: builtinEmojiImportReconciliationStatus{
			Status: builtinPackStatusPartial, AttemptedRooms: 8, ReconciledRooms: 7, FailedRooms: 1,
		},
		AccountData: builtinEmojiImportAccountDataStatus{
			Status: builtinPackStatusSuccess, User: "@stickers:example.org", Room: "!joined:example.org", Writes: 2,
		},
		ProgressNoticeFailed: true,
	}
	before, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}

	first := formatBuiltinEmojiImportResult(result)
	second := formatBuiltinEmojiImportResult(result)
	if first != second {
		t.Fatal("formatBuiltinEmojiImportResult() is nondeterministic")
	}
	if len(first) > builtinEmojiCommandNoticeLimit {
		t.Fatalf("formatted result is %d bytes, notice limit is %d", len(first), builtinEmojiCommandNoticeLimit)
	}
	after, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("formatBuiltinEmojiImportResult() mutated its input")
	}

	for _, want := range []string{
		builtinEmojiSourceRepository,
		builtinEmojiSourceCommit,
		builtinEmojiSourcePath,
		"total=109",
		"uploaded=24",
		"imported=18",
		"persisted=18",
		"skipped=69",
		"failed=22",
		"uploaded_unreferenced=3",
		"persistence=partial",
		"status=partial attempted_rooms=8 reconciled_rooms=7 failed_rooms=1",
		"status=success user=@stickers:example.org room=!joined:example.org writes=2",
		"notice_status=failed",
		"2 additional failures omitted",
		"stickers import wechat --ack-no-license",
		"Emojipedia",
		"WeChat Official Accounts Platform",
		"Web WeChat",
		"no explicit license",
		"does not grant rights",
		"copying",
		"redistribution",
		"wxid_result",
		"!space:example.org",
	} {
		if !strings.Contains(first, want) {
			t.Fatalf("formatted result does not contain %q:\n%s", want, first)
		}
	}
	for _, forbidden := range []string{"mxc://", "secret.example", entries[20].Shortcode, entries[21].Shortcode} {
		if strings.Contains(first, forbidden) {
			t.Fatalf("formatted result contains forbidden or omitted value %q:\n%s", forbidden, first)
		}
	}
	lastPosition := -1
	for registryIndex := 1; registryIndex <= 20; registryIndex++ {
		marker := fmt.Sprintf("#%03d", registryIndex)
		position := strings.Index(first, marker)
		if position <= lastPosition {
			t.Fatalf("failure %s was absent or out of order:\n%s", marker, first)
		}
		lastPosition = position
	}
	for _, line := range strings.Split(first, "\n") {
		if !strings.HasPrefix(line, "- #") {
			continue
		}
		if !utf8.ValidString(line) || len(line) > builtinEmojiFailureDetailLimit {
			t.Fatalf("failure detail is invalid or %d bytes (limit %d): %q", len(line), builtinEmojiFailureDetailLimit, line)
		}
	}

	longResult := result
	longResult.Failures = make([]builtinEmojiImportFailure, 22)
	for index := range longResult.Failures {
		longResult.Failures[index] = builtinEmojiImportFailure{
			RegistryIndex: 1000 + index,
			Shortcode:     strings.Repeat("秘密界", 100),
			Stage:         builtinEmojiImportStageFetch,
			Code:          builtinEmojiImportCodeFetchFailed,
		}
	}
	longOutput := formatBuiltinEmojiImportResult(longResult)
	if !utf8.ValidString(longOutput) || len(longOutput) > builtinEmojiCommandNoticeLimit {
		t.Fatalf("worst-case formatted result is invalid or %d bytes (notice limit %d)", len(longOutput), builtinEmojiCommandNoticeLimit)
	}
	if strings.Count(longOutput, "\n- #") != builtinEmojiResultFailureLimit || !strings.Contains(longOutput, "2 additional failures omitted") || !strings.Contains(longOutput, "does not grant rights") {
		t.Fatalf("bounded worst-case result lost required content:\n%s", longOutput)
	}
}

func TestBuiltinEmojiFetchSafeDetailsExposeOnlyEnumAndHTTPStatus(t *testing.T) {
	secret := "https://token:password@example.org/private?access_token=SECRET response-body"
	wrapped := fmt.Errorf("%s: %w", secret, &builtinEmojiFetchError{code: builtinEmojiFetchHTTPStatus, status: 429})
	reason, status := safeBuiltinEmojiFetchDetails(wrapped)
	if reason != builtinEmojiFetchHTTPStatus || status != 429 {
		t.Fatalf("safeBuiltinEmojiFetchDetails() = (%q, %d)", reason, status)
	}

	reason, status = safeBuiltinEmojiFetchDetails(errors.New(secret))
	if reason != "" || status != 0 {
		t.Fatalf("untyped fetch error exposed details: (%q, %d)", reason, status)
	}
}

type builtinEmojiErrorLocker struct {
	err error
}

func (locker builtinEmojiErrorLocker) Lock(context.Context) error {
	return locker.err
}

func (builtinEmojiErrorLocker) Unlock() {}

type builtinEmojiLogCapture struct {
	mu     sync.Mutex
	events []builtinEmojiLogFields
}

func (capture *builtinEmojiLogCapture) add(fields builtinEmojiLogFields) {
	capture.mu.Lock()
	capture.events = append(capture.events, fields)
	capture.mu.Unlock()
}

func (capture *builtinEmojiLogCapture) snapshot() []builtinEmojiLogFields {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return append([]builtinEmojiLogFields(nil), capture.events...)
}

func (capture *builtinEmojiLogCapture) requireCode(t *testing.T, code builtinEmojiLogCode) builtinEmojiLogFields {
	t.Helper()
	for _, fields := range capture.snapshot() {
		if fields.Code == code {
			return fields
		}
	}
	t.Fatalf("log code %q not found in %#v", code, capture.snapshot())
	return builtinEmojiLogFields{}
}

func TestBuiltinEmojiLoggingUsesAllowlistedStructuredFieldsAndDropsSecrets(t *testing.T) {
	const (
		account = remoteapi.UserID("wxid_logger")
		secret  = "SECRET-token https://user:pass@example.org/image.webp?access_token=SECRET response-body RIFF-WEBP-image-bytes mxc://secret.example/media"
	)
	entries := textemoji.Entries()

	t.Run("fetch", func(t *testing.T) {
		capture := &builtinEmojiLogCapture{}
		store := newTestBuiltinEmojiImportStore()
		store.seed(entries[1:])
		importer := &builtinEmojiImporter{
			fetcher: testBuiltinEmojiFetcherFunc(func(context.Context, textemoji.Entry) ([]byte, error) {
				return nil, fmt.Errorf("%s: %w", secret, &builtinEmojiFetchError{code: builtinEmojiFetchHTTPStatus, status: 503})
			}),
			storeView: store, storePersister: store, packLock: &testBuiltinEmojiImportLocker{},
			reconcile: func(context.Context) (reconciliationResult, accountDataVisibilityResult) {
				return reconciliationResult{Status: builtinPackStatusSuccess}, accountDataVisibilityResult{Status: builtinPackStatusSkipped}
			},
			account: account, logger: capture.add,
		}
		_ = importer.Run(context.Background())
		fields := capture.requireCode(t, builtinEmojiLogCodeFetchFailed)
		assertBuiltinEmojiEntryLogFields(t, fields, account, entries[0], builtinEmojiImportStageFetch)
		if fields.Reason != builtinEmojiSafeReason(builtinEmojiFetchHTTPStatus) || fields.HTTPStatus != 503 {
			t.Fatalf("fetch safe details = %#v", fields)
		}
		assertBuiltinEmojiLogsExclude(t, capture.snapshot(), secret)
	})

	t.Run("upload and bytes", func(t *testing.T) {
		capture := &builtinEmojiLogCapture{}
		store := newTestBuiltinEmojiImportStore()
		store.seed(entries[1:])
		importer := &builtinEmojiImporter{
			fetcher: testBuiltinEmojiFetcherFunc(func(context.Context, textemoji.Entry) ([]byte, error) {
				return []byte(secret), nil
			}),
			uploader: testBuiltinEmojiUploaderFunc(func(context.Context, []byte, string, string) (id.ContentURIString, error) {
				return "", errors.New(secret)
			}),
			storeView: store, storePersister: store, packLock: &testBuiltinEmojiImportLocker{},
			reconcile: func(context.Context) (reconciliationResult, accountDataVisibilityResult) {
				return reconciliationResult{Status: builtinPackStatusSuccess}, accountDataVisibilityResult{Status: builtinPackStatusSkipped}
			},
			account: account, logger: capture.add,
		}
		_ = importer.Run(context.Background())
		fields := capture.requireCode(t, builtinEmojiLogCodeUploadFailed)
		assertBuiltinEmojiEntryLogFields(t, fields, account, entries[0], builtinEmojiImportStageUpload)
		assertBuiltinEmojiLogsExclude(t, capture.snapshot(), secret)
	})

	t.Run("persist and Matrix reconciliation", func(t *testing.T) {
		capture := &builtinEmojiLogCapture{}
		store := newTestBuiltinEmojiImportStore()
		store.seed(entries[1:])
		store.failAt = 1
		importer := &builtinEmojiImporter{
			fetcher: testBuiltinEmojiFetcherFunc(func(context.Context, textemoji.Entry) ([]byte, error) { return []byte(secret), nil }),
			uploader: testBuiltinEmojiUploaderFunc(func(_ context.Context, _ []byte, _ string, _ string) (id.ContentURIString, error) {
				return "mxc://example.org/safe", nil
			}),
			storeView: store, storePersister: store, packLock: &testBuiltinEmojiImportLocker{},
			reconcile: func(context.Context) (reconciliationResult, accountDataVisibilityResult) {
				return reconciliationResult{Status: builtinPackStatusFailed, AttemptedRooms: 2, FailedRooms: 2, Warnings: []string{secret}}, accountDataVisibilityResult{Status: builtinPackStatusFailed, User: "@stickers:example.org", Room: "!joined:example.org", Warnings: []string{secret}}
			},
			account: account, logger: capture.add,
		}
		_ = importer.Run(context.Background())
		persist := capture.requireCode(t, builtinEmojiLogCodePersistFailed)
		assertBuiltinEmojiEntryLogFields(t, persist, account, entries[0], builtinEmojiImportStagePersist)
		reconcile := capture.requireCode(t, builtinEmojiLogCodeReconcileFailed)
		if reconcile.Account != account || reconcile.Commit != builtinEmojiSourceCommit || reconcile.Stage != builtinEmojiImportStageReconcile || reconcile.ReconciliationStatus != builtinPackStatusFailed || reconcile.AccountDataStatus != builtinPackStatusFailed {
			t.Fatalf("reconciliation fields = %#v", reconcile)
		}
		assertBuiltinEmojiLogsExclude(t, capture.snapshot(), secret, "persist secret body token")
	})

	t.Run("reconcile lock error", func(t *testing.T) {
		capture := &builtinEmojiLogCapture{}
		store := newTestBuiltinEmojiImportStore()
		store.seed(entries)
		importer := &builtinEmojiImporter{
			storeView: store, storePersister: store, packLock: builtinEmojiErrorLocker{err: errors.New(secret)},
			account: account, logger: capture.add,
		}
		_ = importer.Run(context.Background())
		fields := capture.requireCode(t, builtinEmojiLogCodeReconcileFailed)
		if fields.Account != account || fields.Commit != builtinEmojiSourceCommit || fields.Stage != builtinEmojiImportStageReconcile || fields.Reason != "reconcile_lock_failed" {
			t.Fatalf("reconcile lock log fields = %#v", fields)
		}
		assertBuiltinEmojiLogsExclude(t, capture.snapshot(), secret)
	})

	t.Run("worker panic", func(t *testing.T) {
		capture := &builtinEmojiLogCapture{}
		store := newTestBuiltinEmojiImportStore()
		store.seed(entries[1:])
		importer := &builtinEmojiImporter{
			fetcher:   testBuiltinEmojiFetcherFunc(func(context.Context, textemoji.Entry) ([]byte, error) { panic(secret) }),
			storeView: store, storePersister: store, packLock: &testBuiltinEmojiImportLocker{},
			reconcile: func(context.Context) (reconciliationResult, accountDataVisibilityResult) {
				return reconciliationResult{Status: builtinPackStatusSuccess}, accountDataVisibilityResult{Status: builtinPackStatusSkipped}
			},
			account: account, logger: capture.add,
		}
		_ = importer.Run(context.Background())
		fields := capture.requireCode(t, builtinEmojiLogCodeWorkerPanic)
		assertBuiltinEmojiEntryLogFields(t, fields, account, entries[0], builtinEmojiImportStageWorker)
		assertBuiltinEmojiLogsExclude(t, capture.snapshot(), secret)
	})
}

func TestBuiltinEmojiLoggingCapturesCommandAccountInProductionImporterContext(t *testing.T) {
	capture := &builtinEmojiLogCapture{}
	recorder := newBuiltinEmojiCommandNoticeRecorder()
	s := commandTestServer(t, &builtinEmojiCommandTestAPI{loggedIn: true}, recorder)
	s.builtinEmojiLogger = capture.add
	runtime := s.captureBuiltinEmojiRuntime(context.Background())
	defer s.clearBuiltinEmojiRuntime(runtime)

	entries := textemoji.Entries()
	store := newTestBuiltinEmojiImportStore()
	store.seed(entries[1:])
	importer := &builtinEmojiImporter{
		fetcher: testBuiltinEmojiFetcherFunc(func(context.Context, textemoji.Entry) ([]byte, error) {
			return nil, newBuiltinEmojiFetchError(builtinEmojiFetchRequestFailed)
		}),
		storeView: store, storePersister: store, packLock: &testBuiltinEmojiImportLocker{},
		reconcile: func(context.Context) (reconciliationResult, accountDataVisibilityResult) {
			return reconciliationResult{Status: builtinPackStatusSuccess}, accountDataVisibilityResult{Status: builtinPackStatusSkipped}
		},
	}
	s.builtinEmojiImporterFactory = func() builtinEmojiImportRunner { return importer }

	s.handleBuiltinEmojiImportCommand(context.Background(), "!management:example.org", "@admin:example.org", "import wechat --ack-no-license")
	recorder.waitFor(t, "!management:example.org", "completed")
	fields := capture.requireCode(t, builtinEmojiLogCodeFetchFailed)
	assertBuiltinEmojiEntryLogFields(t, fields, "wxid_a", entries[0], builtinEmojiImportStageFetch)
}

func TestBuiltinEmojiCommandLogsStartCompletionAndPanicWithoutPanicValue(t *testing.T) {
	const secret = "SECRET panic token https://user:pass@example.org/?access_token=SECRET response-body image-bytes mxc://secret.example/media"
	for _, test := range []struct {
		name       string
		panicValue any
		wantCode   builtinEmojiLogCode
	}{
		{name: "completion", wantCode: builtinEmojiLogCodeCompleted},
		{name: "panic", panicValue: secret, wantCode: builtinEmojiLogCodePanic},
	} {
		t.Run(test.name, func(t *testing.T) {
			capture := &builtinEmojiLogCapture{}
			recorder := newBuiltinEmojiCommandNoticeRecorder()
			s := commandTestServer(t, &builtinEmojiCommandTestAPI{loggedIn: true}, recorder)
			s.builtinEmojiLogger = capture.add
			runtime := s.captureBuiltinEmojiRuntime(context.Background())
			defer s.clearBuiltinEmojiRuntime(runtime)
			s.builtinEmojiImporterFactory = func() builtinEmojiImportRunner {
				return &builtinEmojiCommandTestRunner{
					result: builtinEmojiImportResult{
						TotalConsidered: 109,
						Imported:        108,
						Persisted:       108,
						Skipped:         1,
						Persistence:     builtinEmojiPersistenceSuccess,
					},
					panicValue: test.panicValue,
				}
			}

			s.handleBuiltinEmojiImportCommand(context.Background(), "!management:example.org", "@admin:example.org", "import wechat --ack-no-license")
			if test.panicValue == nil {
				recorder.waitFor(t, "!management:example.org", "completed")
			} else {
				recorder.waitFor(t, "!management:example.org", "failed safely")
			}

			started := capture.requireCode(t, builtinEmojiLogCodeStarted)
			if started.Account != "wxid_a" || started.Commit != builtinEmojiSourceCommit || started.Total != 109 {
				t.Fatalf("start log fields = %#v", started)
			}
			final := capture.requireCode(t, test.wantCode)
			if final.Account != "wxid_a" || final.Commit != builtinEmojiSourceCommit {
				t.Fatalf("final log fields = %#v", final)
			}
			if test.panicValue == nil && (final.Total != 109 || final.Imported != 108 || final.Persisted != 108 || final.Skipped != 1 || final.PersistenceStatus != builtinEmojiPersistenceSuccess) {
				t.Fatalf("completion aggregate fields = %#v", final)
			}
			assertBuiltinEmojiLogsExclude(t, capture.snapshot(), secret)
		})
	}
}

func assertBuiltinEmojiEntryLogFields(t *testing.T, fields builtinEmojiLogFields, account remoteapi.UserID, entry textemoji.Entry, stage builtinEmojiImportStage) {
	t.Helper()
	if fields.Account != account || fields.Commit != builtinEmojiSourceCommit || fields.RegistryIndex != entry.Index || fields.Chinese != entry.Chinese || fields.Name != entry.English || fields.Shortcode != entry.Shortcode || fields.Stage != stage {
		t.Fatalf("entry log fields = %#v, entry = %#v", fields, entry)
	}
}

func assertBuiltinEmojiLogsExclude(t *testing.T, events []builtinEmojiLogFields, secrets ...string) {
	t.Helper()
	encoded, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range secrets {
		for _, fragment := range strings.Fields(secret) {
			sensitive := strings.Contains(fragment, "SECRET") || strings.Contains(fragment, "://") || strings.Contains(fragment, "response-body") || strings.Contains(fragment, "image-bytes") || strings.Contains(fragment, "RIFF-WEBP")
			if sensitive && strings.Contains(string(encoded), fragment) {
				t.Fatalf("structured logs exposed secret fragment %q: %s", fragment, encoded)
			}
		}
	}
}
