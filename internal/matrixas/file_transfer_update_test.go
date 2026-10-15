package matrixas

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"maunium.net/go/mautrix/event"
)

func transferPlaceholder(rec portalRecord) *remoteapi.InboundMessage {
	return &remoteapi.InboundMessage{Account: "wxid_self", Chat: rec.ChatID, Sender: "wxid_self", ID: "100", DeduplicationKeys: []string{"new:100"}, Type: remoteapi.MsgFile, Text: "dist.zip", Timestamp: time.Unix(1710000000, 0), Media: &remoteapi.Media{FileName: "dist.zip", Size: 66076955}, FileTransfer: &remoteapi.FileTransfer{Pending: true}}
}
func transferReady(rec portalRecord) *remoteapi.InboundMessage {
	m := deferredTestMessage(rec)
	m.ID = "200"
	m.DeduplicationKeys = []string{"new:200"}
	m.Text = "dist.zip"
	m.Media.FileName = "dist.zip"
	m.Timestamp = time.Unix(1710000600, 0) // Beyond filename heuristics; explicit link wins.
	m.FileTransfer = &remoteapi.FileTransfer{Replaces: "100", TargetKey: "new:100"}
	m.FetchMedia = func(context.Context) (*remoteapi.Media, error) {
		return &remoteapi.Media{FileName: "dist.zip", MimeType: "application/zip", Data: []byte("PK-test")}, nil
	}
	return m
}
func TestFileUploadPlaceholderBecomesOneNativeFile(t *testing.T) {
	h := newRecordTestMatrix(t, nil)
	pending := transferPlaceholder(h.portal)
	h.server.OnMessage(t.Context(), pending)
	messages, uploads, _ := h.snapshot()
	if len(messages) != 1 || len(uploads) != 0 || !strings.Contains(messages[0].Content.Body, "[文件]") || !strings.Contains(messages[0].Content.Body, "等待") {
		t.Fatalf("upload placeholder not represented as a file: %+v", messages)
	}
	root := messages[0].ID
	// Explicit IDs, not volatile filename cache, must work across restart.
	h.server.references = nil
	h.server.fileCaptions = nil
	ready := transferReady(h.portal)
	h.server.OnMessage(t.Context(), ready)
	messages, uploads, _ = h.snapshot()
	if len(messages) != 3 || len(uploads) != 1 {
		t.Fatalf("duplicate file root or missing upload: events=%d uploads=%d", len(messages), len(uploads))
	}
	assertFileEdits(t, messages[1:], root)
	if messages[2].Content.NewContent.MsgType != event.MsgFile {
		t.Fatal("completion is not a native file")
	}
	ref, err := h.server.getMatrixMessageReference("wxid_self", h.portal, root)
	if err != nil || ref.NewID != "200" {
		t.Fatalf("file identity not canonical: %+v %v", ref, err)
	}
	h.server.OnMessage(t.Context(), pending)
	again, _, _ := h.snapshot()
	if len(again) != 3 {
		t.Fatal("replayed type74 created another visible entry")
	}
}
func TestFileExplicitUpdateCannotCrossSenderOrGuessAnotherCaption(t *testing.T) {
	h := newRecordTestMatrix(t, nil)
	pending := transferPlaceholder(h.portal)
	h.server.OnMessage(t.Context(), pending)
	// A real intervening filename must not replace an explicitly named target.
	text := deferredTestCaption(h.portal)
	text.Text = "dist.zip"
	text.Timestamp = time.Unix(1710000599, 0)
	h.server.OnMessage(t.Context(), text)
	ready := transferReady(h.portal)
	ready.FileTransfer.Replaces = "999"
	ready.FileTransfer.TargetKey = "new:999"
	h.server.OnMessage(t.Context(), ready)
	messages, _, _ := h.snapshot()
	if len(messages) != 5 || messages[2].Content.NewContent != nil || !strings.Contains(messages[2].Content.Body, "[文件]") {
		t.Fatalf("explicit target failure guessed another caption: %+v", messages)
	}
	assertFileEdits(t, messages[3:], messages[2].ID)
}
