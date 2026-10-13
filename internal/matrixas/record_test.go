package matrixas

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func TestRecordRenderingEscapesLabelsTextAndNestedHistory(t *testing.T) {
	attack := `<script>alert("x")</script><img src=x onerror=alert(1)>`
	record := &remoteapi.ForwardedRecord{Title: attack, Items: []remoteapi.RecordItem{
		{SenderName: attack, Time: attack, Text: "first\n" + attack, MediaStatus: attack},
		{Text: "second", Record: &remoteapi.ForwardedRecord{Title: "nested", Items: []remoteapi.RecordItem{{SenderName: "historical", Text: "third"}}}},
		{Text: "fourth", Media: &remoteapi.Media{FileName: attack, URL: "https://secret.invalid/?token=hidden"}},
	}}
	content := matrixContentFromRemote(&remoteapi.InboundMessage{Type: remoteapi.MsgApp, Record: record})
	if content.MsgType != event.MsgText || content.Format != event.FormatHTML || !strings.Contains(content.FormattedBody, html.EscapeString(attack)) {
		t.Fatalf("missing safe rich transcript: %+v", content)
	}
	if strings.Contains(content.FormattedBody, "<script") || strings.Contains(content.FormattedBody, "<img") || strings.Contains(content.Body, "hidden") {
		t.Fatalf("unsafe transcript: %+v", content)
	}
	for _, text := range []string{content.Body, content.FormattedBody} {
		last := -1
		for _, word := range []string{"first", "second", "nested", "third", "fourth"} {
			pos := strings.Index(text, word)
			if pos <= last {
				t.Fatalf("order lost at %q: %s", word, text)
			}
			last = pos
		}
	}
}

type recordTestUpload struct {
	name, mime, body string
}

type recordTestMessage struct {
	ID      id.EventID
	Sender  id.UserID
	Content event.MessageEventContent
	TxnID   string
}

type recordTestMatrix struct {
	server     *Server
	portal     portalRecord
	mu         sync.Mutex
	messages   []recordTestMessage
	attempts   []recordTestMessage
	timestamps []string
	uploads    []recordTestUpload
	redactions []id.EventID
	failedSend map[string]bool
	failedRoot bool
	beforeSend func(event.MessageEventContent)
	txnEvents  map[string]id.EventID
}

