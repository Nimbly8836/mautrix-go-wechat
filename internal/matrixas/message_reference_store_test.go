package matrixas

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"maunium.net/go/mautrix/id"
)

func TestMessageReferenceDurableScopesAndTypedCollision(t *testing.T) {
	s, rec := newPortalMessageTestServer(t, &portalMessageAPI{})
	s.portals.path = filepath.Join(t.TempDir(), "portals.json")
	original := &remoteapi.InboundMessage{Account: "wxid_self", Chat: rec.ChatID, Sender: "wxid_author", ID: "9007199254740993", Type: remoteapi.MsgText, Text: strings.Repeat("文本", 1500), DeduplicationKeys: []string{"new:9007199254740993", "msg:123"}}
	s.rememberMessageReference("wxid_self", rec, original, "$native", s.as.BotIntent().Client)
	// The same digits in a short namespace must never authorize a native quote.
	short := *original
	short.ID, short.DeduplicationKeys = "9007199254740993", []string{"msg:9007199254740993"}
	s.rememberMessageReference("wxid_self", rec, &short, "$short", s.as.BotIntent().Client)
	s.references = nil
	ref := s.getMessageReference("wxid_self", rec, original.ID, "new:"+string(original.ID))
	if ref.EventID != "$native" || ref.Sender != original.Sender || ref.Client == nil || ref.Client.UserID != s.as.BotMXID() || len(ref.Text) > referenceTextLimit || !utf8.ValidString(ref.Text) {
		t.Fatalf("lost durable reference: %+v", ref)
	}
	reverse, err := s.getMatrixMessageReference("wxid_self", rec, "$native")
	if err != nil || reverse.NewID != original.ID {
		t.Fatalf("reverse=%+v err=%v", reverse, err)
	}
	shortRef, _ := s.getMatrixMessageReference("wxid_self", rec, "$short")
	if shortRef.NewID != "" {
		t.Fatalf("short alias promoted: %+v", shortRef)
	}
	for _, tc := range []struct {
		account remoteapi.UserID
		chat    remoteapi.ChatID
		room    id.RoomID
	}{{"other", rec.ChatID, rec.RoomID}, {"wxid_self", "other", rec.RoomID}, {"wxid_self", rec.ChatID, "!other:example.com"}} {
		scoped := rec
		scoped.ChatID, scoped.RoomID = tc.chat, tc.room
		if got := s.getMessageReference(tc.account, scoped, original.ID, "new:"+string(original.ID)); got.EventID != "" {
			t.Fatalf("cross-scope forward reference: %+v", got)
		}
		if got, err := s.getMatrixMessageReference(tc.account, scoped, "$native"); err != nil || got.EventID != "" {
			t.Fatalf("cross-scope reverse reference: %+v %v", got, err)
		}
	}
	info, err := os.Stat(s.portals.path + ".messages.jsonl")
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("mode=%v err=%v", info, err)
	}
}

func TestMessageReferencesNeverEvictAllAt5000(t *testing.T) {
	s, rec := newPortalMessageTestServer(t, &portalMessageAPI{})
	for i := 1; i <= 5100; i++ {
		key := fmt.Sprintf("new:%d", i)
		ref := messageReference{EventID: id.EventID(fmt.Sprintf("$%d", i)), Sender: "wxid_author", MatrixSender: "@author:example.com"}
		if err := s.saveMessageReference("wxid_self", rec, []string{key}, ref); err != nil {
			t.Fatal(err)
		}
	}
	if got := s.getMessageReference("wxid_self", rec, "1", "new:1"); got.EventID != "$1" {
		t.Fatalf("oldest reference evicted: %+v", got)
	}
	if got, _ := s.getMatrixMessageReference("wxid_self", rec, "$1"); got.NewID != "1" {
		t.Fatalf("oldest reverse reference evicted: %+v", got)
	}
}

