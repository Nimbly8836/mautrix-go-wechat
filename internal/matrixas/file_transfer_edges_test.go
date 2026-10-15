package matrixas

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"maunium.net/go/mautrix/event"
)

func TestFileUpdateUsesLegacyAppPlaceholder(t *testing.T) {
	h := newRecordTestMatrix(t, nil)
	pending := transferPlaceholder(h.portal)
	pending.Type = remoteapi.MsgApp
	pending.FileTransfer = nil
	pending.Media = nil
	h.server.OnMessage(t.Context(), pending)
	root, _, _ := h.snapshot()
	h.server.OnMessage(t.Context(), transferReady(h.portal))
	messages, _, _ := h.snapshot()
	if len(messages) != 3 {
		t.Fatalf("did not reuse legacy placeholder: %d", len(messages))
	}
	assertFileEdits(t, messages[1:], root[0].ID)
	ref, err := h.server.getMatrixMessageReference("wxid_self", h.portal, root[0].ID)
	if err != nil || ref.NewID != "200" {
		t.Fatalf("legacy root did not acquire canonical file: %+v %v", ref, err)
	}
}

func TestFileUpdateValidationAndReplyFallback(t *testing.T) {
	for _, mode := range []string{"sender", "filename", "namespace", "human"} {
		t.Run(mode, func(t *testing.T) {
			h := newRecordTestMatrix(t, nil)
			ref := messageReference{EventID: "$original", BridgeSender: true, MatrixSender: h.server.as.BotMXID(), Sender: "wxid_self", Type: remoteapi.MsgFile, Text: "dist.zip"}
			keys := []string{"new:100"}
			switch mode {
			case "sender":
				ref.Sender = "wxid_other"
			case "filename":
				ref.Text = "another.zip"
			case "namespace":
				keys = []string{"msg:100"}
			case "human":
				ref.BridgeSender = false
				ref.MatrixSender = "@human:example.com"
			}
			if err := h.server.saveMessageReference("wxid_self", h.portal, keys, ref); err != nil {
				t.Fatal(err)
			}
			h.server.OnMessage(t.Context(), transferReady(h.portal))
			messages, _, _ := h.snapshot()
			if len(messages) != 3 || messages[0].Content.NewContent != nil || !strings.Contains(messages[0].Content.Body, "[文件]") {
				t.Fatal("unsafe edit or missing file label")
			}
			if mode == "human" {
				if messages[0].Content.RelatesTo.GetReplyTo() != "$original" {
					t.Fatal("cannot edit human event: missing related file reply")
				}
			} else if messages[0].Content.RelatesTo.GetReplyTo() != "" {
				t.Fatal("unverified target became relation")
			}
			assertFileEdits(t, messages[1:], messages[0].ID)
		})
	}
}

func TestFileDownloadAndUploadHaveSeparateBudgets(t *testing.T) {
	h := newRecordTestMatrix(t, nil)
	ready := transferReady(h.portal)
	ready.FileTransfer = nil
	ready.FetchMedia = func(ctx context.Context) (*remoteapi.Media, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) < 9*time.Minute || time.Until(deadline) > 10*time.Minute {
			t.Error("download budget is not ten minutes")
		}
		return &remoteapi.Media{FileName: "dist.zip", Data: []byte("PK-test")}, nil
	}
	sawUpload := false
	h.server.as.BotIntent().Client.RequestHook = func(req *http.Request) {
		if strings.HasSuffix(req.URL.Path, "/upload") {
			sawUpload = true
			deadline, ok := req.Context().Deadline()
			if !ok || time.Until(deadline) < time.Minute || time.Until(deadline) > 2*time.Minute {
				t.Error("upload did not get its own two-minute budget")
			}
		}
	}
	h.server.OnMessage(t.Context(), ready)
	if !sawUpload {
		t.Fatal("missing upload")
	}
}

func TestFileDownloadFailureUpdatesMarkedOriginal(t *testing.T) {
	h := newRecordTestMatrix(t, nil)
	h.server.OnMessage(t.Context(), transferPlaceholder(h.portal))
	ready := transferReady(h.portal)
	ready.FetchMedia = func(context.Context) (*remoteapi.Media, error) { return nil, context.DeadlineExceeded }
	h.server.OnMessage(t.Context(), ready)
	messages, uploads, _ := h.snapshot()
	if len(messages) != 3 || len(uploads) != 0 {
		t.Fatal("failure generated another file card")
	}
	assertFileEdits(t, messages[1:], messages[0].ID)
	body := messages[2].Content.NewContent.Body
	if !strings.Contains(body, "[文件]") || !strings.Contains(body, "下载超时") {
		t.Fatalf("ambiguous file failure: %q", body)
	}
	if messages[2].Content.NewContent.MsgType != event.MsgText {
		t.Fatal("failure has invalid native file content")
	}
}