func newRecordTestMatrix(t *testing.T, failedNames map[string]bool) *recordTestMatrix {
	t.Helper()
	s, rec := newPortalMessageTestServer(t, &portalMessageAPI{})
	s.portals.path = filepath.Join(t.TempDir(), "portals.json")
	h := &recordTestMatrix{server: s, portal: rec, txnEvents: make(map[string]id.EventID)}
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		defer h.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/upload"):
			data, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}
			name := r.URL.Query().Get("filename")
			h.uploads = append(h.uploads, recordTestUpload{name, r.Header.Get("Content-Type"), string(data)})
			if user := r.URL.Query().Get("user_id"); user != "" && user != string(s.as.BotMXID()) {
				t.Errorf("upload did not use bot: %s", user)
			}
			if failedNames[name] {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"errcode":"M_FORBIDDEN","error":"secret-token=do-not-leak"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"content_uri": fmt.Sprintf("mxc://example.com/upload%d", len(h.uploads))})
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/send/m.room.message/"):
			var content event.MessageEventContent
			if err := json.NewDecoder(r.Body).Decode(&content); err != nil {
				t.Error(err)
			}
			sender := id.UserID(r.URL.Query().Get("user_id"))
			if sender == "" {
				sender = s.as.BotMXID()
			}
			txnID := strings.Split(r.URL.Path, "/send/m.room.message/")[1]
			h.attempts = append(h.attempts, recordTestMessage{Sender: sender, Content: content, TxnID: txnID})
			h.timestamps = append(h.timestamps, r.URL.Query().Get("ts"))
			if h.beforeSend != nil {
				h.beforeSend(content)
			}
			if h.failedSend[content.FileName] || (h.failedRoot && content.MsgType == event.MsgText) {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"errcode":"M_FORBIDDEN","error":"secret-token=do-not-leak"}`))
				return
			}
			messageID := h.txnEvents[txnID]
			if messageID == "" {
				messageID = id.EventID(fmt.Sprintf("$record%d", len(h.messages)+1))
				h.messages = append(h.messages, recordTestMessage{messageID, sender, content, txnID})
				h.txnEvents[txnID] = messageID
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"event_id": string(messageID)})
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/redact/"):
			h.redactions = append(h.redactions, id.EventID(strings.Split(strings.Split(r.URL.Path, "/redact/")[1], "/")[0]))
			_, _ = w.Write([]byte(`{"event_id":"$redacted"}`))
		case strings.HasSuffix(r.URL.Path, "/register"):
			_ = json.NewEncoder(w).Encode(map[string]string{"user_id": string(s.as.BotMXID())})
		case strings.Contains(r.URL.Path, "/join"):
			_ = json.NewEncoder(w).Encode(map[string]string{"room_id": string(rec.RoomID)})
		default:
			t.Errorf("unexpected Matrix request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	t.Cleanup(hs.Close)
	if err := s.as.SetHomeserverURL(hs.URL); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *recordTestMatrix) snapshot() ([]recordTestMessage, []recordTestUpload, []id.EventID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]recordTestMessage(nil), h.messages...), append([]recordTestUpload(nil), h.uploads...), append([]id.EventID(nil), h.redactions...)
}

func TestRecordHistoryAttachmentsKeepOuterTimestamp(t *testing.T) {
	h := newRecordTestMatrix(t, nil)
	m := &remoteapi.InboundMessage{ID: "history", Chat: h.portal.ChatID, Sender: "wxid_self", Timestamp: time.Unix(1710000000, 0), Record: &remoteapi.ForwardedRecord{Items: []remoteapi.RecordItem{{Media: &remoteapi.Media{FileName: "clip.mp4", MimeType: "video/mp4", Data: []byte("clip")}}}}}
	h.server.OnMessage(t.Context(), m)
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.timestamps) != 2 {
		t.Fatalf("missing summary/attachment: %v", h.timestamps)
	}
	for _, timestamp := range h.timestamps {
		if timestamp != "1710000000000" {
			t.Errorf("history attachment moved to current time: %q", timestamp)
		}
	}
}

func TestRecordOnMessageNativeMediaAndDurableReplyRecall(t *testing.T) {
	h := newRecordTestMatrix(t, map[string]bool{"failed.mp4": true})
	m := &remoteapi.InboundMessage{
		ID: "bundle", Chat: h.portal.ChatID, Sender: "wxid_self", Type: remoteapi.MsgEmoji,
		Media: &remoteapi.Media{Data: []byte("must not upload outer media")},
		Extra: map[string]any{"fromnewmsgid": "777"},
		Record: &remoteapi.ForwardedRecord{Title: "Quoted history", Items: []remoteapi.RecordItem{
			{SenderName: "NOT a Matrix sender", Time: "yesterday", Text: "image", Media: &remoteapi.Media{FileName: `photo".png`, Data: []byte("\x89PNG\r\n\x1a\nimage"), URL: "https://secret.invalid/?token=hidden"}},
			{SenderName: "second", Text: "video", Media: &remoteapi.Media{FileName: "failed.mp4", MimeType: "video/mp4", Data: []byte("video")}},
			{SenderName: "video author", Time: "a long time ago", Media: &remoteapi.Media{FileName: "clip.mp4", MimeType: "video/mp4", Data: []byte("clip")}},
			{Record: &remoteapi.ForwardedRecord{Title: "nested", Items: []remoteapi.RecordItem{{SenderName: "third", Media: &remoteapi.Media{FileName: "notes.txt", Data: []byte("notes")}}}}},
			{Text: "missing", MediaStatus: "附件下载失败", Media: &remoteapi.Media{FileName: "missing.bin", URL: "https://secret.invalid/?token=hidden"}},
		}},
	}
	// At the start of each native send, the previous confirmed prefix must
	// already survive a fresh journal load, not merely an in-memory lookup.
	confirmed := 0
	h.mu.Lock()
	h.beforeSend = func(content event.MessageEventContent) {
		if !content.MsgType.IsMedia() {
			return
		}
		store, err := h.server.referenceStore()
		if err != nil {
			t.Error(err)
			return
		}
		reloaded, err := openMessageReferenceStore(store.path)
		if err != nil {
			t.Error(err)
			return
		}
		ref := reloaded.forward[messageReferenceKey{"wxid_self", m.Chat, h.portal.RoomID, "id:bundle"}]
		if ref.EventID != "$record1" || len(ref.RelatedEventIDs) != confirmed {
			t.Errorf("prefix not durable before next child: %+v, expected %d children", ref, confirmed)
		}
		confirmed++
	}
	h.mu.Unlock()
	before, _ := json.Marshal(m)
	h.server.OnMessage(t.Context(), m)
	messages, uploads, _ := h.snapshot()
	if len(messages) != 4 || len(uploads) != 4 {
		t.Fatalf("native events/uploads missing: messages=%d uploads=%+v", len(messages), uploads)
	}
	content := messages[0].Content
	if content.MsgType != event.MsgText || messages[0].Sender != h.server.as.BotMXID() {
		t.Fatalf("wrong root type/identity: %+v", messages[0])
	}
	for _, marker := range []string{"[图片]", "[视频]", "[文件]", "failed.mp4", "附件上传失败", "附件下载失败", "missing.bin", "下方原生媒体"} {
		if !strings.Contains(content.Body, marker) || !strings.Contains(content.FormattedBody, marker) {
			t.Errorf("missing item status %q: %+v", marker, content)
		}
	}
	if strings.Contains(content.FormattedBody, "<img") || strings.Contains(content.Body+content.FormattedBody, "mxc://") || strings.Contains(content.Body+content.FormattedBody, "secret") {
		t.Fatal("summary relies on raw MXC anchors or leaks backend URLs")
	}
	for i, want := range []struct {
		msgType                event.MessageType
		name, mime, url, label string
		size                   int
	}{
		{event.MsgImage, `photo".png`, "image/png", "mxc://example.com/upload1", "#1 NOT a Matrix sender yesterday", 13},
		{event.MsgVideo, "clip.mp4", "video/mp4", "mxc://example.com/upload3", "#3 video author a long time ago", 4},
		{event.MsgFile, "notes.txt", "text/plain; charset=utf-8", "mxc://example.com/upload4", "#5 third", 5},
	} {
		child := messages[i+1]
		c := child.Content
		if c.MsgType != want.msgType || c.FileName != want.name || string(c.URL) != want.url || c.Info == nil || c.Info.MimeType != want.mime || c.Info.Size != want.size {
			t.Errorf("bad native media %d: %+v", i, c)
		}
		if child.Sender != messages[0].Sender || c.RelatesTo.GetReplyTo() != messages[0].ID || !strings.Contains(c.Body, want.label) {
			t.Errorf("lost sender, root relation or historical label: %+v", child)
		}
		if ref, err := h.server.getMatrixMessageReference("wxid_self", h.portal, child.ID); err != nil || ref.EventID != "" {
			t.Errorf("child incorrectly mapped as independent WeChat quote: %+v %v", ref, err)
		}
	}
	after, _ := json.Marshal(m)
	if string(before) != string(after) {
		t.Fatal("render mutated inbound message")
	}
	if got := h.server.getMessageEvent(m.Chat, m.ID); got != messages[0].ID {
		t.Fatalf("bundle alias mapping missing: %s", got)
	}
	if got := h.server.getMessageReference("wxid_self", h.portal, "777", "new:777"); got.EventID != "" {
		t.Fatal("historical fromnewmsgid was mapped as live message")
	}
	// Lose all reference caches, then resolve reply and cascade recall from disk.
	h.server.mu.Lock()
	h.server.references = nil
	h.server.mu.Unlock()
	h.server.OnMessage(t.Context(), &remoteapi.InboundMessage{ID: "reply", Chat: m.Chat, Sender: m.Sender, Text: "reply", Reply: &remoteapi.Reply{Target: m.ID}})
	h.server.OnRedaction(t.Context(), &remoteapi.Redaction{Chat: m.Chat, Target: m.ID, By: m.Sender, Text: "撤回记录"})
	messages, _, redactions := h.snapshot()
	if len(messages) != 6 || messages[4].Content.RelatesTo.GetReplyTo() != messages[0].ID || len(redactions) != 4 {
		t.Fatalf("reply/recall mapping broken: messages=%+v redactions=%v", messages, redactions)
	}
	for i, target := range redactions {
		if target != messages[i].ID {
			t.Errorf("recall missed original event: %v", redactions)
		}
	}
}

