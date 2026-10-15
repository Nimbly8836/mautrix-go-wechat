package matrixas

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func deferredTestMessage(rec portalRecord) *remoteapi.InboundMessage {
	return &remoteapi.InboundMessage{Account: "wxid_self", ID: "file-2", Chat: rec.ChatID, Sender: "wxid_self", Type: remoteapi.MsgFile, Text: "report_1__final.pdf", Timestamp: time.Unix(1710000012, 0), Media: &remoteapi.Media{FileName: "report_1__final.pdf"}, FetchMedia: func(ctx context.Context) (*remoteapi.Media, error) {
		return &remoteapi.Media{FileName: "report_1__final.pdf", MimeType: "application/pdf", Data: []byte("%PDF-test"), Size: 999}, nil
	}}
}

func deferredTestCaption(rec portalRecord) *remoteapi.InboundMessage {
	return &remoteapi.InboundMessage{Account: "wxid_self", ID: "caption-1", Chat: rec.ChatID, Sender: "wxid_self", Type: remoteapi.MsgText, Text: "report(1)-final.pdf", Timestamp: time.Unix(1710000000, 0)}
}

func assertFileEdits(t *testing.T, messages []recordTestMessage, root id.EventID) {
	t.Helper()
	for _, msg := range messages {
		if msg.Content.NewContent == nil || msg.Content.RelatesTo == nil || msg.Content.RelatesTo.Type != event.RelReplace || msg.Content.RelatesTo.EventID != root {
			t.Fatalf("not an edit of %s: %+v", root, msg.Content)
		}
	}
}

func TestDeferredFileOneRootNativeEdit(t *testing.T) {
	for _, caption := range []bool{false, true} {
		t.Run(fmt.Sprint(caption), func(t *testing.T) {
			h := newRecordTestMatrix(t, nil)
			m := deferredTestMessage(h.portal)
			if caption {
				h.server.bridgeRemoteToPortal(t.Context(), h.portal, deferredTestCaption(h.portal))
			}
			h.server.bridgeRemoteToPortal(t.Context(), h.portal, m)
			messages, uploads, _ := h.snapshot()
			if len(messages) != 3 || len(uploads) != 1 || messages[0].Content.NewContent != nil || messages[0].Content.MsgType != event.MsgText {
				t.Fatalf("wanted one root and two edits, got %+v, uploads=%d", messages, len(uploads))
			}
			assertFileEdits(t, messages[1:], messages[0].ID)
			for _, msg := range messages {
				if msg.Sender != messages[0].Sender {
					t.Fatalf("edit changed actual sender: %+v", messages)
				}
			}
			final := messages[2].Content.NewContent
			if final.MsgType != event.MsgFile || final.Body != m.Media.FileName || final.FileName != m.Media.FileName || final.URL != "mxc://example.com/upload1" || final.Info == nil || final.Info.Size != len("%PDF-test") || final.Info.MimeType != "application/pdf" {
				t.Fatalf("incorrect native file new_content: %+v", final)
			}
			if messages[1].TxnID == messages[2].TxnID || !strings.HasPrefix(messages[1].TxnID, "wechat-file-") || messages[2].TxnID != fileLifecycleTransactionID("wxid_self", h.portal, m, messages[0].ID, "complete") {
				t.Fatal("non-deterministic or shared phase transaction IDs")
			}
			if caption && h.server.getMessageEvent(m.Chat, "caption-1") != messages[0].ID {
				t.Fatal("caption alias overwritten")
			}
			if h.server.getMessageEvent(m.Chat, m.ID) != messages[0].ID {
				t.Fatal("file alias is not the visible root")
			}
		})
	}
}

