package matrixas

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"maunium.net/go/mautrix/id"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

const (
	builtinEmojiCommandTimeout       = 10 * time.Minute
	builtinEmojiCommandNoticeTimeout = 5 * time.Second
	builtinEmojiCommandNoticeLimit   = 4096
)

type builtinEmojiImportRunner interface {
	Run(context.Context) builtinEmojiImportResult
	setProgressNotice(func(builtinEmojiImportProgress))
}

type builtinEmojiImportLoggingRunner interface {
	setBuiltinEmojiLogging(remoteapi.UserID, builtinEmojiLogSink)
}

type builtinEmojiRuntime struct {
	ctx    context.Context
	cancel context.CancelFunc
}

type builtinEmojiImportTarget struct {
	account remoteapi.UserID
	space   id.RoomID
	room    id.RoomID
	sender  id.UserID
}

func (importer *builtinEmojiImporter) setProgressNotice(notice func(builtinEmojiImportProgress)) {
	importer.notice = notice
}

func parseBuiltinEmojiImportCommand(rest string) (recognized, acknowledged bool) {
	fields := strings.Fields(rest)
	if len(fields) < 2 {
		return false, false
	}
	recognized = fields[0] == "import" && fields[1] == "wechat" || fields[0] == "导入" && fields[1] == "微信"
	acknowledged = recognized && len(fields) == 3 && fields[2] == "--ack-no-license"
	return recognized, acknowledged
}

func (s *Server) allowedBuiltinEmojiImport(sender id.UserID) bool {
	if s == nil || s.cfg == nil || !s.allowed(sender) {
		return false
	}
	if personal := id.UserID(s.cfg.Bridge.PersonalStickerUserID); personal != "" && sender == personal {
		return true
	}
	permissions := s.cfg.Bridge.Permissions
	if level, exists := permissions[sender.String()]; exists {
		return level == "admin"
	}
	_, domain, err := sender.Parse()
	if err != nil {
		return false
	}
	return permissions[domain] == "admin"
}

func (s *Server) handleBuiltinEmojiImportCommand(eventCtx context.Context, room id.RoomID, sender id.UserID, rest string) {
	if !s.allowedBuiltinEmojiImport(sender) {
		s.safeBuiltinEmojiNotice(eventCtx, room, "WeChat built-in emoji import is disabled for this user. Configure bridge.personal_sticker_user_id for the exact user or an exact user/domain bridge.permissions level of admin; explicit denials still apply.")
		return
	}

	recognized, acknowledged := parseBuiltinEmojiImportCommand(rest)
	if !recognized {
		s.safeBuiltinEmojiNotice(eventCtx, room, fmt.Sprintf("unknown stickers import command; use `%s stickers import wechat --ack-no-license` exactly", s.commandPrefix()))
		return
	}
	if !acknowledged {
		s.safeBuiltinEmojiNotice(eventCtx, room, builtinEmojiImportRiskNotice(s.commandPrefix()))
		return
	}
	if !s.tryBeginBuiltinEmojiImport() {
		s.safeBuiltinEmojiNotice(eventCtx, room, "A WeChat built-in emoji import is already running globally.")
		return
	}
	launched := false
	defer func() {
		if !launched {
			s.finishBuiltinEmojiImport()
		}
	}()

	runtime := s.snapshotBuiltinEmojiRuntime()
	if runtime == nil || runtime.ctx == nil || runtime.ctx.Err() != nil {
		s.safeBuiltinEmojiNotice(eventCtx, room, "WeChat built-in emoji import cannot start because the bridge Run context is unavailable.")
		return
	}

	target, ok := s.captureBuiltinEmojiImportTarget(runtime.ctx, room, sender)
	if !ok {
		return
	}

	if s.builtinEmojiStore == nil {
		s.safeBuiltinEmojiNotice(runtime.ctx, room, "WeChat built-in emoji import cannot start because the canonical store is unavailable.")
		return
	}
	if s.builtinEmojiStore.LoadError() != nil {
		now := time.Now()
		if s.builtinEmojiNow != nil {
			now = s.builtinEmojiNow()
		}
		if err := s.builtinEmojiStore.ResetForAcknowledgedImport(now); err != nil {
			s.safeBuiltinEmojiNotice(runtime.ctx, room, "WeChat built-in emoji import aborted: the invalid canonical store could not be quarantined and reset.")
			return
		}
	}

	runner := s.buildBuiltinEmojiImportRunner()
	if runner == nil {
		s.safeBuiltinEmojiNotice(runtime.ctx, room, "WeChat built-in emoji import cannot start because the importer is unavailable.")
		return
	}
	if loggingRunner, ok := runner.(builtinEmojiImportLoggingRunner); ok {
		loggingRunner.setBuiltinEmojiLogging(target.account, s.logBuiltinEmoji)
	}

	importCtx, cancelImport := s.newBuiltinEmojiImportContext(runtime.ctx)
	if importCtx == nil || cancelImport == nil {
		s.safeBuiltinEmojiNotice(runtime.ctx, room, "WeChat built-in emoji import cannot start because its bounded context is unavailable.")
		return
	}
	runner.setProgressNotice(func(progress builtinEmojiImportProgress) {
		s.safeBuiltinEmojiNotice(importCtx, target.room, formatBuiltinEmojiImportProgress(progress))
	})

	s.safeBuiltinEmojiNotice(runtime.ctx, target.room, builtinEmojiImportStartNotice(target))
	s.logBuiltinEmoji(builtinEmojiLogFields{
		Account: target.account,
		Stage:   builtinEmojiImportStageImport,
		Code:    builtinEmojiLogCodeStarted,
		Total:   109,
	})
	launched = true
	go s.runBuiltinEmojiImport(importCtx, cancelImport, runner, target)
}

