package matrixas

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"

	"github.com/leat/mautrix-go-wechat/internal/wechat/textemoji"
)

type testBuiltinEmojiFetcherFunc func(context.Context, textemoji.Entry) ([]byte, error)

func (fn testBuiltinEmojiFetcherFunc) Fetch(ctx context.Context, entry textemoji.Entry) ([]byte, error) {
	return fn(ctx, entry)
}

type testBuiltinEmojiUploaderFunc func(context.Context, []byte, string, string) (id.ContentURIString, error)

func (fn testBuiltinEmojiUploaderFunc) Upload(ctx context.Context, data []byte, mimeType, filename string) (id.ContentURIString, error) {
	return fn(ctx, data, mimeType, filename)
}

type testBuiltinEmojiCancelOnErrContext struct {
	context.Context
	cancel       context.CancelFunc
	cancelOnCall int32
	errCalls     atomic.Int32
	workersReady <-chan struct{}
}

func (ctx *testBuiltinEmojiCancelOnErrContext) Err() error {
	err := ctx.Context.Err()
	if err == nil && ctx.errCalls.Add(1) == ctx.cancelOnCall {
		<-ctx.workersReady
		ctx.cancel()
	}
	return err
}

type testBuiltinEmojiImportStore struct {
	mu                sync.Mutex
	images            map[string]string
	persistCalls      int
	persistBatches    [][]int
	failAt            int
	failed            atomic.Bool
	raceShortcode     string
	raceRegistryIndex int
	getCalls          map[string]int
}

func newTestBuiltinEmojiImportStore() *testBuiltinEmojiImportStore {
	return &testBuiltinEmojiImportStore{images: make(map[string]string), getCalls: make(map[string]int)}
}

func (store *testBuiltinEmojiImportStore) Get(shortcode string) (string, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.getCalls[shortcode]++
	if shortcode == store.raceShortcode && store.getCalls[shortcode] == 2 {
		store.images[shortcode] = testBuiltinEmojiMXC(store.raceRegistryIndex, "race")
	}
	mxc, ok := store.images[shortcode]
	return mxc, ok
}

func (store *testBuiltinEmojiImportStore) PersistMerged(replacements map[string]string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.persistCalls++
	indexes := make([]int, 0, len(replacements))
	for shortcode := range replacements {
		indexes = append(indexes, testBuiltinEmojiRegistryIndex(shortcode))
	}
	sort.Ints(indexes)
	store.persistBatches = append(store.persistBatches, indexes)
	if store.failAt > 0 && store.persistCalls == store.failAt {
		store.failed.Store(true)
		return errors.New("persist secret body token")
	}
	for shortcode, mxc := range replacements {
		store.images[shortcode] = mxc
	}
	return nil
}

func (store *testBuiltinEmojiImportStore) seed(entries []textemoji.Entry) {
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, entry := range entries {
		store.images[entry.Shortcode] = testBuiltinEmojiMXC(entry.Index, "seed")
	}
}

func (store *testBuiltinEmojiImportStore) count() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return len(store.images)
}

func (store *testBuiltinEmojiImportStore) persistenceCalls() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.persistCalls
}

func (store *testBuiltinEmojiImportStore) resetPersistenceFailure() {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.failAt = 0
	store.persistCalls = 0
	store.persistBatches = nil
	store.failed.Store(false)
}

type testBuiltinEmojiImportLocker struct {
	mu     sync.Mutex
	locked atomic.Bool
}

func (locker *testBuiltinEmojiImportLocker) Lock(ctx context.Context) error {
	for !locker.mu.TryLock() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
	locker.locked.Store(true)
	return nil
}

func (locker *testBuiltinEmojiImportLocker) Unlock() {
	locker.locked.Store(false)
	locker.mu.Unlock()
}

type testBuiltinEmojiImportBlockingLocker struct {
	inner     *builtinEmojiImportMutexLocker
	attempted chan struct{}
	once      sync.Once
}

func (locker *testBuiltinEmojiImportBlockingLocker) Lock(ctx context.Context) error {
	locker.once.Do(func() { close(locker.attempted) })
	return locker.inner.Lock(ctx)
}

func (locker *testBuiltinEmojiImportBlockingLocker) Unlock() {
	locker.inner.Unlock()
}

type testBuiltinEmojiImportClock struct {
	mu             sync.Mutex
	now            time.Time
	nowCalls       int
	childDurations []time.Duration
}

func (clock *testBuiltinEmojiImportClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	result := clock.now.Add(time.Duration(clock.nowCalls) * time.Second)
	clock.nowCalls++
	return result
}

func (clock *testBuiltinEmojiImportClock) ChildTimeout(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	clock.mu.Lock()
	clock.childDurations = append(clock.childDurations, timeout)
	clock.mu.Unlock()
	return context.WithCancel(parent)
}

func (clock *testBuiltinEmojiImportClock) snapshot() (int, []time.Duration) {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.nowCalls, append([]time.Duration(nil), clock.childDurations...)
}

type testBuiltinEmojiImportWorkerCounts struct {
	started atomic.Int32
	stopped atomic.Int32
}

func (counts *testBuiltinEmojiImportWorkerCounts) hooks() builtinEmojiImportHooks {
	return builtinEmojiImportHooks{
		WorkerStarted: func(int) { counts.started.Add(1) },
		WorkerStopped: func(int) { counts.stopped.Add(1) },
	}
}

type testBuiltinEmojiConcurrency struct {
	active atomic.Int32
	max    atomic.Int32
}

func (counter *testBuiltinEmojiConcurrency) begin() func() {
	active := counter.active.Add(1)
	for {
		maximum := counter.max.Load()
		if active <= maximum || counter.max.CompareAndSwap(maximum, active) {
			break
		}
	}
	return func() { counter.active.Add(-1) }
}