func TestRecordLongTranscriptBudgetAndUploadFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			h := newRecordTestMatrix(t, map[string]bool{"forwarded-record.txt": fail})
			text := strings.Repeat("<汉字>\"\x00&", 15000) + "FINAL RECORD CONTENT"
			m := &remoteapi.InboundMessage{ID: "long", Chat: h.portal.ChatID, Sender: "wxid_self", Type: remoteapi.MsgApp,
				Record: &remoteapi.ForwardedRecord{Title: "long", Items: []remoteapi.RecordItem{{Text: text}, {Media: &remoteapi.Media{FileName: "last.txt", Data: []byte("last")}}, {MediaStatus: "附件下载失败"}}},
				Reply:  &remoteapi.Reply{SenderName: "missing target", Text: "quoted text"},
			}
			converted := matrixContentFromRemote(m)
			if recordContentTooLarge(converted) {
				t.Fatal("converter produced oversized preview")
			}
			h.server.OnMessage(t.Context(), m)
			messages, uploads, _ := h.snapshot()
			wantEvents := 3
			if fail {
				wantEvents = 2
			}
			if len(messages) != wantEvents || len(uploads) != 2 {
				t.Fatalf("wrong event/upload count: %d %d", len(messages), len(uploads))
			}
			content := messages[0].Content
			if recordContentTooLarge(&content) || !utf8.ValidString(content.Body) || !strings.Contains(content.Body, "预览") || !strings.Contains(content.Body, "附件下载不可用：1") {
				t.Fatal("invalid or oversized preview, or failure count missing")
			}
			transcript := uploads[1]
			if transcript.name != "forwarded-record.txt" || transcript.mime != "text/plain; charset=utf-8" || !utf8.ValidString(transcript.body) || !strings.Contains(transcript.body, text) || !strings.Contains(transcript.body, "last.txt") || !strings.Contains(transcript.body, "quoted text") {
				t.Fatal("full UTF-8 transcript lost content, quote or attachment label")
			}
			if strings.Contains(content.Body+content.FormattedBody, "mxc://") {
				t.Fatal("long transcript relies on raw MXC anchor")
			}
			if fail {
				if !strings.Contains(content.Body, "完整记录附件上传失败") || strings.Contains(content.Body, "secret-token") {
					t.Fatal("missing safe transcript failure")
				}
			} else {
				child := messages[2].Content
				if child.MsgType != event.MsgFile || child.URL != "mxc://example.com/upload2" || child.FileName != "forwarded-record.txt" || child.Info == nil || child.Info.Size != len(transcript.body) || child.RelatesTo.GetReplyTo() != messages[0].ID {
					t.Fatalf("full transcript not a native reply file: %+v", child)
				}
			}
		})
	}
}