func TestDeferredFileRetainsActualBotFallbackClient(t *testing.T) {
	h := newRecordTestMatrix(t, nil)
	caption := deferredTestCaption(h.portal)
	caption.Sender = "remote-friend"
	client := h.server.as.BotIntent().Client
	resp, err := client.SendMessageEvent(t.Context(), h.portal.RoomID, event.EventMessage, &event.MessageEventContent{MsgType: event.MsgText, Body: caption.Text})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.server.rememberMessageReference("wxid_self", h.portal, caption, resp.EventID, client); err != nil {
		t.Fatal(err)
	}
	d := h.server.beginFileCaptionDelivery("wxid_self", h.portal, caption)
	h.server.rememberFileCaption(d, caption, resp.EventID, client)
	// A ghost is now available, but edits must still use the original bot.
	h.server.cfg.Appservice.ID = "wechat"
	h.server.cfg.Homeserver.Domain = "example.com"
	h.server.markPuppetJoined(h.portal.RoomID, caption.Sender)
	m := deferredTestMessage(h.portal)
	m.Sender = caption.Sender
	h.server.bridgeRemoteToPortal(t.Context(), h.portal, m)
	messages, _, _ := h.snapshot()
	if len(messages) != 3 {
		t.Fatalf("got %d events", len(messages))
	}
	for _, msg := range messages {
		if msg.Sender != h.server.as.BotMXID() {
			t.Fatalf("sender changed to ghost: %s", msg.Sender)
		}
	}
}

func TestFileCaptionMatchIsNarrow(t *testing.T) {
	for _, tc := range []struct {
		name, caption, file string
		delta               time.Duration
		want                bool
	}{
		{"exact", "a.pdf", "a.pdf", 12 * time.Second, true},
		{"observed", "report(1)-final.pdf", "report_1__final.pdf", 12 * time.Second, true},
		{"boundary", "a.pdf", "a.pdf", 30 * time.Second, true},
		{"too-late", "a.pdf", "a.pdf", 30*time.Second + 1, false},
		{"negative", "a.pdf", "a.pdf", -1, false},
		{"no-fuzzy", "a,b.pdf", "a_b.pdf", 12 * time.Second, false},
		{"no-reverse", "a_b.pdf", "a-b.pdf", 12 * time.Second, false},
		{"no-substring", "notes a.pdf", "a.pdf", 12 * time.Second, false},
		{"multiline", "a.pdf\na.pdf", "a.pdf", 12 * time.Second, false},
		{"leading-space", " a.pdf", "a.pdf", 12 * time.Second, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, rec := newPortalMessageTestServer(t, &portalMessageAPI{})
			caption := deferredTestCaption(rec)
			caption.Text = tc.caption
			d := s.beginFileCaptionDelivery("wxid_self", rec, caption)
			s.rememberFileCaption(d, caption, "$caption", s.as.BotIntent().Client)
			m := deferredTestMessage(rec)
			m.Media.FileName = tc.file
			m.Timestamp = caption.Timestamp.Add(tc.delta)
			got := s.beginFileCaptionDelivery("wxid_self", rec, m).claimed != nil
			if got != tc.want {
				t.Fatalf("matched=%v want %v", got, tc.want)
			}
		})
	}
}

func TestFileCaptionScopeAndIneligibleArrivals(t *testing.T) {
	for _, mode := range []string{"account", "chat", "room", "sender", "generation", "reply", "record", "solitaire", "intervening", "zero-time", "unknown-sender", "empty-id", "ambiguous", "no-candidate"} {
		t.Run(mode, func(t *testing.T) {
			s, rec := newPortalMessageTestServer(t, &portalMessageAPI{})
			caption := deferredTestCaption(rec)
			caption.DeliveryOrder = 10
			switch mode {
			case "reply":
				caption.Reply = &remoteapi.Reply{Text: "quoted"}
			case "record":
				caption.Record = &remoteapi.ForwardedRecord{}
			case "solitaire":
				caption.Solitaire = &remoteapi.SolitaireSource{}
			case "zero-time":
				caption.Timestamp = time.Time{}
			case "unknown-sender":
				caption.Sender = ""
			case "empty-id":
				caption.ID = ""
			}
			d := s.beginFileCaptionDelivery("wxid_self", rec, caption)
			if mode != "no-candidate" {
				s.rememberFileCaption(d, caption, "$caption", s.as.BotIntent().Client)
			}
			m := deferredTestMessage(rec)
			m.DeliveryOrder = 30
			account := remoteapi.UserID("wxid_self")
			switch mode {
			case "account":
				account = "other"
			case "chat":
				rec.ChatID = "other-chat"
				m.Chat = rec.ChatID
			case "room":
				rec.RoomID = "!other:example.com"
			case "sender":
				m.Sender = "other-sender"
			case "generation":
				s.remoteGeneration++
			case "intervening":
				other := *caption
				other.Text = "not a filename"
				other.DeliveryOrder = 20
				s.beginFileCaptionDelivery(account, rec, &other)
			case "ambiguous":
				s.beginFileCaptionDelivery(account, rec, caption)
			}
			if s.beginFileCaptionDelivery(account, rec, m).claimed != nil {
				t.Fatal("unsafe caption match")
			}
		})
	}
}