func TestBuiltinEmojiImportUsesFourWorkersAndCommitsOrderedBatchesAfterOutOfOrderCompletion(t *testing.T) {
	entries := textemoji.Entries()
	if len(entries) != 109 {
		t.Fatalf("registry entries = %d, want 109", len(entries))
	}
	store := newTestBuiltinEmojiImportStore()
	locker := &testBuiltinEmojiImportLocker{}
	workers := &testBuiltinEmojiImportWorkerCounts{}
	concurrency := &testBuiltinEmojiConcurrency{}
	started := make(chan int, 4)
	completed := make(chan int, 4)
	releases := map[int]chan struct{}{1: make(chan struct{}), 2: make(chan struct{}), 3: make(chan struct{}), 4: make(chan struct{})}
	fetcher := testBuiltinEmojiFetcherFunc(func(_ context.Context, entry textemoji.Entry) ([]byte, error) {
		defer concurrency.begin()()
		if release := releases[entry.Index]; release != nil {
			started <- entry.Index
			<-release
		}
		return []byte(strconv.Itoa(entry.Index)), nil
	})
	uploader := testBuiltinEmojiUploaderFunc(func(_ context.Context, data []byte, mimeType, filename string) (id.ContentURIString, error) {
		defer concurrency.begin()()
		index, err := strconv.Atoi(string(data))
		if err != nil {
			return "", err
		}
		if mimeType != "image/webp" {
			t.Errorf("upload MIME = %q, want image/webp", mimeType)
		}
		if filename != entries[index-1].Filename {
			t.Errorf("upload filename = %q, want %q", filename, entries[index-1].Filename)
		}
		if index <= 4 {
			completed <- index
		}
		return id.ContentURIString(testBuiltinEmojiMXC(index, "ordered")), nil
	})
	var observationsMu sync.Mutex
	var observations []builtinEmojiImportBatch
	reconcileCalls := 0
	importer := &builtinEmojiImporter{
		fetcher:          fetcher,
		uploader:         uploader,
		storeView:        store,
		storePersister:   store,
		packLock:         locker,
		operationTimeout: time.Second,
		hooks:            workers.hooks(),
		observeBatch: func(batch builtinEmojiImportBatch) {
			if locker.locked.Load() {
				t.Error("batch observer called while pack lock held")
			}
			observationsMu.Lock()
			observations = append(observations, batch)
			observationsMu.Unlock()
		},
		reconcile: func(context.Context) (reconciliationResult, accountDataVisibilityResult) {
			if !locker.locked.Load() {
				t.Error("reconciler called without pack lock")
			}
			reconcileCalls++
			return reconciliationResult{Status: builtinPackStatusSuccess}, accountDataVisibilityResult{Status: builtinPackStatusSuccess}
		},
	}

	done := make(chan builtinEmojiImportResult, 1)
	go func() { done <- importer.Run(context.Background()) }()
	seenStarted := map[int]bool{}
	for range 4 {
		seenStarted[<-started] = true
	}
	if !reflect.DeepEqual(seenStarted, map[int]bool{1: true, 2: true, 3: true, 4: true}) {
		t.Fatalf("first started jobs = %v, want registry indexes 1..4", seenStarted)
	}
	for _, index := range []int{4, 3, 2} {
		close(releases[index])
		if got := <-completed; got != index {
			t.Fatalf("completion = %d, want deliberately out-of-order %d", got, index)
		}
	}
	observationsMu.Lock()
	prematureBatches := len(observations)
	observationsMu.Unlock()
	if prematureBatches != 0 {
		t.Fatalf("observed %d batches before registry index 1 completed", prematureBatches)
	}
	close(releases[1])

	var result builtinEmojiImportResult
	select {
	case result = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("import deadlocked")
	}
	if workers.started.Load() != 4 || workers.stopped.Load() != 4 {
		t.Fatalf("workers started/stopped = %d/%d, want 4/4", workers.started.Load(), workers.stopped.Load())
	}
	if concurrency.max.Load() > 4 || concurrency.max.Load() != 4 {
		t.Fatalf("max concurrent operations = %d, want exactly 4 and never above 4", concurrency.max.Load())
	}
	if result.TotalConsidered != 109 || result.Uploaded != 109 || result.Imported != 109 || result.Persisted != 109 || result.Skipped != 0 || len(result.Failures) != 0 {
		t.Fatalf("result counts = %#v", result)
	}
	wantSizes := []int{16, 16, 16, 16, 16, 16, 13}
	if !reflect.DeepEqual(result.BatchSizes, wantSizes) {
		t.Fatalf("batch sizes = %v, want %v", result.BatchSizes, wantSizes)
	}
	if len(result.Batches) != len(wantSizes) {
		t.Fatalf("batches = %d, want %d", len(result.Batches), len(wantSizes))
	}
	next := 1
	for batchIndex, batch := range result.Batches {
		if batch.Number != batchIndex+1 || len(batch.RegistryIndexes) != wantSizes[batchIndex] {
			t.Fatalf("batch %d = %#v", batchIndex+1, batch)
		}
		for _, registryIndex := range batch.RegistryIndexes {
			if registryIndex != next {
				t.Fatalf("batch order registry index = %d, want %d", registryIndex, next)
			}
			next++
		}
	}
	if reconcileCalls != len(wantSizes)+1 {
		t.Fatalf("reconcile calls = %d, want one per persisted batch plus final (%d)", reconcileCalls, len(wantSizes)+1)
	}
	if result.FinalReconciliation.Status != builtinPackStatusSuccess || result.AccountData.Status != builtinPackStatusSuccess {
		t.Fatalf("final statuses = %#v / %#v", result.FinalReconciliation, result.AccountData)
	}
}