func TestRecordNativeSendFailureDoesNotAbandonLaterAttachments(t *testing.T) {
	h := newRecordTestMatrix(t, nil)
	h.mu.Lock()
	h.failedSend = map[string]bool{"failed.mp4": true}
	h.mu.Unlock()
	m := &remoteapi.InboundMessage{ID: "send-failure", Chat: h.portal.ChatID, Sender: "wxid_self", Record: &remoteapi.ForwardedRecord{Items: []remoteapi.RecordItem{
		{Media: &remoteapi.Media{FileName: "failed.mp4", MimeType: "video/mp4", Data: []byte("video")}},
		{Media: &remoteapi.Media{FileName: "last.txt", Data: []byte("last")}},
	}}}
	h.server.OnMessage(t.Context(), m)
	messages, uploads, _ := h.snapshot()
	if len(messages) != 3 || len(uploads) != 2 || messages[1].Content.MsgType != event.MsgNotice || !strings.Contains(messages[1].Content.Body, "发送未确认") || !strings.Contains(messages[1].Content.Body, "failed.mp4") || messages[2].Content.FileName != "last.txt" {
		t.Fatalf("failure notice/continuation missing: %+v", messages)
	}
	for _, child := range messages[1:] {
		if child.Content.RelatesTo.GetReplyTo() != messages[0].ID || strings.Contains(child.Content.Body, "secret-token") {
			t.Fatalf("unsafe/unrelated child: %+v", child)
		}
	}
	ref := h.server.getMessageReference("wxid_self", h.portal, m.ID, "")
	if len(ref.RelatedEventIDs) != 2 || ref.RelatedEventIDs[0] != messages[1].ID || ref.RelatedEventIDs[1] != messages[2].ID {
		t.Fatalf("notice and later file not durably recallable: %+v", ref)
	}
}

