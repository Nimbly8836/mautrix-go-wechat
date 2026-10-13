package matrixas

import (
	"path/filepath"
	"testing"
)

func TestGroupNativeReplyAfterReferenceCacheLoss(t *testing.T) {
	b := newMessageRelationBridge(t)
	b.server.portals.path = filepath.Join(t.TempDir(), "portals.json")
	b.callback(t, 123, 9007199254740993, 1, "有没有修改备注的功能", "有没有修改备注的功能")
	// Discard every in-memory reference index, as on process restart.
	b.server.mu.Lock()
	b.server.references = nil
	b.server.mu.Unlock()
	b.callback(t, 124, 9007199254740995, 49, `<msg><appmsg><title>引用测试</title><type>57</type><refermsg><type>1</type><svrid>9007199254740993</svrid><fromusr>group@chatroom</fromusr><chatusr>wxid_self</chatusr><displayname>Hayek</displayname><content>有没有修改备注的功能</content></refermsg></appmsg></msg>`, "[引用消息]")
	messages, _ := b.snapshot()
	if len(messages) != 2 {
		t.Fatalf("messages=%d", len(messages))
	}
	if messages[1].Content.RelatesTo == nil || messages[1].Content.RelatesTo.GetReplyTo() != messages[0].ID {
		t.Fatalf("original event exists but native relation lost: %+v", messages[1].Content)
	}
}
