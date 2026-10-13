package matrixas

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"maunium.net/go/mautrix/id"
)

func TestRecordRelatedReferencesSurviveRestartAndRecall(t *testing.T) {
	b := newMessageRelationBridge(t)
	b.server.portals.path = filepath.Join(t.TempDir(), "portals.json")
	b.callback(t, 123, 456, 1, "合并记录摘要", "合并记录摘要")
	messages, _ := b.snapshot()
	rec, _ := b.server.portals.get("group@chatroom")
	m := &remoteapi.InboundMessage{Account: "wxid_self", Chat: rec.ChatID, Sender: "wxid_self", ID: "456", Type: remoteapi.MsgApp, Text: "合并记录摘要", DeduplicationKeys: []string{"new:456", "msg:123"}}
	client := b.server.as.BotIntent().Client
	b.server.rememberMessageReference("wxid_self", rec, m, messages[0].ID, client, "$video")
	b.server.rememberMessageReference("wxid_self", rec, m, messages[0].ID, client, "$video", "$image")
	// A repeated root-only write cannot lose confirmed children.
	b.server.rememberMessageReference("wxid_self", rec, m, messages[0].ID, client)
	b.server.references = nil
	ref := b.server.getMessageReference("wxid_self", rec, "456", "new:456")
	if !reflect.DeepEqual(ref.RelatedEventIDs, []id.EventID{"$video", "$image"}) {
		t.Fatalf("lost attachments: %+v", ref)
	}
	child, err := b.server.getMatrixMessageReference("wxid_self", rec, "$video")
	if err != nil || child.EventID != "" {
		t.Fatalf("attachment became forged quote source: %+v %v", child, err)
	}
	b.callback(t, 125, 789, 10002, relationXML(10002, "456", "group@chatroom"), "[撤回]")
	_, redacted := b.snapshot()
	if !reflect.DeepEqual(redacted, []id.EventID{messages[0].ID, "$video", "$image"}) {
		t.Fatalf("incomplete recall: %v", redacted)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.redactionTxns) != 3 || b.redactionTxns[0] == b.redactionTxns[1] || b.redactionTxns[1] == b.redactionTxns[2] {
		t.Fatalf("child transaction collisions: %v", b.redactionTxns)
	}
}

func TestRecordRelatedRecallFailureStillAttemptsAllParts(t *testing.T) {
	b := newMessageRelationBridge(t)
	b.callback(t, 123, 456, 1, "摘要", "摘要")
	messages, _ := b.snapshot()
	rec, _ := b.server.portals.get("group@chatroom")
	m := &remoteapi.InboundMessage{Chat: rec.ChatID, Sender: "wxid_self", ID: "456", DeduplicationKeys: []string{"new:456"}}
	b.server.rememberMessageReference("wxid_self", rec, m, messages[0].ID, b.server.as.BotIntent().Client, "$video")
	b.redactionStatus = 403
	b.callback(t, 125, 789, 10002, relationXML(10002, "456", "group@chatroom"), "[撤回]")
	messages, redacted := b.snapshot()
	if len(redacted) != 2 || !strings.Contains(messages[len(messages)-1].Content.Body, "撤回未确认") {
		t.Fatalf("partial failure hidden: %v %+v", redacted, messages)
	}
}