func TestRecordNativeChildrenReuseUploadAndDeterministicTransactions(t *testing.T) {
	h := newRecordTestMatrix(t, nil)
	asset := &remoteapi.Media{FileName: "same.txt", Data: []byte("same")}
	m := &remoteapi.InboundMessage{ID: "dedup", Chat: h.portal.ChatID, Sender: "wxid_self", Record: &remoteapi.ForwardedRecord{Items: []remoteapi.RecordItem{{Media: asset}, {Media: asset}}}}
	_, children, _ := h.server.uploadRecordMedia(t.Context(), m.Record)
	if len(children) != 2 || children[0].content.URL != children[1].content.URL {
		t.Fatal("asset upload not reused")
	}
	for i := 0; i < 2; i++ {
		h.server.sendRecordChildren(t.Context(), "wxid_self", h.portal, m, h.server.as.BotIntent().Client, "$root", children)
	}
	messages, uploads, _ := h.snapshot()
	if len(messages) != 2 || len(uploads) != 1 || messages[0].TxnID == messages[1].TxnID {
		t.Fatalf("retry duplicated children or conflated items: %+v, %d uploads", messages, len(uploads))
	}
	ref := h.server.getMessageReference("wxid_self", h.portal, m.ID, "")
	if len(ref.RelatedEventIDs) != 2 {
		t.Fatalf("idempotent related IDs not preserved: %+v", ref)
	}
	base := recordChildTransactionID("wxid_self", h.portal, m, "$root", "item-1")
	otherRoom := h.portal
	otherRoom.RoomID = "!other:example.com"
	otherChat := h.portal
	otherChat.ChatID = "another"
	for _, tx := range []string{
		recordChildTransactionID("other-account", h.portal, m, "$root", "item-1"),
		recordChildTransactionID("wxid_self", otherRoom, m, "$root", "item-1"),
		recordChildTransactionID("wxid_self", otherChat, m, "$root", "item-1"),
		recordChildTransactionID("wxid_self", h.portal, m, "$other-root", "item-1"),
		recordChildTransactionID("wxid_self", h.portal, m, "$root", "item-2"),
	} {
		if base == tx {
			t.Fatal("transaction scope collision")
		}
	}
}

func TestRecordRootSendFailureDoesNotSendChildren(t *testing.T) {
	h := newRecordTestMatrix(t, nil)
	h.mu.Lock()
	h.failedRoot = true
	h.mu.Unlock()
	m := &remoteapi.InboundMessage{ID: "root-failure", Chat: h.portal.ChatID, Sender: "wxid_self", Record: &remoteapi.ForwardedRecord{Items: []remoteapi.RecordItem{{Media: &remoteapi.Media{FileName: "file.txt", Data: []byte("data")}}}}}
	h.server.OnMessage(t.Context(), m)
	messages, _, _ := h.snapshot()
	if len(messages) != 1 || messages[0].Content.MsgType != event.MsgNotice || strings.Contains(messages[0].Content.Body, "secret-token") {
		t.Fatalf("root failure not reported safely: %+v", messages)
	}
	if ref := h.server.getMessageReference("wxid_self", h.portal, m.ID, ""); ref.EventID != "" {
		t.Fatal("unconfirmed root was mapped")
	}
}

func TestRecordAccountProvenanceRejectsBeforeUpload(t *testing.T) {
	h := newRecordTestMatrix(t, nil)
	h.server.bridgeRemoteToPortal(t.Context(), h.portal, &remoteapi.InboundMessage{Account: "other-account", ID: "foreign", Chat: h.portal.ChatID, Record: &remoteapi.ForwardedRecord{Items: []remoteapi.RecordItem{{Media: &remoteapi.Media{Data: []byte("do not upload")}}}}})
	messages, uploads, _ := h.snapshot()
	if len(messages) != 0 || len(uploads) != 0 {
		t.Fatal("foreign account record leaked")
	}
}

func TestRecordPreviewUsesSerializedJSONBudget(t *testing.T) {
	for _, text := range []string{strings.Repeat("<", 60000), strings.Repeat("&", 60000), strings.Repeat("\x00", 60000), strings.Repeat("多字节", 30000)} {
		content := matrixContentFromRemote(&remoteapi.InboundMessage{Record: &remoteapi.ForwardedRecord{Items: []remoteapi.RecordItem{{Text: text}}}})
		if recordContentTooLarge(content) || !utf8.ValidString(content.Body) {
			t.Fatal("escaped JSON exceeds budget or invalid UTF-8 preview")
		}
	}
}

