package matrixas

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"github.com/leat/mautrix-go-wechat/internal/wechat/textemoji"
)

const (
	builtinEmojiImportWorkerCount      = 4
	builtinEmojiImportBatchSize        = 16
	builtinEmojiImportOperationTimeout = 30 * time.Second
	builtinEmojiImportMIMEType         = "image/webp"
)

type builtinEmojiImportStage string

const (
	builtinEmojiImportStageImport    builtinEmojiImportStage = "import"
	builtinEmojiImportStageFetch     builtinEmojiImportStage = "fetch"
	builtinEmojiImportStageUpload    builtinEmojiImportStage = "upload"
	builtinEmojiImportStagePersist   builtinEmojiImportStage = "persist"
	builtinEmojiImportStageReconcile builtinEmojiImportStage = "reconcile"
	builtinEmojiImportStageWorker    builtinEmojiImportStage = "worker"
)

type builtinEmojiImportCode string

const (
	builtinEmojiImportCodeFetchFailed       builtinEmojiImportCode = "fetch_failed"
	builtinEmojiImportCodeUploadFailed      builtinEmojiImportCode = "upload_failed"
	builtinEmojiImportCodeInvalidMXC        builtinEmojiImportCode = "invalid_mxc"
	builtinEmojiImportCodeOperationTimeout  builtinEmojiImportCode = "operation_timeout"
	builtinEmojiImportCodeOperationCanceled builtinEmojiImportCode = "operation_canceled"
	builtinEmojiImportCodeWorkerPanic       builtinEmojiImportCode = "worker_panic"
	builtinEmojiImportCodePersistFailed     builtinEmojiImportCode = "persist_failed"
	builtinEmojiImportCodeImportCanceled    builtinEmojiImportCode = "import_canceled"
	builtinEmojiImportCodeImportAborted     builtinEmojiImportCode = "import_aborted"
	builtinEmojiImportCodeRecheckRace       builtinEmojiImportCode = "recheck_race"
)

type builtinEmojiPersistenceStatus string

const (
	builtinEmojiPersistenceNotNeeded builtinEmojiPersistenceStatus = "not_needed"
	builtinEmojiPersistenceSuccess   builtinEmojiPersistenceStatus = "success"
	builtinEmojiPersistencePartial   builtinEmojiPersistenceStatus = "partial"
	builtinEmojiPersistenceFailed    builtinEmojiPersistenceStatus = "failed"
	builtinEmojiPersistenceCanceled  builtinEmojiPersistenceStatus = "canceled"
)

type builtinEmojiImportFailure struct {
	RegistryIndex int
	Shortcode     string
	Stage         builtinEmojiImportStage
	Code          builtinEmojiImportCode
	Reason        builtinEmojiSafeReason
	HTTPStatus    int
}

type builtinEmojiUploadedUnreferenced struct {
	RegistryIndex int
	Shortcode     string
	MXC           id.ContentURIString
	Reason        builtinEmojiImportCode
}

type builtinEmojiImportReconciliationStatus struct {
	Status          builtinPackStatus
	AttemptedRooms  int
	ReconciledRooms int
	FailedRooms     int
}

type builtinEmojiImportAccountDataStatus struct {
	Status builtinPackStatus
	User   id.UserID
	Room   id.RoomID
	Writes int
}

type builtinEmojiImportBatch struct {
	Number          int
	RegistryIndexes []int
	Uploaded        int
	Persisted       int
	Skipped         int
	Persistence     builtinEmojiPersistenceStatus
	Reconciliation  builtinEmojiImportReconciliationStatus
	AccountData     builtinEmojiImportAccountDataStatus
}

type builtinEmojiImportResult struct {
	StartedAt            time.Time
	CompletedAt          time.Time
	CommandStatus        builtinEmojiCommandStatus
	Account              string
	Space                id.RoomID
	TotalConsidered      int
	Uploaded             int
	Imported             int
	Persisted            int
	Skipped              int
	Failures             []builtinEmojiImportFailure
	UploadedUnreferenced []builtinEmojiUploadedUnreferenced
	BatchSizes           []int
	Batches              []builtinEmojiImportBatch
	Persistence          builtinEmojiPersistenceStatus
	FinalReconciliation  builtinEmojiImportReconciliationStatus
	AccountData          builtinEmojiImportAccountDataStatus
	ProgressNoticeFailed bool
}

type builtinEmojiFetcher interface {
	Fetch(context.Context, textemoji.Entry) ([]byte, error)
}

type builtinEmojiUploader interface {
	Upload(context.Context, []byte, string, string) (id.ContentURIString, error)
}

type builtinEmojiCanonicalStoreView interface {
	Get(string) (string, bool)
}

type builtinEmojiCanonicalStorePersister interface {
	PersistMerged(map[string]string) error
}

type builtinEmojiImportReconcileFunc func(context.Context) (reconciliationResult, accountDataVisibilityResult)