func (s *Server) captureBuiltinEmojiImportTarget(ctx context.Context, room id.RoomID, sender id.UserID) (builtinEmojiImportTarget, bool) {
	s.accountTransitionMu.Lock()
	defer s.accountTransitionMu.Unlock()

	api := s.api
	if api == nil {
		s.safeBuiltinEmojiNotice(ctx, room, "WeChat built-in emoji import requires a captured logged-in API session.")
		return builtinEmojiImportTarget{}, false
	}
	loggedIn, err := api.IsLoggedIn(ctx)
	if err != nil {
		s.safeBuiltinEmojiNotice(ctx, room, "WeChat built-in emoji import could not verify the captured API login.")
		return builtinEmojiImportTarget{}, false
	}
	if !loggedIn {
		s.safeBuiltinEmojiNotice(ctx, room, "WeChat built-in emoji import requires a currently logged-in API session.")
		return builtinEmojiImportTarget{}, false
	}
	account, exists := s.accounts.current()
	if !exists || account.ID == "" {
		s.safeBuiltinEmojiNotice(ctx, room, "WeChat built-in emoji import requires one current saved account.")
		return builtinEmojiImportTarget{}, false
	}
	space, err := s.resolveBuiltinEmojiImportSpace(ctx, account.ID)
	if err != nil || space == "" {
		s.safeBuiltinEmojiNotice(ctx, room, "WeChat built-in emoji import requires an available Space for the captured account.")
		return builtinEmojiImportTarget{}, false
	}
	return builtinEmojiImportTarget{account: account.ID, space: space, room: room, sender: sender}, true
}

func (s *Server) resolveBuiltinEmojiImportSpace(ctx context.Context, account remoteapi.UserID) (id.RoomID, error) {
	if s.builtinEmojiSpaceResolver != nil {
		return s.builtinEmojiSpaceResolver(ctx, account)
	}
	return s.ensureAccountSpace(ctx, account)
}