func TestRecordSVGIsNativeFileNotInlineImage(t *testing.T) {
	h := newRecordTestMatrix(t, nil)
	h.server.OnMessage(t.Context(), &remoteapi.InboundMessage{ID: "svg", Chat: h.portal.ChatID, Sender: "wxid_self", Record: &remoteapi.ForwardedRecord{Items: []remoteapi.RecordItem{
		{Media: &remoteapi.Media{FileName: `<img src=x onerror=alert(1)>.svg`, MimeType: "image/png", Data: []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)}},
	}}})
	messages, uploads, _ := h.snapshot()
	if len(messages) != 2 || len(uploads) != 1 || messages[1].Content.MsgType != event.MsgFile || messages[1].Content.URL == "" {
		t.Fatalf("SVG must remain a native file: %+v", messages)
	}
	content := messages[0].Content
	if strings.Contains(content.FormattedBody, "<img ") || strings.Contains(content.FormattedBody, "<script") || !strings.Contains(content.FormattedBody, "&lt;img") {
		t.Fatalf("unsafe or unescaped summary: %s", content.FormattedBody)
	}
}

func TestRecordNativeChildrenUseActualRootClientAndBoundedLabels(t *testing.T) {
	h := newRecordTestMatrix(t, nil)
	client := h.server.as.Intent("@wechat_actual_sender:example.com").Client
	m := &remoteapi.InboundMessage{ID: "puppet-record", Chat: h.portal.ChatID, Sender: "actual_sender", Record: &remoteapi.ForwardedRecord{Items: []remoteapi.RecordItem{{
		SenderName: strings.Repeat("<untrusted historical author>", 2000), Time: "1900-01-01", Media: &remoteapi.Media{FileName: strings.Repeat("多字节", 3000) + ".txt", Data: []byte("data")},
	}}}}
	h.server.bridgeRecordToPortal(t.Context(), "wxid_self", h.portal, m, client)
	messages, _, _ := h.snapshot()
	if len(messages) != 3 {
		t.Fatalf("expected root, native attachment, and full transcript: %+v", messages)
	}
	for i, message := range messages {
		if message.Sender != client.UserID || recordContentTooLarge(&message.Content) || !utf8.ValidString(message.Content.Body) || !utf8.ValidString(message.Content.FileName) {
			t.Fatalf("incorrect sender or invalid/oversized native content %d: %+v", i, message)
		}
		if i > 0 && message.Content.RelatesTo.GetReplyTo() != messages[0].ID {
			t.Fatalf("missing actual root relation: %+v", message)
		}
	}
}

func TestRecordTranscriptSendFailureIsVisible(t *testing.T) {
	h := newRecordTestMatrix(t, nil)
	h.mu.Lock()
	h.failedSend = map[string]bool{"forwarded-record.txt": true}
	h.mu.Unlock()
	m := &remoteapi.InboundMessage{ID: "transcript-send-failure", Chat: h.portal.ChatID, Sender: "wxid_self", Record: &remoteapi.ForwardedRecord{Items: []remoteapi.RecordItem{{Text: strings.Repeat("long", 20000)}}}}
	h.server.OnMessage(t.Context(), m)
	messages, uploads, _ := h.snapshot()
	if len(messages) != 2 || len(uploads) != 1 || messages[1].Content.MsgType != event.MsgNotice || !strings.Contains(messages[1].Content.Body, "forwarded-record.txt") || !strings.Contains(messages[1].Content.Body, "发送未确认") || messages[1].Content.RelatesTo.GetReplyTo() != messages[0].ID {
		t.Fatalf("full transcript send failure not visible: %+v", messages)
	}
}

func TestRecordCycleDoesNotRecurseForever(t *testing.T) {
	r := &remoteapi.ForwardedRecord{Title: "cycle"}
	r.Items = []remoteapi.RecordItem{{Record: r}}
	content := matrixContentFromRemote(&remoteapi.InboundMessage{Record: r})
	if !strings.Contains(content.Body, "循环引用") {
		t.Fatal("missing cycle marker")
	}
}
