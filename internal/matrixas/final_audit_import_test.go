package matrixas

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"maunium.net/go/mautrix/id"

	"github.com/leat/mautrix-go-wechat/internal/wechat/textemoji"
)

type finalAuditPanicPersister struct {
	delegate  *testBuiltinEmojiImportStore
	panicNext atomic.Bool
}

func (persister *finalAuditPanicPersister) PersistMerged(replacements map[string]string) error {
	if persister.panicNext.CompareAndSwap(true, false) {
		panic("persistence panic")
	}
	return persister.delegate.PersistMerged(replacements)
}

type finalAuditPanicLocker struct {
	delegate  *testBuiltinEmojiImportLocker
	panicNext atomic.Bool
}

func (locker *finalAuditPanicLocker) Lock(ctx context.Context) error {
	if locker.panicNext.CompareAndSwap(true, false) {
		panic("lock panic")
	}
	return locker.delegate.Lock(ctx)
}

func (locker *finalAuditPanicLocker) Unlock() { locker.delegate.Unlock() }

func TestBuiltinEmojiImportControllerPanicStopsWorkersReleasesSlotAndRetries(t *testing.T) {
	for _, panicPoint := range []string{"persistence", "reconciliation", "lock", "batch observer", "controller hook"} {
		t.Run(panicPoint, func(t *testing.T) {
			store := newTestBuiltinEmojiImportStore()
			workers := &testBuiltinEmojiImportWorkerCounts{}
			baseLocker := &testBuiltinEmojiImportLocker{}
			panicPersister := &finalAuditPanicPersister{delegate: store}
			panicLocker := &finalAuditPanicLocker{delegate: baseLocker}
			var reconcilePanic atomic.Bool
			var observerPanic atomic.Bool
			var controllerPanic atomic.Bool
			importer := &builtinEmojiImporter{
				fetcher: testBuiltinEmojiFetcherFunc(func(_ context.Context, entry textemoji.Entry) ([]byte, error) {
					return []byte(fmt.Sprintf("%d", entry.Index)), nil
				}),
				uploader: testBuiltinEmojiUploaderFunc(func(_ context.Context, data []byte, _, _ string) (id.ContentURIString, error) {
					return id.ContentURIString("mxc://audit.example/" + string(data)), nil
				}),
				storeView: store, storePersister: panicPersister,
				packLock: panicLocker, operationTimeout: time.Second, hooks: workers.hooks(),
				reconcile: func(context.Context) (reconciliationResult, accountDataVisibilityResult) {
					if reconcilePanic.CompareAndSwap(true, false) {
						panic("reconciliation panic")
					}
					return reconciliationResult{Status: builtinPackStatusSuccess}, accountDataVisibilityResult{Status: builtinPackStatusSkipped}
				},
				observeBatch: func(builtinEmojiImportBatch) {
					if observerPanic.CompareAndSwap(true, false) {
						panic("batch observer panic")
					}
				},
			}
			importer.hooks.ControllerStarted = func() {
				if controllerPanic.CompareAndSwap(true, false) {
					panic("controller hook panic")
				}
			}
			switch panicPoint {
			case "persistence":
				panicPersister.panicNext.Store(true)
			case "reconciliation":
				reconcilePanic.Store(true)
			case "lock":
				panicLocker.panicNext.Store(true)
			case "batch observer":
				observerPanic.Store(true)
			case "controller hook":
				controllerPanic.Store(true)
			}

			recorder := newBuiltinEmojiCommandNoticeRecorder()
			s := commandTestServer(t, &builtinEmojiCommandTestAPI{loggedIn: true}, recorder)
			runtime := s.captureBuiltinEmojiRuntime(context.Background())
			defer s.clearBuiltinEmojiRuntime(runtime)
			s.builtinEmojiImporterFactory = func() builtinEmojiImportRunner { return importer }

			s.handleBuiltinEmojiImportCommand(context.Background(), "!audit:example.org", "@admin:example.org", "import wechat --ack-no-license")
			recorder.waitFor(t, "!audit:example.org", "failed safely")
			waitBuiltinEmojiCommandSlotReleased(t, s)
			if workers.started.Load() != builtinEmojiImportWorkerCount || workers.stopped.Load() != builtinEmojiImportWorkerCount {
				t.Fatalf("workers after panic = started %d stopped %d, want %d/%d", workers.started.Load(), workers.stopped.Load(), builtinEmojiImportWorkerCount, builtinEmojiImportWorkerCount)
			}

			s.handleBuiltinEmojiImportCommand(context.Background(), "!audit:example.org", "@admin:example.org", "import wechat --ack-no-license")
			recorder.waitFor(t, "!audit:example.org", "completed")
			waitBuiltinEmojiCommandSlotReleased(t, s)
			if workers.stopped.Load() != 2*builtinEmojiImportWorkerCount {
				t.Fatalf("worker stop count after immediate retry = %d, want %d", workers.stopped.Load(), 2*builtinEmojiImportWorkerCount)
			}
		})
	}
}

func TestBuiltinEmojiImportResultIncludesTypedSafeFetchDiagnostics(t *testing.T) {
	entries := textemoji.Entries()
	store := newTestBuiltinEmojiImportStore()
	store.seed(entries[1:])
	importer := &builtinEmojiImporter{
		fetcher: testBuiltinEmojiFetcherFunc(func(context.Context, textemoji.Entry) ([]byte, error) {
			return nil, fmt.Errorf("private upstream detail: %w", &builtinEmojiFetchError{code: builtinEmojiFetchHTTPStatus, status: 429})
		}),
		storeView: store, storePersister: store,
		packLock: &testBuiltinEmojiImportLocker{}, operationTimeout: time.Second,
	}

	result := importer.Run(context.Background())
	if len(result.Failures) != 1 {
		t.Fatalf("failures = %#v, want one typed fetch failure", result.Failures)
	}
	failure := result.Failures[0]
	if failure.RegistryIndex != entries[0].Index || failure.Code != builtinEmojiImportCodeFetchFailed || failure.Reason != builtinEmojiSafeReason(builtinEmojiFetchHTTPStatus) || failure.HTTPStatus != 429 {
		t.Fatalf("fetch failure safe diagnostics = %#v", failure)
	}
}