func TestBuiltinEmojiImportSkipsCanonicalEntriesWithoutConsumingBatchCapacityAndAlwaysReconciles(t *testing.T) {
	entries := textemoji.Entries()
	for _, test := range []struct {
		name      string
		seed      int
		wantSizes []int
	}{
		{name: "all skipped", seed: 109, wantSizes: nil},
		{name: "five skipped", seed: 5, wantSizes: []int{16, 16, 16, 16, 16, 16, 8}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newTestBuiltinEmojiImportStore()
			store.seed(entries[:test.seed])
			workers := &testBuiltinEmojiImportWorkerCounts{}
			var fetches atomic.Int32
			var uploads atomic.Int32
			var reconciles atomic.Int32
			importer := &builtinEmojiImporter{
				fetcher: testBuiltinEmojiFetcherFunc(func(_ context.Context, entry textemoji.Entry) ([]byte, error) {
					fetches.Add(1)
					return []byte(strconv.Itoa(entry.Index)), nil
				}),
				uploader: testBuiltinEmojiUploaderFunc(func(_ context.Context, data []byte, _, _ string) (id.ContentURIString, error) {
					uploads.Add(1)
					index, _ := strconv.Atoi(string(data))
					return id.ContentURIString(testBuiltinEmojiMXC(index, "skip")), nil
				}),
				storeView: store, storePersister: store, packLock: &testBuiltinEmojiImportLocker{}, operationTimeout: time.Second, hooks: workers.hooks(),
				reconcile: func(context.Context) (reconciliationResult, accountDataVisibilityResult) {
					reconciles.Add(1)
					return reconciliationResult{Status: builtinPackStatusSuccess}, accountDataVisibilityResult{Status: builtinPackStatusSkipped}
				},
			}
			result := importer.Run(context.Background())
			wantImported := 109 - test.seed
			if result.TotalConsidered != 109 || result.Imported != wantImported || result.Persisted != wantImported || result.Skipped != test.seed {
				t.Fatalf("counts = %#v", result)
			}
			if fetches.Load() != int32(wantImported) || uploads.Load() != int32(wantImported) {
				t.Fatalf("fetch/upload calls = %d/%d, want %d/%d", fetches.Load(), uploads.Load(), wantImported, wantImported)
			}
			if !reflect.DeepEqual(result.BatchSizes, test.wantSizes) {
				t.Fatalf("batch sizes = %v, want %v", result.BatchSizes, test.wantSizes)
			}
			if reconciles.Load() != int32(len(test.wantSizes)+1) {
				t.Fatalf("reconciles = %d, want %d", reconciles.Load(), len(test.wantSizes)+1)
			}
			if workers.started.Load() != 4 || workers.stopped.Load() != 4 {
				t.Fatalf("workers started/stopped = %d/%d", workers.started.Load(), workers.stopped.Load())
			}
		})
	}
}

func TestBuiltinEmojiImportReportsSortedBoundedPartialFailuresTimeoutsAndWorkerPanic(t *testing.T) {
	store := newTestBuiltinEmojiImportStore()
	workers := &testBuiltinEmojiImportWorkerCounts{}
	secret := "UPSTREAM_SECRET_BODY_TOKEN"
	fetcher := testBuiltinEmojiFetcherFunc(func(ctx context.Context, entry textemoji.Entry) ([]byte, error) {
		switch entry.Index {
		case 1:
			return nil, errors.New(secret)
		case 3:
			panic(secret)
		case 4:
			<-ctx.Done()
			return nil, fmt.Errorf("%s: %w", secret, ctx.Err())
		default:
			return []byte(strconv.Itoa(entry.Index)), nil
		}
	})
	uploader := testBuiltinEmojiUploaderFunc(func(ctx context.Context, data []byte, _, _ string) (id.ContentURIString, error) {
		index, _ := strconv.Atoi(string(data))
		switch index {
		case 2:
			return "", errors.New(secret)
		case 5:
			<-ctx.Done()
			return "", fmt.Errorf("%s: %w", secret, ctx.Err())
		default:
			return id.ContentURIString(testBuiltinEmojiMXC(index, "partial")), nil
		}
	})
	importer := &builtinEmojiImporter{
		fetcher: fetcher, uploader: uploader, storeView: store, storePersister: store, packLock: &testBuiltinEmojiImportLocker{},
		operationTimeout: 10 * time.Millisecond, hooks: workers.hooks(),
		reconcile: func(context.Context) (reconciliationResult, accountDataVisibilityResult) {
			return reconciliationResult{Status: builtinPackStatusFailed, Warnings: []string{secret}}, accountDataVisibilityResult{Status: builtinPackStatusFailed, Warnings: []string{secret}}
		},
	}
	result := importer.Run(context.Background())
	want := []builtinEmojiImportFailure{
		{RegistryIndex: 1, Shortcode: "wechat_smile", Stage: builtinEmojiImportStageFetch, Code: builtinEmojiImportCodeFetchFailed},
		{RegistryIndex: 2, Shortcode: "wechat_grimace", Stage: builtinEmojiImportStageUpload, Code: builtinEmojiImportCodeUploadFailed},
		{RegistryIndex: 3, Shortcode: "wechat_drool", Stage: builtinEmojiImportStageWorker, Code: builtinEmojiImportCodeWorkerPanic},
		{RegistryIndex: 4, Shortcode: "wechat_scowl", Stage: builtinEmojiImportStageFetch, Code: builtinEmojiImportCodeOperationTimeout},
		{RegistryIndex: 5, Shortcode: "wechat_chill", Stage: builtinEmojiImportStageUpload, Code: builtinEmojiImportCodeOperationTimeout},
	}
	if !reflect.DeepEqual(result.Failures, want) {
		t.Fatalf("failures = %#v, want %#v", result.Failures, want)
	}
	if result.Imported != 104 || result.Persisted != 104 || result.Uploaded != 104 || result.Skipped != 0 {
		t.Fatalf("partial counts = %#v", result)
	}
	if strings.Contains(fmt.Sprintf("%#v", result), secret) {
		t.Fatal("result leaked arbitrary fetch/upload/reconciliation error text")
	}
	if result.FinalReconciliation.Status != builtinPackStatusFailed || result.AccountData.Status != builtinPackStatusFailed {
		t.Fatalf("final failure statuses not retained: %#v", result)
	}
	if workers.started.Load() != 4 || workers.stopped.Load() != 4 {
		t.Fatalf("workers started/stopped = %d/%d", workers.started.Load(), workers.stopped.Load())
	}
}

