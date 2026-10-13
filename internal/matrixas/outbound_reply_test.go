package matrixas

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"github.com/leat/mautrix-go-wechat/internal/wechat"
	"maunium.net/go/mautrix/event"
)

type nativeReplyRequest struct {
	Path    string
	Wxid    string
	ToWxid  string
	Type    int
	Xml     string
	Content string
}
type nativeReplyHarness struct {
	s        *Server
	rec      portalRecord
	requests []nativeReplyRequest
	notices  []string
	fail     bool
	short    bool
	fetched  *event.Event
	fetches  int
}

func newNativeReplyHarness(t *testing.T) *nativeReplyHarness {
	t.Helper()
	s, rec := newPortalMessageTestServer(t, &portalMessageAPI{})
	rec.ChatID, rec.IsGroup = "group@chatroom", true
	s.portals.data.Portals = map[string]portalRecord{string(rec.ChatID): rec}
	s.portals.path = filepath.Join(t.TempDir(), "portals.json")
	h := &nativeReplyHarness{s: s, rec: rec}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := nativeReplyRequest{Path: r.URL.Path}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		h.requests = append(h.requests, request)
		w.Header().Set("Content-Type", "application/json")
		if h.fail {
			_, _ = w.Write([]byte(`{"Code":-13,"Success":false,"Message":"用户可能退出"}`))
			return
		}
		newID := int64(9007199254741993) + int64(len(h.requests)-1)
		if h.short {
			newID = 0
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Code": 0, "Success": true, "Data": map[string]any{"NewMsgId": newID, "MsgId": 321}})
	}))
	t.Cleanup(upstream.Close)
	api, err := (wechat.Factory{}).New(remoteapi.Config{BaseURL: upstream.URL, ReceiveMode: "push", Session: []byte(`{"wxid":"wxid_self"}`)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(api.Disconnect)
	s.api = api
	matrix := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/event/") {
			h.fetches++
			if h.fetched == nil {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"errcode":"M_NOT_FOUND"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(h.fetched)
			return
		}
		if strings.Contains(r.URL.Path, "/send/") {
			var content event.MessageEventContent
			if err := json.NewDecoder(r.Body).Decode(&content); err != nil {
				t.Error(err)
			}
			h.notices = append(h.notices, content.Body)
			_, _ = w.Write([]byte(`{"event_id":"$notice"}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/register") || strings.HasSuffix(r.URL.Path, "/join") {
			_ = json.NewEncoder(w).Encode(map[string]any{"user_id": s.as.BotMXID(), "room_id": rec.RoomID})
			return
		}
		if strings.Contains(r.URL.Path, "/download/") {
			_, _ = w.Write([]byte("image data"))
			return
		}
		t.Errorf("unexpected Matrix request %s", r.URL.Path)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(matrix.Close)
	if err = s.as.SetHomeserverURL(matrix.URL); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *nativeReplyHarness) seed(t *testing.T, account remoteapi.UserID, rec portalRecord, keys ...string) {
	t.Helper()
	if err := h.s.saveMessageReference(account, rec, keys, messageReference{EventID: "$original", MatrixSender: h.s.as.BotMXID(), BridgeSender: true, Sender: "wxid_author", SenderName: "Alice & <friend>", Type: remoteapi.MsgText, Text: "original <title> & ]]> 文本"}); err != nil {
		t.Fatal(err)
	}
}

func (h *nativeReplyHarness) reply(body string) *event.Event {
	evt := portalTextEvent(h.rec, "> <@author:example.com> untrusted FALLBACK\n\n"+body)
	evt.ID = "$reply"
	msg := evt.Content.AsMessage()
	msg.RelatesTo = (&event.RelatesTo{}).SetReplyTo("$original")
	msg.Format = event.FormatHTML
	msg.FormattedBody = "<mx-reply><blockquote>untrusted FALLBACK</blockquote></mx-reply>" + body
	return evt
}

type parsedNativeQuote struct {
	Title string `xml:"appmsg>title"`
	Type  int    `xml:"appmsg>type"`
	Ref   struct {
		Type    int    `xml:"type"`
		ID      string `xml:"svrid"`
		From    string `xml:"fromusr"`
		Member  string `xml:"chatusr"`
		Name    string `xml:"displayname"`
		Content string `xml:"content"`
	} `xml:"appmsg>refermsg"`
}

func TestMatrixReplySendsNativeWechatQuoteAfterRestart(t *testing.T) {
	h := newNativeReplyHarness(t)
	h.seed(t, "wxid_self", h.rec, "new:9007199254740993", "msg:123")
	h.s.references = nil
	evt := h.reply("reply <x> & ]]> :wechat_smile:")
	h.s.handleMatrixEvent(context.Background(), evt)
	if len(h.requests) != 1 || h.requests[0].Path != "/Msg/SendApp" || h.requests[0].Type != 57 {
		t.Fatalf("requests=%+v notices=%v", h.requests, h.notices)
	}
	request := h.requests[0]
	if request.Wxid != "wxid_self" || request.ToWxid != string(h.rec.ChatID) || strings.Contains(request.Xml, "FALLBACK") {
		t.Fatalf("wrong scoped request: %+v", request)
	}
	var parsed parsedNativeQuote
	if err := xml.Unmarshal([]byte(request.Xml), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Type != 57 || parsed.Title != "reply <x> & ]]> [微笑]" || parsed.Ref.Type != 1 || parsed.Ref.ID != "9007199254740993" || parsed.Ref.From != string(h.rec.ChatID) || parsed.Ref.Member != "wxid_author" || parsed.Ref.Name != "Alice & <friend>" || parsed.Ref.Content != "original <title> & ]]> 文本" {
		t.Fatalf("parsed=%+v", parsed)
	}
	if !strings.Contains(evt.Content.AsMessage().Body, "FALLBACK") {
		t.Fatal("mutated source Matrix event")
	}
	h.s.references = nil
	ref := h.s.getMessageReference("wxid_self", h.rec, "9007199254741993", "new:9007199254741993")
	if ref.EventID != "$reply" || ref.NewID != "9007199254741993" || ref.Sender != "wxid_self" || ref.MatrixSender != evt.Sender || ref.Client != nil {
		t.Fatalf("sent quote reference=%+v", ref)
	}
	// A subsequent incoming quote attaches to the exact outbound Matrix event.
	content := &event.MessageEventContent{Body: "incoming reply"}
	h.s.attachInboundReply("wxid_self", h.rec, &remoteapi.InboundMessage{Reply: &remoteapi.Reply{Target: "9007199254741993", TargetKey: "new:9007199254741993"}}, content)
	if content.RelatesTo.GetReplyTo() != "$reply" {
		t.Fatalf("outbound-to-inbound relation=%+v", content)
	}
}

func TestMatrixReplyScopeAndMissingMappingsNeverInventNativeID(t *testing.T) {
	for _, scope := range []string{"missing", "account", "chat", "room", "short"} {
		t.Run(scope, func(t *testing.T) {
			h := newNativeReplyHarness(t)
			account, rec, keys := remoteapi.UserID("wxid_self"), h.rec, []string{"new:9007199254740993"}
			switch scope {
			case "account":
				account = "other"
			case "chat":
				rec.ChatID = "other"
			case "room":
				rec.RoomID = "!other:example.com"
			case "short":
				keys = []string{"msg:9007199254740993"}
			}
			if scope != "missing" {
				h.seed(t, account, rec, keys...)
			}
			h.s.handleMatrixEvent(context.Background(), h.reply("new reply"))
			if len(h.requests) != 1 || h.requests[0].Path != "/Msg/SendTxt" || strings.Contains(h.requests[0].Content, "FALLBACK") || !strings.Contains(h.requests[0].Content, "不可用") {
				t.Fatalf("invented quote: %+v", h.requests)
			}
			if scope != "short" && strings.Contains(h.requests[0].Content, "original <title>") {
				t.Fatal("cross-scope snapshot leaked")
			}
		})
	}
}

func TestMatrixReplyReadableFetchValidatesRoomAndEvent(t *testing.T) {
	for _, scope := range []string{"valid", "room", "event"} {
		t.Run(scope, func(t *testing.T) {
			h := newNativeReplyHarness(t)
			target := portalTextEvent(h.rec, "real original body")
			target.ID = "$original"
			// Arbitrary user metadata cannot make this event a native quote source.
			target.Content.Raw = map[string]any{"body": "real original body", "msgtype": "m.text", "new_msg_id": "9007199254740993"}
			target.Content.Parsed = nil
			if scope == "room" {
				target.RoomID = "!wrong:example.com"
			}
			if scope == "event" {
				target.ID = "$wrong"
			}
			h.fetched = target
			h.s.handleMatrixEvent(context.Background(), h.reply("new reply"))
			if len(h.requests) != 1 || h.requests[0].Path != "/Msg/SendTxt" {
				t.Fatalf("requests=%+v", h.requests)
			}
			if strings.Contains(h.requests[0].Content, "real original body") != (scope == "valid") {
				t.Fatalf("unverified fallback=%s", h.requests[0].Content)
			}
		})
	}
}

func TestMatrixReplyCommandEditThreadAndXMLHandling(t *testing.T) {
	for _, kind := range []string{"ignored", "command", "edit", "thread_fallback", "xml"} {
		t.Run(kind, func(t *testing.T) {
			h := newNativeReplyHarness(t)
			h.seed(t, "wxid_self", h.rec, "new:9007199254740993")
			body := "new reply"
			switch kind {
			case "ignored":
				body = "!ignore command"
				h.s.cfg.Bridge.IgnoredCommandPrefixes = []string{"!ignore"}
			case "command":
				body = "!wx info"
			case "xml":
				body = "```wx_xml\n<appmsg><title>explicit</title><type>5</type></appmsg>\n```"
			}
			evt := h.reply(body)
			if kind == "edit" {
				evt.Content.AsMessage().RelatesTo.Type = event.RelReplace
				evt.Content.AsMessage().RelatesTo.EventID = "$edit-target"
			}
			if kind == "thread_fallback" {
				evt.Content.AsMessage().RelatesTo.Type = event.RelThread
				evt.Content.AsMessage().RelatesTo.IsFallingBack = true
			}
			if kind == "xml" {
				evt.Content.AsMessage().FormattedBody = "<mx-reply><blockquote>OLD</blockquote></mx-reply><pre><code class=\"language-wx_xml\">&lt;appmsg&gt;&lt;title&gt;explicit&lt;/title&gt;&lt;type&gt;5&lt;/type&gt;&lt;/appmsg&gt;</code></pre>"
			}
			h.s.handleMatrixEvent(context.Background(), evt)
			switch kind {
			case "ignored", "command", "edit":
				if len(h.requests) != 0 {
					t.Fatalf("local event sent upstream: %+v", h.requests)
				}
			case "thread_fallback":
				if len(h.requests) != 1 || h.requests[0].Path != "/Msg/SendTxt" || h.requests[0].Content != "new reply" {
					t.Fatalf("thread fallback became reply: %+v", h.requests)
				}
			case "xml":
				if len(h.requests) != 1 || h.requests[0].Type != 5 || h.requests[0].Xml != "<appmsg><title>explicit</title><type>5</type></appmsg>" {
					t.Fatalf("wx_xml changed: %+v notices=%v", h.requests, h.notices)
				}
			}
		})
	}
}

func TestMatrixNativeReplyUpstreamFailureDoesNotRetryOrMap(t *testing.T) {
	h := newNativeReplyHarness(t)
	h.seed(t, "wxid_self", h.rec, "new:9007199254740993")
	h.fail = true
	h.s.handleMatrixEvent(context.Background(), h.reply("new reply"))
	if len(h.requests) != 1 || len(h.notices) != 1 || !strings.Contains(h.notices[0], "发送到 WeChat 失败") {
		t.Fatalf("requests=%+v notices=%v", h.requests, h.notices)
	}
	ref, err := h.s.getMatrixMessageReference("wxid_self", h.rec, "$reply")
	if err != nil || ref.EventID != "" {
		t.Fatalf("failure mapped: %+v %v", ref, err)
	}
}

func TestMatrixRegularTextAndMediaReferences(t *testing.T) {
	for _, kind := range []string{"text", "media", "short"} {
		t.Run(kind, func(t *testing.T) {
			h := newNativeReplyHarness(t)
			h.short = kind == "short"
			evt := portalTextEvent(h.rec, "outbound body")
			evt.ID = "$outbound"
			if kind == "media" {
				msg := evt.Content.AsMessage()
				msg.MsgType = event.MsgImage
				msg.URL = "mxc://example.com/image"
				msg.Info = &event.FileInfo{MimeType: "image/png"}
				h.s.handlePortalMediaMessage(context.Background(), h.rec, evt.Sender, msg, event.EventMessage, evt.ID)
			} else {
				h.s.handleMatrixEvent(context.Background(), evt)
			}
			h.s.references = nil
			ref, err := h.s.getMatrixMessageReference("wxid_self", h.rec, evt.ID)
			if err != nil || ref.EventID != evt.ID || ref.MatrixSender != evt.Sender {
				t.Fatalf("reference=%+v err=%v notices=%v", ref, err, h.notices)
			}
			if (ref.NewID == "") != (kind == "short") {
				t.Fatalf("strong ID provenance lost: %+v", ref)
			}
			if kind == "media" && ref.Type != remoteapi.MsgImage {
				t.Fatalf("media type lost: %+v", ref)
			}
			if kind != "short" && ref.NewID != remoteapi.MessageID(fmt.Sprint(int64(9007199254741993))) {
				t.Fatalf("large returned ID rounded: %+v", ref)
			}
		})
	}
}

func TestMatrixReplyJournalLoadFailureStopsSend(t *testing.T) {
	h := newNativeReplyHarness(t)
	h.s.referenceStoreErr = fmt.Errorf("corrupt journal")
	h.s.handleMatrixEvent(context.Background(), h.reply("new reply"))
	if len(h.requests) != 0 || len(h.notices) != 1 || !strings.Contains(h.notices[0], "corrupt journal") {
		t.Fatalf("requests=%v notices=%v", h.requests, h.notices)
	}
}
