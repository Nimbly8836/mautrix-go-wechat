package matrixas

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"github.com/leat/mautrix-go-wechat/internal/wechat/textemoji"
)

const (
	builtinEmojiResultFailureLimit = 20
	builtinEmojiFailureDetailLimit = 256
	// Leave enough room in the 4096-byte Matrix notice for all provenance,
	// status, retry, and rights-risk text even when 20 details are present.
	builtinEmojiFailureRenderLimit = 128
)

type builtinEmojiCommandStatus string

const (
	builtinEmojiCommandStatusFinished           builtinEmojiCommandStatus = "finished"
	builtinEmojiCommandStatusCanceledOrTimedOut builtinEmojiCommandStatus = "canceled or timed out"
)

type builtinEmojiSafeReason string

type builtinEmojiLogCode string

const (
	builtinEmojiLogCodeStarted         builtinEmojiLogCode = "import_started"
	builtinEmojiLogCodeCompleted       builtinEmojiLogCode = "import_completed"
	builtinEmojiLogCodePanic           builtinEmojiLogCode = "import_panic"
	builtinEmojiLogCodeFetchFailed     builtinEmojiLogCode = "fetch_failed"
	builtinEmojiLogCodeUploadFailed    builtinEmojiLogCode = "upload_failed"
	builtinEmojiLogCodePersistFailed   builtinEmojiLogCode = "persist_failed"
	builtinEmojiLogCodeReconcileFailed builtinEmojiLogCode = "reconcile_failed"
	builtinEmojiLogCodeWorkerPanic     builtinEmojiLogCode = "worker_panic"
)

// builtinEmojiLogFields is the complete allowlist for built-in emoji logs.
// It intentionally has no arbitrary error, URL, response, media, token, MXC,
// panic-value, or byte fields.
type builtinEmojiLogFields struct {
	Account              remoteapi.UserID              `json:"account,omitempty"`
	Commit               string                        `json:"commit,omitempty"`
	RegistryIndex        int                           `json:"registry_index,omitempty"`
	Chinese              string                        `json:"chinese,omitempty"`
	Name                 string                        `json:"name,omitempty"`
	Shortcode            string                        `json:"shortcode,omitempty"`
	Stage                builtinEmojiImportStage       `json:"stage,omitempty"`
	Code                 builtinEmojiLogCode           `json:"code,omitempty"`
	Reason               builtinEmojiSafeReason        `json:"reason,omitempty"`
	HTTPStatus           int                           `json:"http_status,omitempty"`
	Total                int                           `json:"total,omitempty"`
	Uploaded             int                           `json:"uploaded,omitempty"`
	Imported             int                           `json:"imported,omitempty"`
	Persisted            int                           `json:"persisted,omitempty"`
	Skipped              int                           `json:"skipped,omitempty"`
	Failed               int                           `json:"failed,omitempty"`
	UploadedUnreferenced int                           `json:"uploaded_unreferenced,omitempty"`
	PersistenceStatus    builtinEmojiPersistenceStatus `json:"persistence_status,omitempty"`
	ReconciliationStatus builtinPackStatus             `json:"reconciliation_status,omitempty"`
	AccountDataStatus    builtinPackStatus             `json:"account_data_status,omitempty"`
	ProgressNoticeStatus builtinPackStatus             `json:"progress_notice_status,omitempty"`
}

type builtinEmojiLogSink func(builtinEmojiLogFields)

func callBuiltinEmojiLog(sink builtinEmojiLogSink, fields builtinEmojiLogFields) {
	if sink == nil {
		return
	}
	defer func() { _ = recover() }()
	sink(fields)
}

func (importer *builtinEmojiImporter) setBuiltinEmojiLogging(account remoteapi.UserID, logger builtinEmojiLogSink) {
	if importer == nil {
		return
	}
	importer.account = account
	importer.logger = logger
}

func (importer *builtinEmojiImporter) logBuiltinEmoji(fields builtinEmojiLogFields) {
	if importer == nil {
		return
	}
	fields.Account = importer.account
	fields.Commit = builtinEmojiSourceCommit
	callBuiltinEmojiLog(importer.logger, fields)
}

func (importer *builtinEmojiImporter) logBuiltinEmojiReconciliation(reconciliation builtinEmojiImportReconciliationStatus, accountData builtinEmojiImportAccountDataStatus, result *builtinEmojiImportResult) {
	if reconciliation.Status != builtinPackStatusPartial && reconciliation.Status != builtinPackStatusFailed && accountData.Status != builtinPackStatusPartial && accountData.Status != builtinPackStatusFailed {
		return
	}
	fields := builtinEmojiLogFields{
		Stage:                builtinEmojiImportStageReconcile,
		Code:                 builtinEmojiLogCodeReconcileFailed,
		Reason:               builtinEmojiSafeReason(builtinEmojiLogCodeReconcileFailed),
		ReconciliationStatus: reconciliation.Status,
		AccountDataStatus:    accountData.Status,
	}
	if result != nil {
		fields.Total = result.TotalConsidered
		fields.Uploaded = result.Uploaded
		fields.Imported = result.Imported
		fields.Persisted = result.Persisted
		fields.Skipped = result.Skipped
		fields.Failed = len(result.Failures)
		fields.UploadedUnreferenced = len(result.UploadedUnreferenced)
		fields.PersistenceStatus = result.Persistence
	}
	importer.logBuiltinEmoji(fields)
}

