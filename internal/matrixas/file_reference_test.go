package matrixas

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"maunium.net/go/mautrix/id"
)

func TestFileEditReferencesKeepBothSourceIDsAndVisibleRoot(t *testing.T) {
	s, rec := newPortalMessageTestServer(t, &portalMessageAPI{})
	s.portals.path = filepath.Join(t.TempDir(), "portals.json")
	client := s.as.BotIntent().Client
	caption := &remoteapi.InboundMessage{Account: "wxid_self", Chat: rec.ChatID, Sender: "wxid_author", ID: "100", Type: remoteapi.MsgText, Text: "guide.pdf", DeduplicationKeys: []string{"new:100", "msg:1"}}
	file := &remoteapi.InboundMessage{Account: "wxid_self", Chat: rec.ChatID, Sender: "wxid_author", ID: "200", Type: remoteapi.MsgFile, Text: "guide.pdf", DeduplicationKeys: []string{"new:200", "msg:2"}, Quote: &remoteapi.SentMessageQuote{Type: remoteapi.MsgApp, Text: `<appmsg><title>guide.pdf</title><type>6</type></appmsg>`}}
	s.rememberMessageReference("wxid_self", rec, caption, "$root", client, "$progress")
	s.rememberMessageReference("wxid_self", rec, file, "$progress", client)
	s.rememberMessageReference("wxid_self", rec, caption, "$root", client, "$progress", "$complete")
	s.references = nil
	for _, target := range []remoteapi.MessageID{"100", "200"} {
		ref := s.getMessageReference("wxid_self", rec, target, "new:"+string(target))
		if ref.EventID != "$root" || !reflect.DeepEqual(ref.RelatedEventIDs, []id.EventID{"$progress", "$complete"}) {
			t.Errorf("target=%s resolved to %+v", target, ref)
		}
	}
	for _, eventID := range []id.EventID{"$root", "$progress"} {
		ref, err := s.getMatrixMessageReference("wxid_self", rec, eventID)
		if err != nil || ref.EventID != "$root" || ref.NewID != "200" || ref.Type != remoteapi.MsgFile || ref.QuoteText != file.Quote.Text {
			t.Errorf("reverse %s=%+v error=%v", eventID, ref, err)
		}
	}
}

func TestFileEditReferenceRejectsUntrustedParent(t *testing.T) {
	for _, variant := range []string{"other-sender", "other-client", "human-root", "record-root", "ambiguous-parent"} {
		t.Run(variant, func(t *testing.T) {
			s, rec := newPortalMessageTestServer(t, &portalMessageAPI{})
			root := messageReference{EventID: "$root", MatrixSender: s.as.BotMXID(), BridgeSender: true, Sender: "wxid_author", Type: remoteapi.MsgText, RelatedEventIDs: []id.EventID{"$edit"}}
			file := messageReference{EventID: "$edit", MatrixSender: s.as.BotMXID(), BridgeSender: true, Sender: "wxid_author", Type: remoteapi.MsgFile}
			switch variant {
			case "other-sender":
				file.Sender = "wxid_other"
			case "other-client":
				file.MatrixSender = "@other:example.com"
			case "human-root":
				root.BridgeSender = false
			case "record-root":
				root.Type = remoteapi.MsgApp
			}
			if err := s.saveMessageReference("wxid_self", rec, []string{"new:100"}, root); err != nil {
				t.Fatal(err)
			}
			if err := s.saveMessageReference("wxid_self", rec, []string{"new:200"}, file); err != nil {
				t.Fatal(err)
			}
			if variant == "ambiguous-parent" {
				second := root
				second.EventID = "$second"
				if err := s.saveMessageReference("wxid_self", rec, []string{"new:300"}, second); err != nil {
					t.Fatal(err)
				}
			}
			ref, err := s.getMatrixMessageReference("wxid_self", rec, "$root")
			if err != nil || ref.NewID != "100" {
				t.Fatalf("untrusted file became canonical: %+v %v", ref, err)
			}
			got := s.getMessageReference("wxid_self", rec, "200", "new:200")
			if got.EventID == "$root" || got.EventID == "$second" {
				t.Fatalf("file crossed identity or ambiguous parent: %+v", got)
			}
		})
	}
}
