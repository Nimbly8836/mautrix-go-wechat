package matrixas

import (
	"encoding/xml"
	"testing"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

func TestOutboundConfirmationBeatsEarlySelfEcho(t *testing.T) {
	h := newNativeReplyHarness(t)
	// Callback delivery won the race against the HTTP send response.
	echo := &remoteapi.InboundMessage{Account: "wxid_self", Chat: h.rec.ChatID, Sender: "wxid_self", ID: "9007199254741993", DeduplicationKeys: []string{"new:9007199254741993", "msg:321"}, Text: "hello"}
	h.s.rememberMessageReference("wxid_self", h.rec, echo, "$echo", h.s.as.BotIntent().Client)
	evt := portalTextEvent(h.rec, "hello")
	evt.ID = "$human-original"
	h.s.handleMatrixEvent(t.Context(), evt)
	h.s.references = nil
	ref, err := h.s.getMatrixMessageReference("wxid_self", h.rec, evt.ID)
	if err != nil || ref.NewID != "9007199254741993" {
		t.Fatalf("original human event lost to echo: %+v %v", ref, err)
	}
	forward := h.s.getMessageReference("wxid_self", h.rec, ref.NewID, "new:"+string(ref.NewID))
	if forward.EventID != evt.ID || forward.BridgeSender {
		t.Fatalf("echo still canonical: %+v", forward)
	}
}

func TestResponseFirstSelfEchoDoesNotDuplicateHumanEvent(t *testing.T) {
	h := newNativeReplyHarness(t)
	evt := portalTextEvent(h.rec, "hello")
	evt.ID = "$human-original"
	h.s.handleMatrixEvent(t.Context(), evt)
	echo := &remoteapi.InboundMessage{Account: "wxid_self", Chat: h.rec.ChatID, Sender: "wxid_self", ID: "9007199254741993", DeduplicationKeys: []string{"new:9007199254741993", "msg:321"}, Text: "hello"}
	h.s.OnMessage(t.Context(), echo)
	if len(h.notices) != 0 {
		t.Fatalf("self-echo was re-bridged: %v", h.notices)
	}
	ref := h.s.getMessageReference("wxid_self", h.rec, echo.ID, "new:"+string(echo.ID))
	if ref.EventID != evt.ID {
		t.Fatalf("echo replaced original: %+v", ref)
	}
	echo.Sender = "wxid_someone_else"
	if h.s.isConfirmedOutboundEcho("wxid_self", h.rec, echo) {
		t.Fatal("other sender treated as our echo")
	}
}

func TestReplyToNativeReplyUsesActualAppKindAndSafePreview(t *testing.T) {
	h := newNativeReplyHarness(t)
	h.seed(t, "wxid_self", h.rec, "new:9007199254740993")
	h.s.handleMatrixEvent(t.Context(), h.reply("first <reply>"))
	evt := h.reply("second reply")
	evt.ID = "$second"
	evt.Content.AsMessage().RelatesTo.SetReplyTo("$reply")
	h.s.handleMatrixEvent(t.Context(), evt)
	if len(h.requests) != 2 {
		t.Fatalf("requests=%+v notices=%v", h.requests, h.notices)
	}
	var quote parsedNativeQuote
	if err := xml.Unmarshal([]byte(h.requests[1].Xml), &quote); err != nil {
		t.Fatal(err)
	}
	var nested parsedNativeQuote
	if err := xml.Unmarshal([]byte(quote.Ref.Content), &nested); err != nil {
		t.Fatalf("quote content is not valid app XML: %q %v", quote.Ref.Content, err)
	}
	if quote.Ref.Type != 49 || nested.Type != 57 || nested.Title != "first <reply>" {
		t.Fatalf("wrong quote-to-quote wire content: %+v nested=%+v", quote, nested)
	}
}

func TestReplyToExplicitXMLUsesAppPreviewNotMarkdownFence(t *testing.T) {
	h := newNativeReplyHarness(t)
	evt := portalTextEvent(h.rec, "```wx_xml\n<appmsg><title>shared link</title><type>5</type><url>https://example.invalid/?token=SECRET</url></appmsg>\n```")
	evt.ID = "$xml-original"
	h.s.handleMatrixEvent(t.Context(), evt)
	reply := h.reply("reply to link")
	reply.Content.AsMessage().RelatesTo.SetReplyTo(evt.ID)
	h.s.handleMatrixEvent(t.Context(), reply)
	if len(h.requests) != 2 {
		t.Fatalf("requests=%+v notices=%v", h.requests, h.notices)
	}
	var quote, preview parsedNativeQuote
	if err := xml.Unmarshal([]byte(h.requests[1].Xml), &quote); err != nil {
		t.Fatal(err)
	}
	if err := xml.Unmarshal([]byte(quote.Ref.Content), &preview); err != nil {
		t.Fatalf("not a valid bounded preview: %q", quote.Ref.Content)
	}
	if quote.Ref.Type != 49 || preview.Type != 5 || preview.Title != "shared link" {
		t.Fatalf("wrong preview: %+v", quote)
	}
}