func TestFileCaptionArrivalOrderNotAckOrder(t *testing.T) {
	for _, intervening := range []bool{false, true} {
		t.Run(fmt.Sprint(intervening), func(t *testing.T) {
			s, rec := newPortalMessageTestServer(t, &portalMessageAPI{})
			caption := deferredTestCaption(rec)
			caption.DeliveryOrder = 10
			d := s.beginFileCaptionDelivery("wxid_self", rec, caption)
			later := *caption
			later.Text = "later unrelated"
			later.DeliveryOrder = 40
			if intervening {
				later.DeliveryOrder = 20
			}
			s.beginFileCaptionDelivery("wxid_self", rec, &later)
			// Caption acknowledgement arrives last; a file with order 30 was
			// waiting on that ACK while the ordinary later message passed it.
			s.rememberFileCaption(d, caption, "$caption", s.as.BotIntent().Client)
			file := deferredTestMessage(rec)
			file.DeliveryOrder = 30
			claim := s.beginFileCaptionDelivery("wxid_self", rec, file).claimed
			if (claim != nil) == intervening {
				t.Fatalf("incorrect predecessor: %+v", claim)
			}
			file.ID = "second-file"
			file.DeliveryOrder = 31
			if s.beginFileCaptionDelivery("wxid_self", rec, file).claimed != nil {
				t.Fatal("claimed twice")
			}
		})
	}
}

func TestFileCaptionAtomicClaimAndBounds(t *testing.T) {
	s, rec := newPortalMessageTestServer(t, &portalMessageAPI{})
	caption := deferredTestCaption(rec)
	d := s.beginFileCaptionDelivery("wxid_self", rec, caption)
	s.rememberFileCaption(d, caption, "$caption", s.as.BotIntent().Client)
	var claims atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s.beginFileCaptionDelivery("wxid_self", rec, deferredTestMessage(rec)).claimed != nil {
				claims.Add(1)
			}
		}()
	}
	wg.Wait()
	if claims.Load() != 1 {
		t.Fatalf("claims=%d", claims.Load())
	}
	if len(s.fileCaptions[d.key]) > 8 {
		t.Fatal("unbounded per-scope history")
	}
	for i := 0; i < fileCandidateLimit+5; i++ {
		m := *caption
		m.Sender = remoteapi.UserID(fmt.Sprintf("sender%d", i))
		s.beginFileCaptionDelivery("wxid_self", rec, &m)
	}
	if len(s.fileCaptions) != fileCandidateLimit {
		t.Fatalf("unbounded scopes: %d", len(s.fileCaptions))
	}
	for _, history := range s.fileCaptions {
		for _, entry := range history {
			entry.expires = time.Now().Add(-time.Minute)
		}
	}
	s.beginFileCaptionDelivery("wxid_self", rec, caption)
	if len(s.fileCaptions) != 1 {
		t.Fatal("expired scopes retained")
	}
}

