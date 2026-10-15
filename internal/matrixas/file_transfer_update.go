package matrixas

import (
	"fmt"
	"strings"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

func isPendingFile(m *remoteapi.InboundMessage) bool {
	return m.Type == remoteapi.MsgFile && m.FileTransfer != nil && m.FileTransfer.Pending
}

func fileStatusBody(name, status string, size int64) string {
	body := "[文件] " + recordShortText(name, 512)
	if size > 0 {
		body += fmt.Sprintf(" · %.1f MiB", float64(size)/(1024*1024))
	}
	if status != "" {
		body += "（" + status + "）"
	}
	return body
}

func fileNamesMatch(caption, file string) bool {
	if !isFileCaptionName(caption) || !isFileCaptionName(file) {
		return false
	}
	return caption == file || strings.NewReplacer("(", "_", ")", "_", "（", "_", "）", "_", "-", "_").Replace(caption) == file
}

func (s *Server) knownFileMessageReference(account remoteapi.UserID, rec portalRecord, m *remoteapi.InboundMessage) messageReference {
	for _, key := range m.DeduplicationKeys {
		if strings.HasPrefix(key, "new:") {
			if ref := s.getMessageReference(account, rec, remoteapi.MessageID(strings.TrimPrefix(key, "new:")), key); ref.EventID != "" {
				return ref
			}
		}
	}
	return messageReference{}
}

// The backend's replacement ID takes precedence over filename/time heuristics.
// This lookup is scoped and never authorizes editing a different sender's event.
func (s *Server) explicitFileUpdateReference(account remoteapi.UserID, rec portalRecord, m *remoteapi.InboundMessage) messageReference {
	update := m.FileTransfer
	if update == nil || update.Pending || update.Replaces == "" || update.TargetKey != "new:"+string(update.Replaces) {
		return messageReference{}
	}
	ref := s.getMessageReference(account, rec, update.Replaces, update.TargetKey)
	if ref.EventID == "" || ref.Sender != m.Sender || !fileNamesMatch(ref.Text, fileLifecycleName(m)) {
		return messageReference{}
	}
	if ref.Type != remoteapi.MsgFile && ref.Type != remoteapi.MsgApp && ref.Type != remoteapi.MsgText {
		return messageReference{}
	}
	return ref
}