func safeBuiltinEmojiImportReason(code builtinEmojiImportCode) builtinEmojiSafeReason {
	switch code {
	case builtinEmojiImportCodeFetchFailed,
		builtinEmojiImportCodeUploadFailed,
		builtinEmojiImportCodeInvalidMXC,
		builtinEmojiImportCodeOperationTimeout,
		builtinEmojiImportCodeOperationCanceled,
		builtinEmojiImportCodeWorkerPanic,
		builtinEmojiImportCodePersistFailed,
		builtinEmojiImportCodeImportCanceled,
		builtinEmojiImportCodeImportAborted,
		builtinEmojiImportCodeRecheckRace:
		return builtinEmojiSafeReason(code)
	default:
		return ""
	}
}

func truncateUTF8Bytes(value string, limit int) string {
	if limit <= 0 || value == "" {
		return ""
	}
	var output strings.Builder
	output.Grow(min(len(value), limit))
	for len(value) > 0 {
		r, width := utf8.DecodeRuneInString(value)
		encodedWidth := utf8.RuneLen(r)
		if encodedWidth < 0 || output.Len()+encodedWidth > limit {
			break
		}
		output.WriteRune(r)
		value = value[width:]
	}
	return output.String()
}

func formatBuiltinEmojiImportResult(result builtinEmojiImportResult) string {
	failures := append([]builtinEmojiImportFailure(nil), result.Failures...)
	sort.SliceStable(failures, func(first, second int) bool {
		if failures[first].RegistryIndex != failures[second].RegistryIndex {
			return failures[first].RegistryIndex < failures[second].RegistryIndex
		}
		if failures[first].Stage != failures[second].Stage {
			return failures[first].Stage < failures[second].Stage
		}
		if failures[first].Code != failures[second].Code {
			return failures[first].Code < failures[second].Code
		}
		if failures[first].Reason != failures[second].Reason {
			return failures[first].Reason < failures[second].Reason
		}
		return failures[first].HTTPStatus < failures[second].HTTPStatus
	})

	commandStatus := result.CommandStatus
	if commandStatus == "" {
		commandStatus = builtinEmojiCommandStatusFinished
	}
	progressStatus := builtinPackStatusSuccess
	if result.ProgressNoticeFailed {
		progressStatus = builtinPackStatusFailed
	}

	var output strings.Builder
	fmt.Fprintf(&output, "WeChat built-in emoji import completed (%s).\n", commandStatus)
	fmt.Fprintf(&output, "Source repository: %s\nPinned commit: %s\nSource path: %s\n", builtinEmojiSourceRepository, builtinEmojiSourceCommit, builtinEmojiSourcePath)
	if result.Account != "" || result.Space != "" {
		fmt.Fprintf(&output, "Captured target: account=%s space=%s\n", result.Account, result.Space)
	}
	fmt.Fprintf(&output, "Counts: total=%d uploaded=%d imported=%d persisted=%d skipped=%d failed=%d uploaded_unreferenced=%d\n", result.TotalConsidered, result.Uploaded, result.Imported, result.Persisted, result.Skipped, len(failures), len(result.UploadedUnreferenced))
	fmt.Fprintf(&output, "Persistence: persistence=%s\n", result.Persistence)
	fmt.Fprintf(&output, "Final reconciliation: status=%s attempted_rooms=%d reconciled_rooms=%d failed_rooms=%d\n", result.FinalReconciliation.Status, result.FinalReconciliation.AttemptedRooms, result.FinalReconciliation.ReconciledRooms, result.FinalReconciliation.FailedRooms)
	fmt.Fprintf(&output, "Account data: status=%s user=%s room=%s writes=%d\n", result.AccountData.Status, result.AccountData.User, result.AccountData.Room, result.AccountData.Writes)
	fmt.Fprintf(&output, "Progress-notice status: notice_status=%s\n", progressStatus)

	if len(failures) > 0 {
		output.WriteString("Failures (sorted by registry index; first 20 only):\n")
		displayed := min(len(failures), builtinEmojiResultFailureLimit)
		for _, failure := range failures[:displayed] {
			detail := formatBuiltinEmojiImportFailure(failure)
			detail = truncateUTF8Bytes(detail, builtinEmojiFailureRenderLimit-2)
			fmt.Fprintf(&output, "- %s\n", detail)
		}
		if omitted := len(failures) - displayed; omitted > 0 {
			fmt.Fprintf(&output, "%d additional failures omitted.\n", omitted)
		}
	}

	output.WriteString("Rerun exact command: `stickers import wechat --ack-no-license`; existing canonical MXCs are skipped and missing entries plus reconciliation are retried.\n")
	fmt.Fprintf(&output, "Attribution: %s at commit %s, path %s. The upstream README credits Emojipedia, WeChat Official Accounts Platform, and Web WeChat.\n", builtinEmojiSourceRepository, builtinEmojiSourceCommit, builtinEmojiSourcePath)
	output.WriteString("No-license acknowledgment and risk: no explicit license or COPYING grant was identified upstream. Downloading, uploading, and room/Space distribution involve copying and possible redistribution. The `--ack-no-license` acknowledgment does not grant rights, imply permission, or remove operator risk.")
	return output.String()
}