func TestMessageReferenceJournalRecoversOnlyIncompleteTail(t *testing.T) {
	s, rec := newPortalMessageTestServer(t, &portalMessageAPI{})
	s.portals.path = filepath.Join(t.TempDir(), "portals.json")
	save := func(eventID id.EventID, key string) error {
		return s.saveMessageReference("wxid_self", rec, []string{key}, messageReference{EventID: eventID, Sender: "wxid_author", MatrixSender: "@author:example.com"})
	}
	if err := save("$one", "new:1"); err != nil {
		t.Fatal(err)
	}
	path := s.portals.path + ".messages.jsonl"
	committed, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, append(append([]byte(nil), committed...), []byte(`{"version":1,"account":"cut`)...), 0600); err != nil {
		t.Fatal(err)
	}
	s.references = nil
	if err = save("$two", "new:2"); err != nil {
		t.Fatal(err)
	}
	s.references = nil
	for _, n := range []string{"1", "2"} {
		if got := s.getMessageReference("wxid_self", rec, remoteapi.MessageID(n), "new:"+n); got.EventID != id.EventID(map[string]string{"1": "$one", "2": "$two"}[n]) {
			t.Fatalf("lost record %s: %+v", n, got)
		}
	}
	data, _ := os.ReadFile(path)
	if strings.Count(string(data), "\n") != 2 || !strings.HasPrefix(string(data), string(committed)) {
		t.Fatalf("not an append-only recovery: %s", data)
	}
	// Unlike a partial tail, a corrupt complete record must not be discarded.
	if err = os.WriteFile(path, append(data, []byte("{bad}\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	s.references = nil
	if err = save("$three", "new:3"); err == nil {
		t.Fatal("corrupt journal silently accepted")
	}
	after, _ := os.ReadFile(path)
	if len(after) != len(data)+6 {
		t.Fatal("corrupt journal modified")
	}
}

func TestMessageReferenceJournalRejectsInvalidAndSymlink(t *testing.T) {
	for _, payload := range []string{"{}\n", `{"version":99}` + "\n", strings.Repeat("x", 128*1024) + "\n"} {
		path := filepath.Join(t.TempDir(), "journal")
		if err := os.WriteFile(path, []byte(payload), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := openMessageReferenceStore(path); err == nil {
			t.Fatal("invalid record accepted")
		}
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	path := filepath.Join(dir, "link")
	if err := os.WriteFile(target, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := openMessageReferenceStore(path); err == nil {
		t.Fatal("journal followed symlink")
	}
}

func TestMessageReferenceJournalWriteFailureIsReturnedAndLatched(t *testing.T) {
	s, rec := newPortalMessageTestServer(t, &portalMessageAPI{})
	s.portals.path = filepath.Join(t.TempDir(), "portals.json")
	store, err := s.referenceStore()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(store.path); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(store.path, 0700); err != nil {
		t.Fatal(err)
	}
	ref := messageReference{EventID: "$failed", MatrixSender: "@a:example.com", Sender: "wxid_author"}
	if err = s.saveMessageReference("wxid_self", rec, []string{"new:1"}, ref); err == nil {
		t.Fatal("write failure swallowed")
	}
	if got, _ := s.getMatrixMessageReference("wxid_self", rec, "$failed"); got.EventID != "" {
		t.Fatal("failed write published in index")
	}
	if err = os.Remove(store.path); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(store.path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err = s.saveMessageReference("wxid_self", rec, []string{"new:1"}, ref); err == nil {
		t.Fatal("writes resumed after uncertain failure without reload")
	}
}

func TestMessageReferenceAliasLearningDoesNotDowngradeStrongID(t *testing.T) {
	s, rec := newPortalMessageTestServer(t, &portalMessageAPI{})
	s.portals.path = filepath.Join(t.TempDir(), "portals.json")
	ref := messageReference{EventID: "$one", Sender: "wxid_author", MatrixSender: "@a:example.com", Text: "original"}
	if err := s.saveMessageReference("wxid_self", rec, []string{"new:1"}, ref); err != nil {
		t.Fatal(err)
	}
	ref.Text = "partial echo"
	if err := s.saveMessageReference("wxid_self", rec, []string{"msg:2"}, ref); err != nil {
		t.Fatal(err)
	}
	s.references = nil
	got, err := s.getMatrixMessageReference("wxid_self", rec, "$one")
	if err != nil || got.NewID != "1" || got.Text != "original" {
		t.Fatalf("downgraded original: %+v %v", got, err)
	}
	ref.EventID = "$conflict"
	if err = s.saveMessageReference("wxid_self", rec, []string{"new:1"}, ref); err == nil {
		t.Fatal("strong collision replaced original")
	}
	if got = s.getMessageReference("wxid_self", rec, "1", "new:1"); got.EventID != "$one" {
		t.Fatalf("strong collision changed mapping: %+v", got)
	}
}

func TestRecallRestoresOriginalPuppetAfterRestart(t *testing.T) {
	b := newMessageRelationBridge(t)
	b.server.portals.path = filepath.Join(t.TempDir(), "portals.json")
	b.callbackPayload(t, map[string]any{"MsgID": 123, "NewMsgID": 456, "MsgType": 1, "Content": "原消息", "Talker": "group@chatroom", "SenderUserName": "wxid_alice", "IsGroup": true, "CreateTime": 1710000000})
	original, _ := b.snapshot()
	b.server.references = nil
	b.callback(t, 125, 789, 10002, relationXML(10002, "456", "group@chatroom"), "[撤回]")
	_, redacted := b.snapshot()
	if len(redacted) != 1 || redacted[0] != original[0].ID {
		t.Fatalf("redacted=%v", redacted)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.redactionSenders) != 1 || b.redactionSenders[0] != original[0].Sender {
		t.Fatalf("lost original sender: %v want %s", b.redactionSenders, original[0].Sender)
	}
}