func (s *Server) buildBuiltinEmojiImportRunner() (runner builtinEmojiImportRunner) {
	defer func() {
		if recover() != nil {
			runner = nil
		}
	}()
	if s.builtinEmojiImporterFactory != nil {
		return s.builtinEmojiImporterFactory()
	}
	return s.newBuiltinEmojiImporter()
}

func (s *Server) newBuiltinEmojiImportContext(parent context.Context) (ctx context.Context, cancel context.CancelFunc) {
	defer func() {
		if recover() != nil {
			ctx = nil
			cancel = nil
		}
	}()
	if s.builtinEmojiContextFactory != nil {
		return s.builtinEmojiContextFactory(parent, builtinEmojiCommandTimeout)
	}
	return context.WithTimeout(parent, builtinEmojiCommandTimeout)
}

func (s *Server) runBuiltinEmojiImport(importCtx context.Context, cancelImport context.CancelFunc, runner builtinEmojiImportRunner, target builtinEmojiImportTarget) {
	var result builtinEmojiImportResult
	var importErr error
	completed := false
	defer func() {
		panicValue := recover()
		s.finishBuiltinEmojiImport()
		func() {
			defer func() {
				_ = recover()
			}()
			cancelImport()
		}()

		if panicValue != nil {
			s.logBuiltinEmoji(builtinEmojiLogFields{
				Account: target.account,
				Stage:   builtinEmojiImportStageImport,
				Code:    builtinEmojiLogCodePanic,
				Reason:  builtinEmojiSafeReason(builtinEmojiLogCodePanic),
			})
			s.safeBuiltinEmojiBestEffortNotice(target.room, "WeChat built-in emoji import failed safely after an internal panic; the global import slot was released and the command may be retried.")
			return
		}
		if completed {
			result.CommandStatus = builtinEmojiCommandStatusFinished
			if importErr != nil {
				result.CommandStatus = builtinEmojiCommandStatusCanceledOrTimedOut
			}
			result.Account = string(target.account)
			result.Space = target.space
			progressStatus := builtinPackStatusSuccess
			if result.ProgressNoticeFailed {
				progressStatus = builtinPackStatusFailed
			}
			s.logBuiltinEmoji(builtinEmojiLogFields{
				Account:              target.account,
				Stage:                builtinEmojiImportStageImport,
				Code:                 builtinEmojiLogCodeCompleted,
				Reason:               builtinEmojiSafeReason(result.CommandStatus),
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
				ProgressNoticeStatus: progressStatus,
			})
			s.safeBuiltinEmojiBestEffortNotice(target.room, formatBuiltinEmojiImportResult(result))
		}
	}()

	result = runner.Run(importCtx)
	importErr = importCtx.Err()
	completed = true
}

func (s *Server) tryBeginBuiltinEmojiImport() bool {
	s.importMu.Lock()
	defer s.importMu.Unlock()
	if s.importRunning {
		return false
	}
	s.importRunning = true
	return true
}

func (s *Server) finishBuiltinEmojiImport() {
	s.importMu.Lock()
	s.importRunning = false
	s.importMu.Unlock()
}

func (s *Server) captureBuiltinEmojiRuntime(parent context.Context) *builtinEmojiRuntime {
	ctx, cancel := context.WithCancel(parent)
	runtime := &builtinEmojiRuntime{ctx: ctx, cancel: cancel}

	s.importMu.Lock()
	previous := s.importRuntime
	s.importRuntime = runtime
	s.importMu.Unlock()
	if previous != nil && previous.cancel != nil {
		previous.cancel()
	}
	return runtime
}

func (s *Server) clearBuiltinEmojiRuntime(runtime *builtinEmojiRuntime) {
	if runtime == nil {
		return
	}
	if runtime.cancel != nil {
		runtime.cancel()
	}
	s.importMu.Lock()
	if s.importRuntime == runtime {
		s.importRuntime = nil
	}
	s.importMu.Unlock()
}

func (s *Server) snapshotBuiltinEmojiRuntime() *builtinEmojiRuntime {
	s.importMu.Lock()
	defer s.importMu.Unlock()
	return s.importRuntime
}