type builtinEmojiImportContextClock interface {
	Now() time.Time
	ChildTimeout(context.Context, time.Duration) (context.Context, context.CancelFunc)
}

type systemBuiltinEmojiImportContextClock struct{}

func (systemBuiltinEmojiImportContextClock) Now() time.Time {
	return time.Now()
}

func (systemBuiltinEmojiImportContextClock) ChildTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, timeout)
}

type builtinEmojiImportLocker interface {
	Lock(context.Context) error
	Unlock()
}

const builtinEmojiImportLockRetryDelay = 5 * time.Millisecond

type builtinEmojiImportMutexLocker struct {
	mutex      *sync.Mutex
	retryDelay time.Duration
}

func newBuiltinEmojiImportMutexLocker(mutex *sync.Mutex) *builtinEmojiImportMutexLocker {
	return &builtinEmojiImportMutexLocker{mutex: mutex, retryDelay: builtinEmojiImportLockRetryDelay}
}

func (locker *builtinEmojiImportMutexLocker) Lock(ctx context.Context) error {
	if locker == nil || locker.mutex == nil {
		return errors.New("pack mutex unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	delay := locker.retryDelay
	if delay <= 0 {
		delay = builtinEmojiImportLockRetryDelay
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if locker.mutex.TryLock() {
			return nil
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (locker *builtinEmojiImportMutexLocker) Unlock() {
	locker.mutex.Unlock()
}

type builtinEmojiImportProgressKind string

const (
	builtinEmojiImportProgressBatchCommitted      builtinEmojiImportProgressKind = "batch_committed"
	builtinEmojiImportProgressFinalReconciliation builtinEmojiImportProgressKind = "final_reconciliation"
)

type builtinEmojiImportProgress struct {
	Kind              builtinEmojiImportProgressKind
	BatchNumber       int
	Uploaded          int
	Persisted         int
	Skipped           int
	TotalImported     int
	TotalPersisted    int
	TotalSkipped      int
	ReconciledRooms   int
	FailedRooms       int
	AccountDataWrites int
}

type builtinEmojiImportHooks struct {
	ControllerStarted func()
	WorkerStarted     func(int)
	WorkerStopped     func(int)
	JobDispatched     func(int)
	JobStarted        func(int, int)
	BeforeFetch       func(int)
	BeforeUpload      func(int)
}

type builtinEmojiImporter struct {
	fetcher          builtinEmojiFetcher
	uploader         builtinEmojiUploader
	account          remoteapi.UserID
	logger           builtinEmojiLogSink
	storeView        builtinEmojiCanonicalStoreView
	storePersister   builtinEmojiCanonicalStorePersister
	packLock         builtinEmojiImportLocker
	reconcile        builtinEmojiImportReconcileFunc
	operationTimeout time.Duration
	clock            builtinEmojiImportContextClock
	notice           func(builtinEmojiImportProgress)
	hooks            builtinEmojiImportHooks
	observeBatch     func(builtinEmojiImportBatch)
}

type serverBuiltinEmojiUploader struct {
	server *Server
	upload func(context.Context, []byte, string, string) (*mautrix.RespMediaUpload, error)
}

func (uploader *serverBuiltinEmojiUploader) Upload(ctx context.Context, data []byte, mimeType, filename string) (id.ContentURIString, error) {
	if uploader == nil {
		return "", errors.New("matrix uploader unavailable")
	}
	upload := uploader.upload
	if upload == nil {
		if uploader.server == nil || uploader.server.as == nil || uploader.server.as.BotIntent() == nil {
			return "", errors.New("matrix uploader unavailable")
		}
		upload = uploader.server.as.BotIntent().UploadBytesWithName
	}
	response, err := upload(ctx, data, mimeType, filename)
	if err != nil {
		return "", err
	}
	if response == nil {
		return "", errors.New("matrix upload returned no response")
	}
	mxc, valid := validMXC(string(response.ContentURI.CUString()))
	if !valid {
		return "", errors.New("matrix upload returned invalid content URI")
	}
	return mxc, nil
}

func (s *Server) newBuiltinEmojiImporter() *builtinEmojiImporter {
	fetcher := &githubBuiltinEmojiFetcher{
		client:  newBuiltinEmojiHTTPClient(nil),
		timeout: builtinEmojiImportOperationTimeout,
	}
	return &builtinEmojiImporter{
		fetcher:          fetcher,
		uploader:         &serverBuiltinEmojiUploader{server: s},
		storeView:        s.builtinEmojiStore,
		storePersister:   s.builtinEmojiStore,
		packLock:         newBuiltinEmojiImportMutexLocker(&s.packMu),
		operationTimeout: builtinEmojiImportOperationTimeout,
		clock:            systemBuiltinEmojiImportContextClock{},
		reconcile: func(ctx context.Context) (reconciliationResult, accountDataVisibilityResult) {
			return s.reconcileBuiltinEmojiPacksLocked(ctx), s.reconcileBuiltinPackAccountDataLocked(ctx)
		},
	}
}

type builtinEmojiImportJob struct {
	position int
	entry    textemoji.Entry
}

type builtinEmojiImportOutcomeKind uint8

const (
	builtinEmojiImportOutcomeSkipped builtinEmojiImportOutcomeKind = iota
	builtinEmojiImportOutcomeFailed
	builtinEmojiImportOutcomeUploaded
)

type builtinEmojiImportOutcome struct {
	position int
	entry    textemoji.Entry
	kind     builtinEmojiImportOutcomeKind
	mxc      id.ContentURIString
	failure  builtinEmojiImportFailure
}

type builtinEmojiImportStopReason uint8

const (
	builtinEmojiImportRunning builtinEmojiImportStopReason = iota
	builtinEmojiImportStoppedByCancellation
	builtinEmojiImportStoppedByPersistence
)

func (importer *builtinEmojiImporter) Run(ctx context.Context) builtinEmojiImportResult {
	if ctx == nil {
		ctx = context.Background()
	}
	clock := importer.contextClock()
	entries := textemoji.Entries()
	result := builtinEmojiImportResult{
		StartedAt:           clock.Now(),
		TotalConsidered:     len(entries),
		Persistence:         builtinEmojiPersistenceNotNeeded,
		FinalReconciliation: builtinEmojiImportReconciliationStatus{Status: builtinPackStatusSkipped},
		AccountData:         builtinEmojiImportAccountDataStatus{Status: builtinPackStatusSkipped},
	}

	workContext, cancelWork := context.WithCancel(ctx)
	jobs := make(chan builtinEmojiImportJob)
	outcomes := make(chan builtinEmojiImportOutcome, builtinEmojiImportWorkerCount)
	var workers sync.WaitGroup
	workers.Add(builtinEmojiImportWorkerCount)
	for workerIndex := 0; workerIndex < builtinEmojiImportWorkerCount; workerIndex++ {
		go importer.runWorker(workContext, workerIndex, jobs, outcomes, &workers)
	}
	workersDone := make(chan struct{})
	go func() {
		workers.Wait()
		close(workersDone)
	}()
	var closeJobsOnce sync.Once
	closeJobs := func() { closeJobsOnce.Do(func() { close(jobs) }) }
	drainWorkers := func() {
		for {
			select {
			case <-outcomes:
			case <-workersDone:
				return
			}
		}
	}
	defer func() {
		cancelWork()
		closeJobs()
		drainWorkers()
	}()
	if importer.hooks.ControllerStarted != nil {
		importer.hooks.ControllerStarted()
	}

	nextDispatch := 0
	nextOutcome := 0
	inFlight := 0
	buffered := make(map[int]builtinEmojiImportOutcome, builtinEmojiImportWorkerCount)
	batch := make([]builtinEmojiImportOutcome, 0, builtinEmojiImportBatchSize)
	stopReason := builtinEmojiImportRunning
	parentDone := ctx.Done()

	stop := func(reason builtinEmojiImportStopReason) {
		if stopReason != builtinEmojiImportRunning {
			return
		}
		stopReason = reason
		cancelWork()
		parentDone = nil
	}

	for nextOutcome < len(entries) {
		if stopReason == builtinEmojiImportRunning && ctx.Err() != nil {
			stop(builtinEmojiImportStoppedByCancellation)
		}
		for {
			outcome, ok := buffered[nextOutcome]
			if !ok {
				break
			}
			if stopReason == builtinEmojiImportRunning && ctx.Err() != nil {
				stop(builtinEmojiImportStoppedByCancellation)
			}
			delete(buffered, nextOutcome)
			if stopReason == builtinEmojiImportRunning {
				if reason := importer.applyOutcome(ctx, outcome, &batch, &result); reason != builtinEmojiImportRunning {
					stop(reason)
				}
			} else {
				importer.applyStoppedOutcome(outcome, stopReason, &result)
			}
			nextOutcome++
		}

		if nextOutcome == len(entries) || stopReason != builtinEmojiImportRunning && inFlight == 0 {
			break
		}

		var dispatch chan<- builtinEmojiImportJob
		var job builtinEmojiImportJob
		if stopReason == builtinEmojiImportRunning && ctx.Err() == nil && nextDispatch < len(entries) {
			dispatch = jobs
			job = builtinEmojiImportJob{position: nextDispatch, entry: entries[nextDispatch]}
		}
		if dispatch == nil && inFlight == 0 {
			break
		}
		if stopReason == builtinEmojiImportRunning && ctx.Err() != nil {
			stop(builtinEmojiImportStoppedByCancellation)
			continue
		}

		select {
		case dispatch <- job:
			inFlight++
			if ctx.Err() != nil {
				stop(builtinEmojiImportStoppedByCancellation)
				continue
			}
			nextDispatch++
			callBuiltinEmojiWorkerHook(importer.hooks.JobDispatched, job.entry.Index)
		case outcome := <-outcomes:
			inFlight--
			buffered[outcome.position] = outcome
		case <-parentDone:
			stop(builtinEmojiImportStoppedByCancellation)
		}
	}

	closeJobs()
	drainWorkers()

	if stopReason == builtinEmojiImportRunning && ctx.Err() != nil {
		stop(builtinEmojiImportStoppedByCancellation)
	}
	if stopReason == builtinEmojiImportRunning && len(batch) > 0 {
		if ctx.Err() != nil {
			stop(builtinEmojiImportStoppedByCancellation)
		} else {
			commit := importer.commitBatch(ctx, batch, &result)
			batch = append(batch[:0], commit.remaining...)
			if commit.stopReason != builtinEmojiImportRunning {
				stop(commit.stopReason)
			}
		}
	}
	if stopReason != builtinEmojiImportRunning {
		importer.markUnpersistedBatch(batch, stopReason, &result)
		for position := nextOutcome; position < len(entries); position++ {
			result.Failures = append(result.Failures, builtinEmojiImportFailure{
				RegistryIndex: entries[position].Index,
				Shortcode:     entries[position].Shortcode,
				Stage:         builtinEmojiImportStageImport,
				Code:          stopReason.failureCode(),
			})
		}
		if stopReason == builtinEmojiImportStoppedByCancellation {
			if result.Persisted > 0 {
				result.Persistence = builtinEmojiPersistencePartial
			} else {
				result.Persistence = builtinEmojiPersistenceCanceled
			}
		}
	}

	if stopReason != builtinEmojiImportStoppedByCancellation {
		importer.finalReconcile(ctx, &result)
	}
	result.CompletedAt = clock.Now()
	sortBuiltinEmojiImportResult(&result)
	return result
}

func (importer *builtinEmojiImporter) runWorker(ctx context.Context, workerIndex int, jobs <-chan builtinEmojiImportJob, outcomes chan<- builtinEmojiImportOutcome, workers *sync.WaitGroup) {
	defer workers.Done()
	callBuiltinEmojiWorkerHook(importer.hooks.WorkerStarted, workerIndex)
	defer callBuiltinEmojiWorkerHook(importer.hooks.WorkerStopped, workerIndex)
	for job := range jobs {
		outcomes <- importer.runJob(ctx, workerIndex, job)
	}
}

func (importer *builtinEmojiImporter) runJob(ctx context.Context, workerIndex int, job builtinEmojiImportJob) (outcome builtinEmojiImportOutcome) {
	outcome = builtinEmojiImportOutcome{position: job.position, entry: job.entry}
	defer func() {
		if recover() != nil {
			outcome.kind = builtinEmojiImportOutcomeFailed
			outcome.mxc = ""
			outcome.failure = newBuiltinEmojiImportFailure(job.entry, builtinEmojiImportStageWorker, builtinEmojiImportCodeWorkerPanic)
			fields := builtinEmojiEntryLogFields(importer.account, job.entry, builtinEmojiImportStageWorker, builtinEmojiLogCodeWorkerPanic)
			fields.Reason = builtinEmojiSafeReason(builtinEmojiImportCodeWorkerPanic)
			importer.logBuiltinEmoji(fields)
		}
	}()

	if ctx.Err() != nil {
		outcome.kind = builtinEmojiImportOutcomeFailed
		outcome.failure = newBuiltinEmojiImportFailure(job.entry, builtinEmojiImportStageImport, builtinEmojiImportCodeOperationCanceled)
		return outcome
	}
	if importer.hooks.JobStarted != nil {
		importer.hooks.JobStarted(workerIndex, job.entry.Index)
	}
	if importer.storeView != nil {
		if current, ok := importer.storeView.Get(job.entry.Shortcode); ok {
			if _, valid := validMXC(current); valid {
				outcome.kind = builtinEmojiImportOutcomeSkipped
				return outcome
			}
		}
	}

	if importer.hooks.BeforeFetch != nil {
		importer.hooks.BeforeFetch(job.entry.Index)
	}
	operationContext, cancel := importer.contextClock().ChildTimeout(ctx, importer.operationTimeoutValue())
	var data []byte
	var err error
	if importer.fetcher == nil {
		err = errors.New("fetcher unavailable")
	} else {
		data, err = importer.fetcher.Fetch(operationContext, job.entry)
	}
	operationErr := operationContext.Err()
	cancel()
	if err != nil {
		outcome.kind = builtinEmojiImportOutcomeFailed
		failureCode := classifyBuiltinEmojiImportOperationError(operationErr, ctx.Err(), builtinEmojiImportCodeFetchFailed)
		outcome.failure = newBuiltinEmojiImportFailure(job.entry, builtinEmojiImportStageFetch, failureCode)
		fetchReason, status := safeBuiltinEmojiFetchDetails(err)
		safeReason := builtinEmojiSafeReason(fetchReason)
		if safeReason == "" {
			safeReason = safeBuiltinEmojiImportReason(failureCode)
		}
		outcome.failure.Reason = safeReason
		outcome.failure.HTTPStatus = status
		fields := builtinEmojiEntryLogFields(importer.account, job.entry, builtinEmojiImportStageFetch, builtinEmojiLogCodeFetchFailed)
		fields.Reason = safeReason
		fields.HTTPStatus = status
		importer.logBuiltinEmoji(fields)
		return outcome
	}
	if ctx.Err() != nil {
		outcome.kind = builtinEmojiImportOutcomeFailed
		outcome.failure = newBuiltinEmojiImportFailure(job.entry, builtinEmojiImportStageUpload, builtinEmojiImportCodeOperationCanceled)
		return outcome
	}

	if importer.hooks.BeforeUpload != nil {
		importer.hooks.BeforeUpload(job.entry.Index)
	}
	operationContext, cancel = importer.contextClock().ChildTimeout(ctx, importer.operationTimeoutValue())
	var mxc id.ContentURIString
	if importer.uploader == nil {
		err = errors.New("uploader unavailable")
	} else {
		mxc, err = importer.uploader.Upload(operationContext, data, builtinEmojiImportMIMEType, job.entry.Filename)
	}
	operationErr = operationContext.Err()
	cancel()
	if err != nil {
		outcome.kind = builtinEmojiImportOutcomeFailed
		failureCode := classifyBuiltinEmojiImportOperationError(operationErr, ctx.Err(), builtinEmojiImportCodeUploadFailed)
		outcome.failure = newBuiltinEmojiImportFailure(job.entry, builtinEmojiImportStageUpload, failureCode)
		fields := builtinEmojiEntryLogFields(importer.account, job.entry, builtinEmojiImportStageUpload, builtinEmojiLogCodeUploadFailed)
		fields.Reason = safeBuiltinEmojiImportReason(failureCode)
		importer.logBuiltinEmoji(fields)
		return outcome
	}
	if _, valid := validMXC(string(mxc)); !valid {
		outcome.kind = builtinEmojiImportOutcomeFailed
		outcome.failure = newBuiltinEmojiImportFailure(job.entry, builtinEmojiImportStageUpload, builtinEmojiImportCodeInvalidMXC)
		fields := builtinEmojiEntryLogFields(importer.account, job.entry, builtinEmojiImportStageUpload, builtinEmojiLogCodeUploadFailed)
		fields.Reason = builtinEmojiSafeReason(builtinEmojiImportCodeInvalidMXC)
		importer.logBuiltinEmoji(fields)
		return outcome
	}
	outcome.kind = builtinEmojiImportOutcomeUploaded
	outcome.mxc = mxc
	return outcome
}

func (importer *builtinEmojiImporter) operationTimeoutValue() time.Duration {
	if importer.operationTimeout > 0 {
		return importer.operationTimeout
	}
	return builtinEmojiImportOperationTimeout
}

func (importer *builtinEmojiImporter) contextClock() builtinEmojiImportContextClock {
	if importer != nil && importer.clock != nil {
		return importer.clock
	}
	return systemBuiltinEmojiImportContextClock{}
}

type builtinEmojiImportCommitResult struct {
	stopReason builtinEmojiImportStopReason
	remaining  []builtinEmojiImportOutcome
}

func (importer *builtinEmojiImporter) applyOutcome(ctx context.Context, outcome builtinEmojiImportOutcome, batch *[]builtinEmojiImportOutcome, result *builtinEmojiImportResult) builtinEmojiImportStopReason {
	switch outcome.kind {
	case builtinEmojiImportOutcomeSkipped:
		result.Skipped++
	case builtinEmojiImportOutcomeFailed:
		result.Failures = append(result.Failures, outcome.failure)
	case builtinEmojiImportOutcomeUploaded:
		result.Uploaded++
		*batch = append(*batch, outcome)
		if len(*batch) == builtinEmojiImportBatchSize {
			if ctx.Err() != nil {
				return builtinEmojiImportStoppedByCancellation
			}
			commit := importer.commitBatch(ctx, *batch, result)
			*batch = append((*batch)[:0], commit.remaining...)
			return commit.stopReason
		}
	}
	return builtinEmojiImportRunning
}

func (importer *builtinEmojiImporter) applyStoppedOutcome(outcome builtinEmojiImportOutcome, stopReason builtinEmojiImportStopReason, result *builtinEmojiImportResult) {
	switch outcome.kind {
	case builtinEmojiImportOutcomeSkipped:
		result.Skipped++
	case builtinEmojiImportOutcomeFailed:
		result.Failures = append(result.Failures, outcome.failure)
	case builtinEmojiImportOutcomeUploaded:
		result.Uploaded++
		result.UploadedUnreferenced = append(result.UploadedUnreferenced, newBuiltinEmojiUnreferenced(outcome, stopReason.failureCode()))
		result.Failures = append(result.Failures, newBuiltinEmojiImportFailure(outcome.entry, builtinEmojiImportStageImport, stopReason.failureCode()))
	}
}

func (importer *builtinEmojiImporter) markUnpersistedBatch(batch []builtinEmojiImportOutcome, stopReason builtinEmojiImportStopReason, result *builtinEmojiImportResult) {
	for _, outcome := range batch {
		result.UploadedUnreferenced = append(result.UploadedUnreferenced, newBuiltinEmojiUnreferenced(outcome, stopReason.failureCode()))
		result.Failures = append(result.Failures, newBuiltinEmojiImportFailure(outcome.entry, builtinEmojiImportStageImport, stopReason.failureCode()))
	}
}

func (importer *builtinEmojiImporter) commitBatch(ctx context.Context, outcomes []builtinEmojiImportOutcome, result *builtinEmojiImportResult) builtinEmojiImportCommitResult {
	if ctx.Err() != nil {
		return builtinEmojiImportCommitResult{stopReason: builtinEmojiImportStoppedByCancellation, remaining: outcomes}
	}
	batch := builtinEmojiImportBatch{
		Number:          len(result.Batches) + 1,
		RegistryIndexes: make([]int, 0, len(outcomes)),
		Uploaded:        len(outcomes),
		Persistence:     builtinEmojiPersistenceNotNeeded,
		Reconciliation:  builtinEmojiImportReconciliationStatus{Status: builtinPackStatusSkipped},
		AccountData:     builtinEmojiImportAccountDataStatus{Status: builtinPackStatusSkipped},
	}
	for _, outcome := range outcomes {
		batch.RegistryIndexes = append(batch.RegistryIndexes, outcome.entry.Index)
	}

	locker := importer.packLock
	if locker == nil {
		locker = noopBuiltinEmojiImportLocker{}
	}
	if err := locker.Lock(ctx); err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return builtinEmojiImportCommitResult{stopReason: builtinEmojiImportStoppedByCancellation, remaining: outcomes}
		}
		batch.Persistence = builtinEmojiPersistenceFailed
		result.Persistence = builtinEmojiPersistenceFailed
		result.BatchSizes = append(result.BatchSizes, len(outcomes))
		importer.recordPersistFailures(outcomes, result)
		importer.recordBatch(batch, result)
		return builtinEmojiImportCommitResult{stopReason: builtinEmojiImportStoppedByPersistence}
	}
	locked := true
	defer func() {
		if locked {
			locker.Unlock()
		}
	}()
	unlock := func() {
		locker.Unlock()
		locked = false
	}
	if ctx.Err() != nil {
		unlock()
		return builtinEmojiImportCommitResult{stopReason: builtinEmojiImportStoppedByCancellation, remaining: outcomes}
	}

	replacements := make(map[string]string, len(outcomes))
	persistedOutcomes := make([]builtinEmojiImportOutcome, 0, len(outcomes))
	recheckedOutcomes := make([]builtinEmojiImportOutcome, 0)
	for index, outcome := range outcomes {
		if ctx.Err() != nil {
			unlock()
			importer.recordRecheckedOutcomes(recheckedOutcomes, result)
			remaining := append(persistedOutcomes, outcomes[index:]...)
			return builtinEmojiImportCommitResult{stopReason: builtinEmojiImportStoppedByCancellation, remaining: remaining}
		}
		if importer.storeView != nil {
			if current, ok := importer.storeView.Get(outcome.entry.Shortcode); ok {
				if _, valid := validMXC(current); valid {
					batch.Skipped++
					recheckedOutcomes = append(recheckedOutcomes, outcome)
					continue
				}
			}
		}
		replacements[outcome.entry.Shortcode] = string(outcome.mxc)
		persistedOutcomes = append(persistedOutcomes, outcome)
	}

	if ctx.Err() != nil {
		unlock()
		importer.recordRecheckedOutcomes(recheckedOutcomes, result)
		return builtinEmojiImportCommitResult{stopReason: builtinEmojiImportStoppedByCancellation, remaining: persistedOutcomes}
	}
	result.BatchSizes = append(result.BatchSizes, len(outcomes))
	if len(replacements) > 0 {
		var err error
		if importer.storePersister == nil {
			err = errors.New("store persister unavailable")
		} else {
			if ctx.Err() != nil {
				unlock()
				importer.recordRecheckedOutcomes(recheckedOutcomes, result)
				result.BatchSizes = result.BatchSizes[:len(result.BatchSizes)-1]
				return builtinEmojiImportCommitResult{stopReason: builtinEmojiImportStoppedByCancellation, remaining: persistedOutcomes}
			}
			err = importer.storePersister.PersistMerged(replacements)
		}
		if err != nil {
			unlock()
			batch.Persistence = builtinEmojiPersistenceFailed
			result.Persistence = builtinEmojiPersistenceFailed
			importer.recordRecheckedOutcomes(recheckedOutcomes, result)
			importer.recordPersistFailures(persistedOutcomes, result)
			importer.recordBatch(batch, result)
			return builtinEmojiImportCommitResult{stopReason: builtinEmojiImportStoppedByPersistence}
		}
		batch.Persisted = len(persistedOutcomes)
		batch.Persistence = builtinEmojiPersistenceSuccess
	}

	canceledAfterPersistence := ctx.Err() != nil
	if !canceledAfterPersistence {
		reconciliation, accountData := importer.reconcileLocked(ctx)
		batch.Reconciliation = safeBuiltinEmojiReconciliationStatus(reconciliation)
		batch.AccountData = safeBuiltinEmojiAccountDataStatus(accountData)
		importer.logBuiltinEmojiReconciliation(batch.Reconciliation, batch.AccountData, result)
		canceledAfterPersistence = ctx.Err() != nil
	}
	unlock()

	importer.recordRecheckedOutcomes(recheckedOutcomes, result)
	if batch.Persisted > 0 {
		result.Imported += batch.Persisted
		result.Persisted += batch.Persisted
		result.Persistence = builtinEmojiPersistenceSuccess
	}
	importer.recordBatch(batch, result)
	importer.notifyBatchCommitted(batch, result)
	if canceledAfterPersistence {
		return builtinEmojiImportCommitResult{stopReason: builtinEmojiImportStoppedByCancellation}
	}
	return builtinEmojiImportCommitResult{stopReason: builtinEmojiImportRunning}
}

func (importer *builtinEmojiImporter) recordRecheckedOutcomes(outcomes []builtinEmojiImportOutcome, result *builtinEmojiImportResult) {
	for _, outcome := range outcomes {
		result.Skipped++
		result.UploadedUnreferenced = append(result.UploadedUnreferenced, newBuiltinEmojiUnreferenced(outcome, builtinEmojiImportCodeRecheckRace))
	}
}

func (importer *builtinEmojiImporter) recordPersistFailures(outcomes []builtinEmojiImportOutcome, result *builtinEmojiImportResult) {
	for _, outcome := range outcomes {
		result.UploadedUnreferenced = append(result.UploadedUnreferenced, newBuiltinEmojiUnreferenced(outcome, builtinEmojiImportCodePersistFailed))
		failure := newBuiltinEmojiImportFailure(outcome.entry, builtinEmojiImportStagePersist, builtinEmojiImportCodePersistFailed)
		result.Failures = append(result.Failures, failure)
		fields := builtinEmojiEntryLogFields(importer.account, outcome.entry, builtinEmojiImportStagePersist, builtinEmojiLogCodePersistFailed)
		fields.Reason = builtinEmojiSafeReason(builtinEmojiImportCodePersistFailed)
		importer.logBuiltinEmoji(fields)
	}
}

func (importer *builtinEmojiImporter) recordBatch(batch builtinEmojiImportBatch, result *builtinEmojiImportResult) {
	batch.RegistryIndexes = append([]int(nil), batch.RegistryIndexes...)
	result.Batches = append(result.Batches, batch)
	if importer.observeBatch != nil {
		importer.observeBatch(batch)
	}
}

func (importer *builtinEmojiImporter) notifyBatchCommitted(batch builtinEmojiImportBatch, result *builtinEmojiImportResult) {
	importer.notify(builtinEmojiImportProgress{
		Kind:              builtinEmojiImportProgressBatchCommitted,
		BatchNumber:       batch.Number,
		Uploaded:          batch.Uploaded,
		Persisted:         batch.Persisted,
		Skipped:           batch.Skipped,
		TotalImported:     result.Imported,
		TotalPersisted:    result.Persisted,
		TotalSkipped:      result.Skipped,
		ReconciledRooms:   batch.Reconciliation.ReconciledRooms,
		FailedRooms:       batch.Reconciliation.FailedRooms,
		AccountDataWrites: batch.AccountData.Writes,
	}, result)
}

func (importer *builtinEmojiImporter) notify(progress builtinEmojiImportProgress, result *builtinEmojiImportResult) {
	if importer.notice == nil || result.ProgressNoticeFailed {
		return
	}
	if !callBuiltinEmojiImportProgressNotice(importer.notice, progress) {
		result.ProgressNoticeFailed = true
	}
}

func callBuiltinEmojiImportProgressNotice(notice func(builtinEmojiImportProgress), progress builtinEmojiImportProgress) (ok bool) {
	ok = true
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	notice(progress)
	return ok
}

func (importer *builtinEmojiImporter) finalReconcile(ctx context.Context, result *builtinEmojiImportResult) {
	if ctx.Err() != nil {
		return
	}
	locker := importer.packLock
	if locker == nil {
		locker = noopBuiltinEmojiImportLocker{}
	}
	if err := locker.Lock(ctx); err != nil {
		if ctx.Err() == nil {
			importer.logBuiltinEmoji(builtinEmojiLogFields{
				Stage:                builtinEmojiImportStageReconcile,
				Code:                 builtinEmojiLogCodeReconcileFailed,
				Reason:               "reconcile_lock_failed",
				Total:                result.TotalConsidered,
				Uploaded:             result.Uploaded,
				Imported:             result.Imported,
				Persisted:            result.Persisted,
				Skipped:              result.Skipped,
				Failed:               len(result.Failures),
				UploadedUnreferenced: len(result.UploadedUnreferenced),
				PersistenceStatus:    result.Persistence,
				ReconciliationStatus: result.FinalReconciliation.Status,
				AccountDataStatus:    result.AccountData.Status,
			})
		}
		return
	}
	locked := true
	defer func() {
		if locked {
			locker.Unlock()
		}
	}()
	unlock := func() {
		locker.Unlock()
		locked = false
	}
	if ctx.Err() != nil {
		unlock()
		return
	}
	reconciliation, accountData := importer.reconcileLocked(ctx)
	unlock()
	result.FinalReconciliation = safeBuiltinEmojiReconciliationStatus(reconciliation)
	result.AccountData = safeBuiltinEmojiAccountDataStatus(accountData)
	importer.logBuiltinEmojiReconciliation(result.FinalReconciliation, result.AccountData, result)
	importer.notify(builtinEmojiImportProgress{
		Kind:              builtinEmojiImportProgressFinalReconciliation,
		TotalImported:     result.Imported,
		TotalPersisted:    result.Persisted,
		TotalSkipped:      result.Skipped,
		ReconciledRooms:   result.FinalReconciliation.ReconciledRooms,
		FailedRooms:       result.FinalReconciliation.FailedRooms,
		AccountDataWrites: result.AccountData.Writes,
	}, result)
}

func (importer *builtinEmojiImporter) reconcileLocked(ctx context.Context) (reconciliationResult, accountDataVisibilityResult) {
	if importer.reconcile == nil {
		return reconciliationResult{Status: builtinPackStatusSkipped}, accountDataVisibilityResult{Status: builtinPackStatusSkipped}
	}
	return importer.reconcile(ctx)
}

func classifyBuiltinEmojiImportOperationError(operationErr, parentErr error, fallback builtinEmojiImportCode) builtinEmojiImportCode {
	if errors.Is(operationErr, context.DeadlineExceeded) {
		return builtinEmojiImportCodeOperationTimeout
	}
	if parentErr != nil || errors.Is(operationErr, context.Canceled) {
		return builtinEmojiImportCodeOperationCanceled
	}
	return fallback
}

func newBuiltinEmojiImportFailure(entry textemoji.Entry, stage builtinEmojiImportStage, code builtinEmojiImportCode) builtinEmojiImportFailure {
	return builtinEmojiImportFailure{RegistryIndex: entry.Index, Shortcode: entry.Shortcode, Stage: stage, Code: code}
}

func newBuiltinEmojiUnreferenced(outcome builtinEmojiImportOutcome, reason builtinEmojiImportCode) builtinEmojiUploadedUnreferenced {
	return builtinEmojiUploadedUnreferenced{RegistryIndex: outcome.entry.Index, Shortcode: outcome.entry.Shortcode, MXC: outcome.mxc, Reason: reason}
}

func (reason builtinEmojiImportStopReason) failureCode() builtinEmojiImportCode {
	if reason == builtinEmojiImportStoppedByCancellation {
		return builtinEmojiImportCodeImportCanceled
	}
	return builtinEmojiImportCodeImportAborted
}

func safeBuiltinEmojiReconciliationStatus(result reconciliationResult) builtinEmojiImportReconciliationStatus {
	return builtinEmojiImportReconciliationStatus{
		Status:          result.Status,
		AttemptedRooms:  result.AttemptedRooms,
		ReconciledRooms: result.ReconciledRooms,
		FailedRooms:     result.FailedRooms,
	}
}

func safeBuiltinEmojiAccountDataStatus(result accountDataVisibilityResult) builtinEmojiImportAccountDataStatus {
	return builtinEmojiImportAccountDataStatus{Status: result.Status, User: result.User, Room: result.Room, Writes: result.Writes}
}

func sortBuiltinEmojiImportResult(result *builtinEmojiImportResult) {
	sort.SliceStable(result.Failures, func(first, second int) bool {
		if result.Failures[first].RegistryIndex != result.Failures[second].RegistryIndex {
			return result.Failures[first].RegistryIndex < result.Failures[second].RegistryIndex
		}
		if result.Failures[first].Stage != result.Failures[second].Stage {
			return result.Failures[first].Stage < result.Failures[second].Stage
		}
		return result.Failures[first].Code < result.Failures[second].Code
	})
	sort.SliceStable(result.UploadedUnreferenced, func(first, second int) bool {
		return result.UploadedUnreferenced[first].RegistryIndex < result.UploadedUnreferenced[second].RegistryIndex
	})
}

func callBuiltinEmojiWorkerHook(hook func(int), workerIndex int) {
	if hook == nil {
		return
	}
	defer func() { _ = recover() }()
	hook(workerIndex)
}

type noopBuiltinEmojiImportLocker struct{}

func (noopBuiltinEmojiImportLocker) Lock(ctx context.Context) error {
	return ctx.Err()
}

func (noopBuiltinEmojiImportLocker) Unlock() {}