func TestDeferredFileErrorsRemainEdits(t *testing.T) {
	for _, mode := range []string{"fetch", "empty", "upload", "complete-edit", "progress-edit", "root", "persist"} {
		t.Run(mode, func(t *testing.T) {
			failed := map[string]bool{}
			if mode == "upload" {
				failed["report_1__final.pdf"] = true
			}
			h := newRecordTestMatrix(t, failed)
			m := deferredTestMessage(h.portal)
			fetchCalls := 0
			original := m.FetchMedia
			m.FetchMedia = func(ctx context.Context) (*remoteapi.Media, error) {
				fetchCalls++
				if mode == "fetch" {
					return nil, errors.New("password=private https://secret.invalid?token=private")
				}
				if mode == "empty" {
					return &remoteapi.Media{}, nil
				}
				return original(ctx)
			}
			if mode == "complete-edit" {
				h.failedSend = map[string]bool{"report_1__final.pdf": true}
			}
			if mode == "root" {
				h.failedRoot = true
			}
			if mode == "persist" {
				h.server.referenceStoreErr = errors.New("private disk failure")
			}
			if mode == "progress-edit" {
				h.server.bridgeRemoteToPortal(t.Context(), h.portal, deferredTestCaption(h.portal))
				h.failedRoot = true
			}
			h.server.bridgeRemoteToPortal(t.Context(), h.portal, m)
			messages, _, _ := h.snapshot()
			if mode == "root" {
				if len(messages) != 0 || fetchCalls != 0 {
					t.Fatal("uncertain root continued")
				}
				return
			}
			if len(messages) == 0 {
				t.Fatal("missing root")
			}
			assertFileEdits(t, messages[1:], messages[0].ID)
			if mode == "persist" || mode == "progress-edit" {
				if len(messages) != 1 || fetchCalls != 0 {
					t.Fatal("continued after reference/progress failure")
				}
				return
			}
			last := messages[len(messages)-1].Content.NewContent
			if last == nil || last.MsgType != event.MsgText || !strings.Contains(last.Body, "文件传输失败") || strings.Contains(last.Body, "private") || strings.Contains(last.Body, "secret") {
				t.Fatalf("unsafe failure: %+v", last)
			}
			if len(messages) != 3 {
				t.Fatalf("separate failure event or wrong count: %d", len(messages))
			}
		})
	}
}

func TestDeferredFileContextAndScope(t *testing.T) {
	for _, mode := range []string{"budget", "cancel", "deadline", "portal", "account-generation", "already-cancelled"} {
		t.Run(mode, func(t *testing.T) {
			h := newRecordTestMatrix(t, nil)
			m := deferredTestMessage(h.portal)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "deadline" {
				var done context.CancelFunc
				ctx, done = context.WithTimeout(ctx, 30*time.Millisecond)
				defer done()
			}
			if mode == "already-cancelled" {
				cancel()
			}
			original := m.FetchMedia
			m.FetchMedia = func(work context.Context) (*remoteapi.Media, error) {
				deadline, ok := work.Deadline()
				if !ok || time.Until(deadline) > deferredFileTimeout {
					t.Error("unbounded download context")
				}
				switch mode {
				case "cancel":
					cancel()
					return nil, work.Err()
				case "deadline":
					<-work.Done()
					return nil, work.Err()
				case "budget":
					return nil, context.DeadlineExceeded
				case "portal":
					h.server.portals.mu.Lock()
					delete(h.server.portals.data.Portals, string(m.Chat))
					h.server.portals.mu.Unlock()
				case "account-generation":
					h.server.mu.Lock()
					h.server.remoteGeneration++
					h.server.mu.Unlock()
				case "already-cancelled":
					t.Error("fetch started after cancellation")
				}
				return original(work)
			}
			h.server.bridgeRemoteToPortal(ctx, h.portal, m)
			messages, uploads, _ := h.snapshot()
			want := 2
			if mode == "budget" {
				want = 3
			}
			if mode == "already-cancelled" {
				want = 0
			}
			if len(messages) != want || len(uploads) != 0 {
				t.Fatalf("post-cancel/scope publish: events=%d uploads=%d", len(messages), len(uploads))
			}
			if len(messages) > 1 {
				assertFileEdits(t, messages[1:], messages[0].ID)
			}
		})
	}
}

func TestDeferredFilePreservesRootReply(t *testing.T) {
	h := newRecordTestMatrix(t, nil)
	target := deferredTestCaption(h.portal)
	target.Text = "original message"
	h.server.bridgeRemoteToPortal(t.Context(), h.portal, target)
	m := deferredTestMessage(h.portal)
	m.Reply = &remoteapi.Reply{Target: target.ID, Text: target.Text}
	h.server.bridgeRemoteToPortal(t.Context(), h.portal, m)
	messages, _, _ := h.snapshot()
	if len(messages) != 4 {
		t.Fatalf("got %d events", len(messages))
	}
	root := messages[1]
	if root.Content.RelatesTo.GetReplyTo() != messages[0].ID {
		t.Fatal("missing root reply")
	}
	assertFileEdits(t, messages[2:], root.ID)
	for _, edit := range messages[2:] {
		if edit.Content.NewContent.RelatesTo.GetReplyTo() != messages[0].ID {
			t.Fatal("edit lost root reply relation")
		}
	}
}