func TestBuiltinEmojiImportFetchAndUploadUseIndependentChildTimeouts(t *testing.T) {
	store := newTestBuiltinEmojiImportStore()
	const operationTimeout = 100 * time.Millisecond
	fetcher := testBuiltinEmojiFetcherFunc(func(ctx context.Context, entry textemoji.Entry) ([]byte, error) {
		if entry.Index == 1 {
			select {
			case <-time.After(60 * time.Millisecond):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return []byte(strconv.Itoa(entry.Index)), nil
	})
	uploader := testBuiltinEmojiUploaderFunc(func(ctx context.Context, data []byte, _, _ string) (id.ContentURIString, error) {
		index, _ := strconv.Atoi(string(data))
		if index == 1 {
			if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) < 80*time.Millisecond {
				return "", errors.New("upload did not receive a fresh child timeout")
			}
			select {
			case <-time.After(60 * time.Millisecond):
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		return id.ContentURIString(testBuiltinEmojiMXC(index, "independent")), nil
	})
	importer := &builtinEmojiImporter{
		fetcher: fetcher, uploader: uploader, storeView: store, storePersister: store, packLock: &testBuiltinEmojiImportLocker{}, operationTimeout: operationTimeout,
		reconcile: func(context.Context) (reconciliationResult, accountDataVisibilityResult) {
			return reconciliationResult{Status: builtinPackStatusSuccess}, accountDataVisibilityResult{Status: builtinPackStatusSuccess}
		},
	}
	result := importer.Run(context.Background())
	if len(result.Failures) != 0 || result.Imported != 109 {
		t.Fatalf("independent child contexts result = %#v", result)
	}
}

func TestBuiltinEmojiImportPersistenceFailureCancelsDispatchDrainsAndRetrySkipsPersisted(t *testing.T) {
	store := newTestBuiltinEmojiImportStore()
	store.failAt = 2
	workers := &testBuiltinEmojiImportWorkerCounts{}
	var dispatchesAfterFailure atomic.Int32
	fetcher := testBuiltinEmojiFetcherFunc(func(ctx context.Context, entry textemoji.Entry) ([]byte, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		return []byte(strconv.Itoa(entry.Index)), nil
	})
	uploader := testBuiltinEmojiUploaderFunc(func(ctx context.Context, data []byte, _, _ string) (id.ContentURIString, error) {
		index, _ := strconv.Atoi(string(data))
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}
		return id.ContentURIString(testBuiltinEmojiMXC(index, "retry")), nil
	})
	var reconcileCalls atomic.Int32
	hooks := workers.hooks()
	hooks.JobDispatched = func(int) {
		if store.failed.Load() {
			dispatchesAfterFailure.Add(1)
		}
	}
	importer := &builtinEmojiImporter{
		fetcher: fetcher, uploader: uploader, storeView: store, storePersister: store, packLock: &testBuiltinEmojiImportLocker{}, operationTimeout: time.Second, hooks: hooks,
		reconcile: func(context.Context) (reconciliationResult, accountDataVisibilityResult) {
			reconcileCalls.Add(1)
			return reconciliationResult{Status: builtinPackStatusSuccess}, accountDataVisibilityResult{Status: builtinPackStatusSuccess}
		},
	}
	first := importer.Run(context.Background())
	if first.Persisted != 16 || first.Imported != 16 || store.count() != 16 || first.Persistence != builtinEmojiPersistenceFailed {
		t.Fatalf("first partial persistence = %#v, store=%d", first, store.count())
	}
	if dispatchesAfterFailure.Load() != 0 {
		t.Fatalf("jobs dispatched after persistence failure = %d", dispatchesAfterFailure.Load())
	}
	if first.Uploaded-first.Persisted != len(first.UploadedUnreferenced) || len(first.UploadedUnreferenced) < 16 {
		t.Fatalf("uploaded/unreferenced accounting = uploaded %d persisted %d unreferenced %d", first.Uploaded, first.Persisted, len(first.UploadedUnreferenced))
	}
	if first.TotalConsidered != first.Imported+first.Skipped+len(first.Failures) {
		t.Fatalf("first result does not account for all entries: %#v", first)
	}
	if workers.started.Load() != 4 || workers.stopped.Load() != 4 {
		t.Fatalf("workers started/stopped = %d/%d", workers.started.Load(), workers.stopped.Load())
	}
	if reconcileCalls.Load() != 2 {
		t.Fatalf("reconciles = %d, want successful first batch plus final", reconcileCalls.Load())
	}

	store.resetPersistenceFailure()
	var retryFetches atomic.Int32
	var persistedFetches atomic.Int32
	retry := &builtinEmojiImporter{
		fetcher: testBuiltinEmojiFetcherFunc(func(_ context.Context, entry textemoji.Entry) ([]byte, error) {
			retryFetches.Add(1)
			if entry.Index <= 16 {
				persistedFetches.Add(1)
			}
			return []byte(strconv.Itoa(entry.Index)), nil
		}),
		uploader: uploader, storeView: store, storePersister: store, packLock: &testBuiltinEmojiImportLocker{}, operationTimeout: time.Second,
		reconcile: func(context.Context) (reconciliationResult, accountDataVisibilityResult) {
			return reconciliationResult{Status: builtinPackStatusSuccess}, accountDataVisibilityResult{Status: builtinPackStatusSuccess}
		},
	}
	second := retry.Run(context.Background())
	if retryFetches.Load() != 93 || persistedFetches.Load() != 0 {
		t.Fatalf("retry fetches = %d, persisted redownloads = %d, want 93/0", retryFetches.Load(), persistedFetches.Load())
	}
	if second.Skipped != 16 || second.Imported != 93 || second.Persisted != 93 || store.count() != 109 {
		t.Fatalf("retry result = %#v, store=%d", second, store.count())
	}
	if !reflect.DeepEqual(second.BatchSizes, []int{16, 16, 16, 16, 16, 13}) {
		t.Fatalf("retry batch sizes = %v", second.BatchSizes)
	}
}

func TestBuiltinEmojiImportParentCancellationStopsDispatchDrainsAndReportsUploads(t *testing.T) {
	store := newTestBuiltinEmojiImportStore()
	workers := &testBuiltinEmojiImportWorkerCounts{}
	started := make(chan int, 8)
	uploaded := make(chan struct{})
	var uploadOnce sync.Once
	fetcher := testBuiltinEmojiFetcherFunc(func(ctx context.Context, entry textemoji.Entry) ([]byte, error) {
		started <- entry.Index
		if entry.Index != 1 {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return []byte("1"), nil
	})
	uploader := testBuiltinEmojiUploaderFunc(func(_ context.Context, _ []byte, _, _ string) (id.ContentURIString, error) {
		uploadOnce.Do(func() { close(uploaded) })
		return id.ContentURIString(testBuiltinEmojiMXC(1, "cancel")), nil
	})
	var reconciles atomic.Int32
	importer := &builtinEmojiImporter{
		fetcher: fetcher, uploader: uploader, storeView: store, storePersister: store, packLock: &testBuiltinEmojiImportLocker{}, operationTimeout: time.Second, hooks: workers.hooks(),
		reconcile: func(ctx context.Context) (reconciliationResult, accountDataVisibilityResult) {
			reconciles.Add(1)
			status := builtinPackStatusSuccess
			if ctx.Err() != nil {
				status = builtinPackStatusFailed
			}
			return reconciliationResult{Status: status}, accountDataVisibilityResult{Status: status}
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan builtinEmojiImportResult, 1)
	go func() { done <- importer.Run(ctx) }()
	select {
	case <-uploaded:
		cancel()
	case <-time.After(time.Second):
		t.Fatal("first upload did not start")
	}
	var result builtinEmojiImportResult
	select {
	case result = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled import deadlocked")
	}
	if result.Persisted != 0 || result.Imported != 0 || result.Uploaded != 1 || len(result.UploadedUnreferenced) != 1 {
		t.Fatalf("cancelled upload accounting = %#v", result)
	}
	if result.TotalConsidered != result.Imported+result.Skipped+len(result.Failures) || result.TotalConsidered != 109 {
		t.Fatalf("cancelled result does not deterministically account for registry: %#v", result)
	}
	if result.Persistence != builtinEmojiPersistenceCanceled {
		t.Fatalf("persistence status = %q, want canceled", result.Persistence)
	}
	if reconciles.Load() != 0 || result.FinalReconciliation.Status != builtinPackStatusSkipped || result.AccountData.Status != builtinPackStatusSkipped {
		t.Fatalf("canceled import unexpectedly reconciled = calls %d, %#v", reconciles.Load(), result)
	}
	if workers.started.Load() != 4 || workers.stopped.Load() != 4 {
		t.Fatalf("workers started/stopped = %d/%d", workers.started.Load(), workers.stopped.Load())
	}
	if len(started) >= 109 {
		t.Fatalf("cancellation failed to stop dispatch: started=%d", len(started))
	}
	assertBuiltinEmojiImportFailuresSorted(t, result.Failures)
	assertBuiltinEmojiImportUnreferencedSorted(t, result.UploadedUnreferenced)
}

func TestBuiltinEmojiImportPostCancellationDispatchDoesNotStartExternalWork(t *testing.T) {
	const iterations = 1000
	for _, testCase := range []struct {
		name         string
		cancelOnCall int32
	}{
		{name: "before_select", cancelOnCall: 2},
		{name: "after_send", cancelOnCall: 3},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			for iteration := range iterations {
				parent, cancel := context.WithCancel(context.Background())
				workersReady := make(chan struct{})
				var workersStarted atomic.Int32
				ctx := &testBuiltinEmojiCancelOnErrContext{
					Context: parent, cancel: cancel, cancelOnCall: testCase.cancelOnCall, workersReady: workersReady,
				}
				var dispatched atomic.Int32
				var jobStarted atomic.Int32
				var fetched atomic.Int32
				importer := &builtinEmojiImporter{
					fetcher: testBuiltinEmojiFetcherFunc(func(context.Context, textemoji.Entry) ([]byte, error) {
						fetched.Add(1)
						return nil, errors.New("unexpected fetch")
					}),
					operationTimeout: time.Second,
					hooks: builtinEmojiImportHooks{
						WorkerStarted: func(int) {
							if workersStarted.Add(1) == builtinEmojiImportWorkerCount {
								close(workersReady)
							}
						},
						JobDispatched: func(int) { dispatched.Add(1) },
						JobStarted:    func(int, int) { jobStarted.Add(1) },
					},
				}

				result := importer.Run(ctx)
				cancel()
				if dispatched.Load() != 0 || jobStarted.Load() != 0 || fetched.Load() != 0 {
					t.Fatalf("iteration %d started work after cancellation: dispatched=%d job_started=%d fetched=%d", iteration, dispatched.Load(), jobStarted.Load(), fetched.Load())
				}
				if len(result.Failures) != result.TotalConsidered || result.Persistence != builtinEmojiPersistenceCanceled {
					t.Fatalf("iteration %d cancellation accounting = %#v", iteration, result)
				}
				assertBuiltinEmojiImportFailuresSorted(t, result.Failures)
			}
		})
	}
}

func TestBuiltinEmojiImportCancellationWithLargeBufferedGapDoesNotCommitOrDispatch(t *testing.T) {
	store := newTestBuiltinEmojiImportStore()
	gapRelease := make(chan struct{})
	parked := make(chan int, 3)
	fetcher := testBuiltinEmojiFetcherFunc(func(ctx context.Context, entry textemoji.Entry) ([]byte, error) {
		switch {
		case entry.Index == 1:
			<-gapRelease
		case entry.Index >= 41 && entry.Index <= 43:
			parked <- entry.Index
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return []byte(strconv.Itoa(entry.Index)), nil
	})
	uploader := testBuiltinEmojiUploaderFunc(func(_ context.Context, data []byte, _, _ string) (id.ContentURIString, error) {
		index, _ := strconv.Atoi(string(data))
		return id.ContentURIString(testBuiltinEmojiMXC(index, "gap-cancel")), nil
	})
	var dispatches atomic.Int32
	importer := &builtinEmojiImporter{
		fetcher: fetcher, uploader: uploader, storeView: store, storePersister: store,
		packLock: &testBuiltinEmojiImportLocker{}, operationTimeout: time.Second,
		hooks: builtinEmojiImportHooks{JobDispatched: func(int) { dispatches.Add(1) }},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan builtinEmojiImportResult, 1)
	go func() { done <- importer.Run(ctx) }()
	for range 3 {
		select {
		case <-parked:
		case <-time.After(time.Second):
			t.Fatal("workers did not build the buffered outcome gap")
		}
	}
	cancel()
	dispatchedAtCancel := dispatches.Load()
	close(gapRelease)
	var result builtinEmojiImportResult
	select {
	case result = <-done:
	case <-time.After(time.Second):
		t.Fatal("buffered cancellation did not return")
	}
	if store.persistenceCalls() != 0 || result.Persisted != 0 {
		t.Fatalf("cancellation persisted buffered outcomes: calls=%d result=%#v", store.persistenceCalls(), result)
	}
	if dispatches.Load() != dispatchedAtCancel {
		t.Fatalf("jobs dispatched after cancellation = %d", dispatches.Load()-dispatchedAtCancel)
	}
	if len(result.UploadedUnreferenced) != result.Uploaded {
		t.Fatalf("uploaded/unpersisted accounting = %d/%d", result.Uploaded, len(result.UploadedUnreferenced))
	}
}

func TestBuiltinEmojiImportCancellationWhilePackLockContendedDoesNotPersistOrDispatch(t *testing.T) {
	store := newTestBuiltinEmojiImportStore()
	var packMutex sync.Mutex
	packMutex.Lock()
	defer packMutex.Unlock()
	locker := &testBuiltinEmojiImportBlockingLocker{inner: newBuiltinEmojiImportMutexLocker(&packMutex), attempted: make(chan struct{})}
	var dispatches atomic.Int32
	importer := &builtinEmojiImporter{
		fetcher: testBuiltinEmojiFetcherFunc(func(_ context.Context, entry textemoji.Entry) ([]byte, error) {
			return []byte(strconv.Itoa(entry.Index)), nil
		}),
		uploader: testBuiltinEmojiUploaderFunc(func(_ context.Context, data []byte, _, _ string) (id.ContentURIString, error) {
			index, _ := strconv.Atoi(string(data))
			return id.ContentURIString(testBuiltinEmojiMXC(index, "lock-cancel")), nil
		}),
		storeView: store, storePersister: store, packLock: locker, operationTimeout: time.Second,
		hooks: builtinEmojiImportHooks{JobDispatched: func(int) { dispatches.Add(1) }},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan builtinEmojiImportResult, 1)
	go func() { done <- importer.Run(ctx) }()
	select {
	case <-locker.attempted:
	case <-time.After(time.Second):
		t.Fatal("importer did not attempt the contended pack lock")
	}
	cancel()
	dispatchedAtCancel := dispatches.Load()
	var result builtinEmojiImportResult
	select {
	case result = <-done:
	case <-time.After(time.Second):
		t.Fatal("pack-lock cancellation did not return")
	}
	if store.persistenceCalls() != 0 || result.Persisted != 0 {
		t.Fatalf("pack-lock cancellation persisted: calls=%d result=%#v", store.persistenceCalls(), result)
	}
	if dispatches.Load() != dispatchedAtCancel {
		t.Fatalf("jobs dispatched after pack-lock cancellation = %d", dispatches.Load()-dispatchedAtCancel)
	}
	if len(result.UploadedUnreferenced) != result.Uploaded {
		t.Fatalf("pack-lock uploaded/unpersisted accounting = %d/%d", result.Uploaded, len(result.UploadedUnreferenced))
	}
}

func TestBuiltinEmojiImportCancellationDuringReconciliationPreservesBatchAndStops(t *testing.T) {
	store := newTestBuiltinEmojiImportStore()
	reconcileStarted := make(chan struct{})
	var reconcileOnce sync.Once
	var reconciles atomic.Int32
	var dispatches atomic.Int32
	importer := &builtinEmojiImporter{
		fetcher: testBuiltinEmojiFetcherFunc(func(_ context.Context, entry textemoji.Entry) ([]byte, error) {
			return []byte(strconv.Itoa(entry.Index)), nil
		}),
		uploader: testBuiltinEmojiUploaderFunc(func(_ context.Context, data []byte, _, _ string) (id.ContentURIString, error) {
			index, _ := strconv.Atoi(string(data))
			return id.ContentURIString(testBuiltinEmojiMXC(index, "reconcile-cancel")), nil
		}),
		storeView: store, storePersister: store, packLock: &testBuiltinEmojiImportLocker{}, operationTimeout: time.Second,
		hooks: builtinEmojiImportHooks{JobDispatched: func(int) { dispatches.Add(1) }},
		reconcile: func(ctx context.Context) (reconciliationResult, accountDataVisibilityResult) {
			reconciles.Add(1)
			reconcileOnce.Do(func() { close(reconcileStarted) })
			<-ctx.Done()
			return reconciliationResult{Status: builtinPackStatusFailed}, accountDataVisibilityResult{Status: builtinPackStatusFailed}
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan builtinEmojiImportResult, 1)
	go func() { done <- importer.Run(ctx) }()
	select {
	case <-reconcileStarted:
	case <-time.After(time.Second):
		t.Fatal("first batch did not reach reconciliation")
	}
	cancel()
	dispatchedAtCancel := dispatches.Load()
	var result builtinEmojiImportResult
	select {
	case result = <-done:
	case <-time.After(time.Second):
		t.Fatal("reconciliation cancellation did not return")
	}
	if store.persistenceCalls() != 1 || result.Persisted != builtinEmojiImportBatchSize || result.Imported != builtinEmojiImportBatchSize {
		t.Fatalf("persisted batch not preserved: calls=%d result=%#v", store.persistenceCalls(), result)
	}
	if reconciles.Load() != 1 {
		t.Fatalf("reconciliation calls = %d, want no final reconciliation", reconciles.Load())
	}
	if dispatches.Load() != dispatchedAtCancel {
		t.Fatalf("jobs dispatched after reconciliation cancellation = %d", dispatches.Load()-dispatchedAtCancel)
	}
	if len(result.UploadedUnreferenced) != result.Uploaded-result.Persisted {
		t.Fatalf("reconciliation cancellation uploaded accounting = %#v", result)
	}
	if result.Persistence != builtinEmojiPersistencePartial {
		t.Fatalf("persistence status = %q, want partial", result.Persistence)
	}
}

func TestBuiltinEmojiImportUsesInjectedClockForChildTimeoutsAndTimestamps(t *testing.T) {
	entries := textemoji.Entries()
	store := newTestBuiltinEmojiImportStore()
	store.seed(entries[1:])
	clock := &testBuiltinEmojiImportClock{now: time.Date(2026, time.August, 3, 10, 0, 0, 0, time.UTC)}
	importer := &builtinEmojiImporter{
		fetcher: testBuiltinEmojiFetcherFunc(func(_ context.Context, entry textemoji.Entry) ([]byte, error) {
			return []byte(strconv.Itoa(entry.Index)), nil
		}),
		uploader: testBuiltinEmojiUploaderFunc(func(_ context.Context, data []byte, _, _ string) (id.ContentURIString, error) {
			index, _ := strconv.Atoi(string(data))
			return id.ContentURIString(testBuiltinEmojiMXC(index, "clock")), nil
		}),
		storeView: store, storePersister: store, packLock: &testBuiltinEmojiImportLocker{},
		operationTimeout: 77 * time.Second, clock: clock,
	}
	result := importer.Run(context.Background())
	nowCalls, childDurations := clock.snapshot()
	if nowCalls != 2 || result.StartedAt != clock.now || result.CompletedAt != clock.now.Add(time.Second) {
		t.Fatalf("injected timestamps = %s/%s calls=%d", result.StartedAt, result.CompletedAt, nowCalls)
	}
	if !reflect.DeepEqual(childDurations, []time.Duration{77 * time.Second, 77 * time.Second}) {
		t.Fatalf("child timeout calls = %v", childDurations)
	}
}

func TestBuiltinEmojiImportProgressNoticesAreOrderedSafeAndOutsidePackLock(t *testing.T) {
	entries := textemoji.Entries()
	store := newTestBuiltinEmojiImportStore()
	store.seed(entries[17:])
	locker := &testBuiltinEmojiImportLocker{}
	var notices []builtinEmojiImportProgress
	importer := &builtinEmojiImporter{
		fetcher: testBuiltinEmojiFetcherFunc(func(_ context.Context, entry textemoji.Entry) ([]byte, error) {
			return []byte(strconv.Itoa(entry.Index)), nil
		}),
		uploader: testBuiltinEmojiUploaderFunc(func(_ context.Context, data []byte, _, _ string) (id.ContentURIString, error) {
			index, _ := strconv.Atoi(string(data))
			return id.ContentURIString(testBuiltinEmojiMXC(index, "notice")), nil
		}),
		storeView: store, storePersister: store, packLock: locker, operationTimeout: time.Second,
		reconcile: func(context.Context) (reconciliationResult, accountDataVisibilityResult) {
			return reconciliationResult{Status: builtinPackStatusSuccess, ReconciledRooms: 2}, accountDataVisibilityResult{Status: builtinPackStatusSuccess}
		},
		notice: func(progress builtinEmojiImportProgress) {
			if locker.locked.Load() {
				t.Error("progress notice invoked while pack lock held")
			}
			notices = append(notices, progress)
		},
	}
	result := importer.Run(context.Background())
	if result.ProgressNoticeFailed {
		t.Fatal("safe progress callback was reported failed")
	}
	if len(notices) != 3 {
		t.Fatalf("progress notices = %#v, want two batches and final reconciliation", notices)
	}
	if notices[0].Kind != builtinEmojiImportProgressBatchCommitted || notices[0].BatchNumber != 1 || notices[0].Persisted != 16 ||
		notices[1].Kind != builtinEmojiImportProgressBatchCommitted || notices[1].BatchNumber != 2 || notices[1].Persisted != 1 ||
		notices[2].Kind != builtinEmojiImportProgressFinalReconciliation || notices[2].TotalPersisted != 17 || notices[2].ReconciledRooms != 2 {
		t.Fatalf("ordered progress notices = %#v", notices)
	}
}

func TestBuiltinEmojiImportProgressNoticePanicBecomesBoundedFailure(t *testing.T) {
	entries := textemoji.Entries()
	store := newTestBuiltinEmojiImportStore()
	store.seed(entries[1:])
	secret := "NOTICE_SECRET_PANIC_TOKEN"
	var noticeCalls atomic.Int32
	importer := &builtinEmojiImporter{
		fetcher: testBuiltinEmojiFetcherFunc(func(_ context.Context, entry textemoji.Entry) ([]byte, error) {
			return []byte(strconv.Itoa(entry.Index)), nil
		}),
		uploader: testBuiltinEmojiUploaderFunc(func(_ context.Context, data []byte, _, _ string) (id.ContentURIString, error) {
			index, _ := strconv.Atoi(string(data))
			return id.ContentURIString(testBuiltinEmojiMXC(index, "notice-panic")), nil
		}),
		storeView: store, storePersister: store, packLock: &testBuiltinEmojiImportLocker{}, operationTimeout: time.Second,
		notice: func(builtinEmojiImportProgress) {
			noticeCalls.Add(1)
			panic(secret)
		},
	}
	result := importer.Run(context.Background())
	if !result.ProgressNoticeFailed || result.Imported != 1 || noticeCalls.Load() != 1 {
		t.Fatalf("notice panic handling = calls=%d result=%#v", noticeCalls.Load(), result)
	}
	if strings.Contains(fmt.Sprintf("%#v", result), secret) {
		t.Fatal("notice panic payload leaked into result")
	}
}

func TestBuiltinEmojiImportRejectsInvalidUploaderMXCWithSafeCode(t *testing.T) {
	entries := textemoji.Entries()
	for _, returnedMXC := range []id.ContentURIString{"", "not-an-mxc", "mxc://bad host/media", "mxc://example.org/bad/path"} {
		t.Run(string(returnedMXC), func(t *testing.T) {
			store := newTestBuiltinEmojiImportStore()
			store.seed(entries[1:])
			importer := &builtinEmojiImporter{
				fetcher: testBuiltinEmojiFetcherFunc(func(_ context.Context, entry textemoji.Entry) ([]byte, error) {
					return []byte(strconv.Itoa(entry.Index)), nil
				}),
				uploader: testBuiltinEmojiUploaderFunc(func(context.Context, []byte, string, string) (id.ContentURIString, error) {
					return returnedMXC, nil
				}),
				storeView: store, storePersister: store, packLock: &testBuiltinEmojiImportLocker{}, operationTimeout: time.Second,
			}
			result := importer.Run(context.Background())
			want := []builtinEmojiImportFailure{{RegistryIndex: 1, Shortcode: entries[0].Shortcode, Stage: builtinEmojiImportStageUpload, Code: builtinEmojiImportCodeInvalidMXC}}
			if !reflect.DeepEqual(result.Failures, want) || result.Persisted != 0 || store.persistenceCalls() != 0 {
				t.Fatalf("invalid MXC %q result = %#v", returnedMXC, result)
			}
			if strings.Contains(fmt.Sprintf("%#v", result), string(returnedMXC)) && returnedMXC != "" {
				t.Fatal("invalid MXC value leaked into bounded result")
			}
		})
	}
}

func TestBuiltinEmojiProductionUploaderValidatesResponseMXC(t *testing.T) {
	valid := id.ContentURI{Homeserver: "example.org", FileID: "emoji_1"}
	for _, test := range []struct {
		name     string
		response *mautrix.RespMediaUpload
		want     id.ContentURIString
		wantErr  bool
	}{
		{name: "nil response", response: nil, wantErr: true},
		{name: "empty MXC", response: &mautrix.RespMediaUpload{}, wantErr: true},
		{name: "malformed MXC", response: &mautrix.RespMediaUpload{ContentURI: id.ContentURI{Homeserver: "bad host", FileID: "emoji"}}, wantErr: true},
		{name: "invalid MXC", response: &mautrix.RespMediaUpload{ContentURI: id.ContentURI{Homeserver: "example.org", FileID: "bad/path"}}, wantErr: true},
		{name: "valid response", response: &mautrix.RespMediaUpload{ContentURI: valid}, want: valid.CUString()},
	} {
		t.Run(test.name, func(t *testing.T) {
			uploader := &serverBuiltinEmojiUploader{upload: func(context.Context, []byte, string, string) (*mautrix.RespMediaUpload, error) {
				return test.response, nil
			}}
			got, err := uploader.Upload(context.Background(), []byte("safe"), builtinEmojiImportMIMEType, "emoji.webp")
			if (err != nil) != test.wantErr || got != test.want {
				t.Fatalf("Upload() = %q, %v; want %q, error=%v", got, err, test.want, test.wantErr)
			}
		})
	}
}

func TestBuiltinEmojiImportMutexLockerCancelsWhileUnderlyingMutexHeld(t *testing.T) {
	var mutex sync.Mutex
	mutex.Lock()
	locker := newBuiltinEmojiImportMutexLocker(&mutex)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- locker.Lock(ctx) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Lock() error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("context-aware mutex lock did not return on cancellation")
	}
	mutex.Unlock()
}

func TestBuiltinEmojiImportRecheckRaceSkipsPersistenceAndReportsUploadedUnreferenced(t *testing.T) {
	entries := textemoji.Entries()
	store := newTestBuiltinEmojiImportStore()
	store.raceShortcode = entries[0].Shortcode
	store.raceRegistryIndex = entries[0].Index
	importer := &builtinEmojiImporter{
		fetcher: testBuiltinEmojiFetcherFunc(func(_ context.Context, entry textemoji.Entry) ([]byte, error) {
			return []byte(strconv.Itoa(entry.Index)), nil
		}),
		uploader: testBuiltinEmojiUploaderFunc(func(_ context.Context, data []byte, _, _ string) (id.ContentURIString, error) {
			index, _ := strconv.Atoi(string(data))
			return id.ContentURIString(testBuiltinEmojiMXC(index, "race-upload")), nil
		}),
		storeView: store, storePersister: store, packLock: &testBuiltinEmojiImportLocker{}, operationTimeout: time.Second,
		reconcile: func(context.Context) (reconciliationResult, accountDataVisibilityResult) {
			return reconciliationResult{Status: builtinPackStatusSuccess}, accountDataVisibilityResult{Status: builtinPackStatusSuccess}
		},
	}
	result := importer.Run(context.Background())
	if result.Uploaded != 109 || result.Imported != 108 || result.Persisted != 108 || result.Skipped != 1 || len(result.Failures) != 0 {
		t.Fatalf("race result = %#v", result)
	}
	if len(result.UploadedUnreferenced) != 1 || result.UploadedUnreferenced[0].RegistryIndex != 1 || result.UploadedUnreferenced[0].Reason != builtinEmojiImportCodeRecheckRace {
		t.Fatalf("race unreferenced = %#v", result.UploadedUnreferenced)
	}
	if result.Batches[0].Uploaded != 16 || result.Batches[0].Persisted != 15 || result.Batches[0].Skipped != 1 {
		t.Fatalf("race first batch = %#v", result.Batches[0])
	}
	if store.count() != 109 {
		t.Fatalf("race store count = %d, want 109", store.count())
	}
}

func TestBuiltinEmojiImportProductionServerBuilderWiresFetcherUploaderStoreLockAndReconciliation(t *testing.T) {
	store := newBuiltinEmojiStore(t.TempDir() + "/builtin.json")
	server := &Server{builtinEmojiStore: store}
	importer := server.newBuiltinEmojiImporter()
	if importer == nil {
		t.Fatal("production importer is nil")
	}
	if _, ok := importer.fetcher.(*githubBuiltinEmojiFetcher); !ok {
		t.Fatalf("production fetcher = %T, want *githubBuiltinEmojiFetcher", importer.fetcher)
	}
	uploader, ok := importer.uploader.(*serverBuiltinEmojiUploader)
	if !ok || uploader.server != server {
		t.Fatalf("production uploader = %#v, want BotIntent-backed server uploader", importer.uploader)
	}
	if importer.storeView != store || importer.storePersister != store {
		t.Fatal("production importer does not use canonical builtinEmojiStore for view and persistence")
	}
	packLocker, ok := importer.packLock.(*builtinEmojiImportMutexLocker)
	if !ok || packLocker.mutex != &server.packMu || importer.reconcile == nil {
		t.Fatal("production importer does not wrap Server packMu and existing reconciliation")
	}
	if _, ok = importer.clock.(systemBuiltinEmojiImportContextClock); !ok {
		t.Fatalf("production importer clock = %T, want system clock/context", importer.clock)
	}
	if importer.operationTimeout != 30*time.Second {
		t.Fatalf("production operation timeout = %s, want 30s", importer.operationTimeout)
	}
}

func assertBuiltinEmojiImportFailuresSorted(t *testing.T, failures []builtinEmojiImportFailure) {
	t.Helper()
	for index := 1; index < len(failures); index++ {
		if failures[index-1].RegistryIndex > failures[index].RegistryIndex {
			t.Fatalf("failures not sorted: %#v", failures)
		}
	}
}

func assertBuiltinEmojiImportUnreferencedSorted(t *testing.T, uploads []builtinEmojiUploadedUnreferenced) {
	t.Helper()
	for index := 1; index < len(uploads); index++ {
		if uploads[index-1].RegistryIndex > uploads[index].RegistryIndex {
			t.Fatalf("unreferenced uploads not sorted: %#v", uploads)
		}
	}
}

func testBuiltinEmojiRegistryIndex(shortcode string) int {
	for _, entry := range textemoji.Entries() {
		if entry.Shortcode == shortcode {
			return entry.Index
		}
	}
	return 0
}

func testBuiltinEmojiMXC(index int, suffix string) string {
	return fmt.Sprintf("mxc://example.org/emoji_%03d_%s", index, suffix)
}