func formatBuiltinEmojiImportFailure(failure builtinEmojiImportFailure) string {
	chinese, name, shortcode := "", "", "unknown"
	entries := textemoji.Entries()
	if failure.RegistryIndex > 0 && failure.RegistryIndex <= len(entries) {
		entry := entries[failure.RegistryIndex-1]
		if entry.Index == failure.RegistryIndex {
			chinese, name, shortcode = entry.Chinese, entry.English, entry.Shortcode
		}
	}
	detail := fmt.Sprintf("#%03d chinese=%s name=%s shortcode=%s stage=%s code=%s", failure.RegistryIndex, chinese, name, shortcode, failure.Stage, failure.Code)
	if failure.Reason != "" {
		detail += " reason=" + string(failure.Reason)
	}
	if failure.HTTPStatus != 0 {
		detail += fmt.Sprintf(" http_status=%d", failure.HTTPStatus)
	}
	return detail
}

func (s *Server) logBuiltinEmoji(fields builtinEmojiLogFields) {
	fields.Commit = builtinEmojiSourceCommit
	if s != nil && s.builtinEmojiLogger != nil {
		callBuiltinEmojiLog(s.builtinEmojiLogger, fields)
		return
	}
	if s == nil || s.as == nil {
		return
	}

	event := s.as.Log.Info()
	if fields.Account != "" {
		event.Str("account", string(fields.Account))
	}
	event.Str("commit", fields.Commit)
	if fields.RegistryIndex != 0 {
		event.Int("registry_index", fields.RegistryIndex)
	}
	if fields.Chinese != "" {
		event.Str("chinese", fields.Chinese)
	}
	if fields.Name != "" {
		event.Str("name", fields.Name)
	}
	if fields.Shortcode != "" {
		event.Str("shortcode", fields.Shortcode)
	}
	if fields.Stage != "" {
		event.Str("stage", string(fields.Stage))
	}
	if fields.Code != "" {
		event.Str("code", string(fields.Code))
	}
	if fields.Reason != "" {
		event.Str("reason", string(fields.Reason))
	}
	if fields.HTTPStatus != 0 {
		event.Int("http_status", fields.HTTPStatus)
	}
	aggregate := fields.Code == builtinEmojiLogCodeCompleted || fields.Code == builtinEmojiLogCodeReconcileFailed
	if fields.Total != 0 || aggregate {
		event.Int("total", fields.Total)
	}
	if fields.Uploaded != 0 || aggregate {
		event.Int("uploaded", fields.Uploaded)
	}
	if fields.Imported != 0 || aggregate {
		event.Int("imported", fields.Imported)
	}
	if fields.Persisted != 0 || aggregate {
		event.Int("persisted", fields.Persisted)
	}
	if fields.Skipped != 0 || aggregate {
		event.Int("skipped", fields.Skipped)
	}
	if fields.Failed != 0 || aggregate {
		event.Int("failed", fields.Failed)
	}
	if fields.UploadedUnreferenced != 0 || aggregate {
		event.Int("uploaded_unreferenced", fields.UploadedUnreferenced)
	}
	if fields.PersistenceStatus != "" {
		event.Str("persistence_status", string(fields.PersistenceStatus))
	}
	if fields.ReconciliationStatus != "" {
		event.Str("reconciliation_status", string(fields.ReconciliationStatus))
	}
	if fields.AccountDataStatus != "" {
		event.Str("account_data_status", string(fields.AccountDataStatus))
	}
	if fields.ProgressNoticeStatus != "" {
		event.Str("progress_notice_status", string(fields.ProgressNoticeStatus))
	}
	event.Msg("WeChat built-in emoji import")
}

func builtinEmojiEntryLogFields(account remoteapi.UserID, entry textemoji.Entry, stage builtinEmojiImportStage, code builtinEmojiLogCode) builtinEmojiLogFields {
	return builtinEmojiLogFields{
		Account:       account,
		Commit:        builtinEmojiSourceCommit,
		RegistryIndex: entry.Index,
		Chinese:       entry.Chinese,
		Name:          entry.English,
		Shortcode:     entry.Shortcode,
		Stage:         stage,
		Code:          code,
	}
}