func TestDeferredFileUploadCancellationAndFailureBudget(t *testing.T) {
	for _, mode := range []string{"upload-cancel", "failure-budget", "queue-cancel", "progress-persist"} {
		t.Run(mode, func(t *testing.T) {
			h := newRecordTestMatrix(t, nil)
			m := deferredTestMessage(h.portal)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			observed := false
			if mode == "failure-budget" {
				m.FetchMedia = func(context.Context) (*remoteapi.Media, error) { return nil, context.DeadlineExceeded }
			}
			if mode == "queue-cancel" {
				slots := h.server.fileTransferSlots()
				for i := 0; i < 4; i++ {
					slots <- struct{}{}
				}
				defer func() {
					for i := 0; i < 4; i++ {
						<-slots
					}
				}()
			}
			h.beforeSend = func(content event.MessageEventContent) {
				if content.NewContent == nil {
					return
				}
				if mode == "queue-cancel" {
					cancel()
					observed = true
				}
				if mode == "progress-persist" {
					h.server.mu.Lock()
					h.server.referenceStoreErr = errors.New("private persistence failure")
					h.server.mu.Unlock()
					observed = true
				}
			}
			h.server.as.BotIntent().Client.RequestHook = func(req *http.Request) {
				if strings.HasSuffix(req.URL.Path, "/upload") && mode == "upload-cancel" {
					deadline, ok := req.Context().Deadline()
					if !ok || time.Until(deadline) > deferredFileTimeout {
						t.Error("upload escaped work budget")
					}
					observed = true
					cancel()
				}
				if mode == "failure-budget" && strings.HasSuffix(req.URL.Path, fileLifecycleTransactionID("wxid_self", h.portal, m, "$record1", "failure")) {
					deadline, ok := req.Context().Deadline()
					if !ok || time.Until(deadline) > deferredFileFailureTimeout || time.Until(deadline) <= 0 {
						t.Error("failure edit has no separate bounded budget")
					}
					observed = true
				}
			}
			h.server.bridgeRemoteToPortal(ctx, h.portal, m)
			messages, uploads, _ := h.snapshot()
			want := 2
			if mode == "failure-budget" {
				want = 3
			}
			if !observed || len(messages) != want || len(uploads) != 0 {
				t.Fatalf("observed=%v events=%d uploads=%d", observed, len(messages), len(uploads))
			}
			assertFileEdits(t, messages[1:], messages[0].ID)
		})
	}
}

func TestDeferredFileConcurrencyFour(t *testing.T) {
	h := newRecordTestMatrix(t, nil)
	// Server.Run initializes the SDK bot intent before starting the event pump.
	// Match that lifecycle: the SDK's lazy initialization itself isn't concurrent.
	h.server.as.BotIntent()
	started := make(chan struct{}, 6)
	release := make(chan struct{})
	var active, maximum atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		m := deferredTestMessage(h.portal)
		m.ID = remoteapi.MessageID(fmt.Sprintf("file-%d", i))
		m.FetchMedia = func(ctx context.Context) (*remoteapi.Media, error) {
			n := active.Add(1)
			defer active.Add(-1)
			for {
				old := maximum.Load()
				if n <= old || maximum.CompareAndSwap(old, n) {
					break
				}
			}
			started <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return &remoteapi.Media{Data: []byte("content"), FileName: "a.bin"}, nil
		}
		wg.Add(1)
		go func() { defer wg.Done(); h.server.bridgeRemoteToPortal(t.Context(), h.portal, m) }()
	}
	for i := 0; i < 4; i++ {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			close(release)
			wg.Wait()
			t.Fatal("four transfers did not start")
		}
	}
	select {
	case <-started:
		t.Error("fifth transfer started before a slot was released")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	wg.Wait()
	if maximum.Load() != 4 {
		t.Fatalf("peak concurrency=%d", maximum.Load())
	}
	messages, uploads, _ := h.snapshot()
	if len(messages) != 18 || len(uploads) != 6 {
		t.Fatalf("lost transfers: messages=%d uploads=%d", len(messages), len(uploads))
	}
}