func (s *Server) newBuiltinEmojiNoticeContext(parent context.Context, bestEffort bool) (ctx context.Context, cancel context.CancelFunc) {
	defer func() {
		if recover() != nil {
			ctx = nil
			cancel = nil
		}
	}()
	if bestEffort || parent == nil {
		parent = context.Background()
	}
	if s.builtinEmojiNoticeContextFactory != nil {
		return s.builtinEmojiNoticeContextFactory(parent, builtinEmojiCommandNoticeTimeout)
	}
	return context.WithTimeout(parent, builtinEmojiCommandNoticeTimeout)
}

func (s *Server) safeBuiltinEmojiNotice(parent context.Context, room id.RoomID, body string) {
	s.safeBuiltinEmojiNoticeWithContext(parent, false, room, body)
}

func (s *Server) safeBuiltinEmojiBestEffortNotice(room id.RoomID, body string) {
	s.safeBuiltinEmojiNoticeWithContext(context.Background(), true, room, body)
}

func (s *Server) safeBuiltinEmojiNoticeWithContext(parent context.Context, bestEffort bool, room id.RoomID, body string) {
	defer func() {
		_ = recover()
	}()
	ctx, cancel := s.newBuiltinEmojiNoticeContext(parent, bestEffort)
	if ctx == nil || cancel == nil {
		return
	}
	defer cancel()

	body = boundBuiltinEmojiCommandNotice(body)
	if s.builtinEmojiNotice != nil {
		s.builtinEmojiNotice(ctx, room, body)
		return
	}
	s.notice(ctx, room, body)
}

func boundBuiltinEmojiCommandNotice(body string) string {
	if len(body) <= builtinEmojiCommandNoticeLimit {
		return body
	}
	end := builtinEmojiCommandNoticeLimit
	for end > 0 && !utf8.ValidString(body[:end]) {
		end--
	}
	return body[:end]
}

func builtinEmojiImportRiskNotice(prefix string) string {
	return fmt.Sprintf("WeChat built-in emoji import not started. Source repository: %s\nPinned commit: %s\nSource path: %s\nThe upstream repository has no explicit license. Downloading and uploading these files involves copying and may redistribute them; acknowledgment grants no rights. If you accept this risk, use `%s stickers import wechat --ack-no-license` exactly.", builtinEmojiSourceRepository, builtinEmojiSourceCommit, builtinEmojiSourcePath, prefix)
}

func builtinEmojiImportStartNotice(target builtinEmojiImportTarget) string {
	return fmt.Sprintf("Starting WeChat built-in emoji import.\nSource repository: %s\nPinned commit: %s\nSource path: %s\nWarning: no explicit license was found; copying and possible redistribution are at the operator's risk and acknowledgment grants no rights.\nTotal: 109\nCaptured account: %s\nCaptured Space: %s", builtinEmojiSourceRepository, builtinEmojiSourceCommit, builtinEmojiSourcePath, target.account, target.space)
}

func formatBuiltinEmojiImportProgress(progress builtinEmojiImportProgress) string {
	switch progress.Kind {
	case builtinEmojiImportProgressBatchCommitted:
		return fmt.Sprintf("WeChat built-in emoji import progress: batch %d committed; uploaded=%d persisted=%d skipped=%d totals(imported=%d persisted=%d skipped=%d)", progress.BatchNumber, progress.Uploaded, progress.Persisted, progress.Skipped, progress.TotalImported, progress.TotalPersisted, progress.TotalSkipped)
	case builtinEmojiImportProgressFinalReconciliation:
		return fmt.Sprintf("WeChat built-in emoji import progress: final reconciliation; rooms=%d failed_rooms=%d account_data_writes=%d", progress.ReconciledRooms, progress.FailedRooms, progress.AccountDataWrites)
	default:
		return "WeChat built-in emoji import progress updated."
	}
}
