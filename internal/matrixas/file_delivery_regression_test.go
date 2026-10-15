package matrixas

import (
	"strconv"
	"testing"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

func TestDeferredFileEditsUseIncreasingTimestamps(t *testing.T) {
	h := newRecordTestMatrix(t, nil)
	h.server.OnMessage(t.Context(), deferredTestCaption(h.portal))
	h.server.OnMessage(t.Context(), deferredTestMessage(h.portal))
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.timestamps) != 3 {
		t.Fatalf("unexpected send count %v", h.timestamps)
	}
	previous := int64(0)
	for _, raw := range h.timestamps {
		timestamp, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || timestamp <= previous {
			t.Fatalf("an older/progress edit may win aggregation: %v", h.timestamps)
		}
		previous = timestamp
	}
}

func TestDeferredFileReplayAfterRestartDoesNotCreateAnotherRoot(t *testing.T) {
	for _, withCaption := range []bool{false, true} {
		h := newRecordTestMatrix(t, nil)
		if withCaption {
			caption := deferredTestCaption(h.portal)
			caption.ID = "100"
			caption.DeduplicationKeys = []string{"new:100"}
			h.server.OnMessage(t.Context(), caption)
		}
		m := deferredTestMessage(h.portal)
		m.ID = "200"
		m.DeduplicationKeys = []string{"new:200"}
		h.server.OnMessage(t.Context(), m)
		before, uploads, _ := h.snapshot()
		h.server.references = nil
		h.server.fileCaptions = nil
		h.server.OnMessage(t.Context(), m)
		after, moreUploads, _ := h.snapshot()
		if len(before) != len(after) || len(uploads) != len(moreUploads) {
			t.Fatalf("replayed a completed file (caption=%v): events %d->%d uploads %d->%d", withCaption, len(before), len(after), len(uploads), len(moreUploads))
		}
		ref, err := h.server.getMatrixMessageReference("wxid_self", h.portal, before[0].ID)
		if err != nil || ref.NewID != "200" || ref.Type != remoteapi.MsgFile {
			t.Fatalf("wrong native file target after restart: %+v %v", ref, err)
		}
	}
}
