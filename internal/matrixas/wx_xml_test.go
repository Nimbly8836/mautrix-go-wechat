package matrixas

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"maunium.net/go/mautrix/event"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"github.com/leat/mautrix-go-wechat/internal/wechat"
)

const portalXMLFixture = `<msg><appmsg><title><![CDATA[:wechat_smile: & <test>]]></title><type>5</type><url>https://example.com/?x=1&amp;y=2</url></appmsg></msg>`

func TestPortalXMLExtraction(t *testing.T) {
	formatted := `<pre><code class="language-wx_xml">` + html.EscapeString(portalXMLFixture) + `</code></pre>`
	tests := []struct {
		name      string
		body      string
		formatted string
		want      string
		found     bool
		wantErr   bool
	}{
		{name: "Fenced", body: "```wx_xml\n" + portalXMLFixture + "\n```", want: portalXMLFixture, found: true},
		{name: "LeadingProse", body: "explanation\n```wx_xml\n" + portalXMLFixture + "\n```", found: true, wantErr: true},
		{name: "LongerClosingFence", body: "```wx_xml\n" + portalXMLFixture + "\n````", want: portalXMLFixture, found: true},
		{name: "LongerClosingFenceWithHTML", body: "```wx_xml\n" + portalXMLFixture + "\n````", formatted: formatted, want: portalXMLFixture, found: true},
		{name: "FourBackticks", body: "````wx_xml\n" + portalXMLFixture + "\n````", want: portalXMLFixture, found: true},
		{name: "SpacedLanguage", body: "``` wx_xml\n" + portalXMLFixture + "\n```", want: portalXMLFixture, found: true},
		{name: "TildeFence", body: "~~~wx_xml\n" + portalXMLFixture + "\n~~~~", want: portalXMLFixture, found: true},
		{name: "IndentedFence", body: "\n  ```wx_xml\n" + portalXMLFixture + "\n  ```\n", want: portalXMLFixture, found: true},
		{name: "ShortClosingFence", body: "````wx_xml\n" + portalXMLFixture + "\n```", found: true, wantErr: true},
		{name: "DifferentClosingFence", body: "~~~wx_xml\n" + portalXMLFixture + "\n```", found: true, wantErr: true},
		{name: "OrdinaryFenceBeforeXML", body: "```text\nexample\n```\n```wx_xml\n" + portalXMLFixture + "\n```", found: true, wantErr: true},
		{name: "MarkerInsideExampleIsText", body: "````text\n```wx_xml\n" + portalXMLFixture + "\n```\n````"},
		{name: "IndentedExampleIsText", body: "    ```wx_xml\n    " + portalXMLFixture + "\n    ```"},
		{name: "BackticksInCDATA", body: "````wx_xml\n<appmsg><type>5</type><title><![CDATA[\n```\n]]></title></appmsg>\n````", want: "<appmsg><type>5</type><title><![CDATA[\n```\n]]></title></appmsg>", found: true},
		{name: "OuterWhitespace", body: " \n```wx_xml\n " + portalXMLFixture + " \n```\n ", want: " " + portalXMLFixture + " ", found: true},
		{name: "CRLF", body: "```wx_xml\r\n" + portalXMLFixture + "\r\n```", want: portalXMLFixture, found: true},
		{name: "HTML", body: portalXMLFixture, formatted: formatted, want: portalXMLFixture, found: true},
		{name: "HTMLEntitiesOnce", body: "fallback", formatted: `<pre><code class="language-wx_xml">&lt;appmsg&gt;&lt;type&gt;5&lt;/type&gt;&lt;title&gt;&amp;lt;&amp;amp;&lt;/title&gt;&lt;/appmsg&gt;</code></pre>`, want: `<appmsg><type>5</type><title>&lt;&amp;</title></appmsg>`, found: true},
		{name: "Highlighted", body: "fallback", formatted: `<pre><code class="hljs language-wx_xml"><span>&lt;appmsg&gt;</span>&lt;type&gt;5&lt;/type&gt;&lt;/appmsg&gt;</code></pre>`, want: `<appmsg><type>5</type></appmsg>`, found: true},
		{name: "RawXMLIsText", body: portalXMLFixture},
		{name: "OtherLanguage", body: "```xml\n" + portalXMLFixture + "\n```", formatted: strings.ReplaceAll(formatted, "language-wx_xml", "language-xml")},
		{name: "SimilarLanguage", body: "```wx_xml_example\n" + portalXMLFixture + "\n```"},
		{name: "InlineCodeIsText", body: "`wx_xml` " + portalXMLFixture},
		{name: "Empty", body: "```wx_xml\n```", found: true, wantErr: true},
		{name: "Unclosed", body: "```wx_xml\n" + portalXMLFixture, found: true, wantErr: true},
		{name: "TrailingProse", body: "```wx_xml\n" + portalXMLFixture + "\n```\nextra", found: true, wantErr: true},
		{name: "MultipleFences", body: "```wx_xml\n" + portalXMLFixture + "\n```\n```wx_xml\n" + portalXMLFixture + "\n```", found: true, wantErr: true},
		{name: "EmptyHTML", body: "fallback", formatted: `<pre><code class="language-wx_xml"> </code></pre>`, found: true, wantErr: true},
		{name: "MixedHTML", body: "fallback", formatted: "<p>extra</p>" + formatted, found: true, wantErr: true},
		{name: "MultipleHTML", body: "fallback", formatted: formatted + formatted, found: true, wantErr: true},
		{name: "InlineHTML", body: "fallback", formatted: `<code class="language-wx_xml">xml</code>`, found: true, wantErr: true},
		{name: "QuotedHTML", body: "fallback", formatted: "<blockquote>" + formatted + "</blockquote>", found: true, wantErr: true},
		{name: "EmbeddedHTML", body: "fallback", formatted: `<pre><code class="language-wx_xml"><img src="https://example.com" /></code></pre>`, found: true, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, found, err := portalXML(&event.MessageEventContent{Body: tt.body, Format: event.FormatHTML, FormattedBody: tt.formatted})
			if got != tt.want || found != tt.found || (err != nil) != tt.wantErr {
				t.Fatalf("payload=%q found=%v err=%v; want payload=%q found=%v error=%v", got, found, err, tt.want, tt.found, tt.wantErr)
			}
		})
	}
	// A client must explicitly declare formatted_body as HTML.
	_, found, err := portalXML(&event.MessageEventContent{Body: "plain", FormattedBody: formatted})
	if found || err != nil {
		t.Fatalf("undeclared HTML triggered XML sending: found=%v err=%v", found, err)
	}
}

func TestPortalXMLUsesExistingPermissionAndCommandGates(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		deny bool
	}{
		{"DeniedUser", "```wx_xml\n" + portalXMLFixture + "\n```", true},
		{"LocalCommand", "!wx help\n```wx_xml\n" + portalXMLFixture + "\n```", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			api := &portalMessageAPI{}
			s, rec := newPortalMessageTestServer(t, api)
			if test.deny {
				s.cfg.Bridge.Permissions = map[string]string{"@alice:example.com": "relay"}
			}
			s.handleMatrixEvent(context.Background(), portalTextEvent(rec, test.body))
			if len(api.sent) != 0 {
				t.Fatalf("message bypassed permission/command gate: %#v", api.sent)
			}
		})
	}
}

func TestPortalXMLThroughWechatHTTP(t *testing.T) {
	for _, test := range []struct {
		name     string
		body     string
		html     bool
		response string
		wantPath string
		wantText string
		wantErr  bool
	}{
		{name: "Fenced", body: "```wx_xml\n" + portalXMLFixture + "\n```", wantPath: "/Msg/SendApp"},
		{name: "LeadingProse", body: "explanation\n```wx_xml\n<appmsg><type>oops</type></appmsg>\n```", wantErr: true},
		{name: "LongerClosingFence", body: "```wx_xml\n" + portalXMLFixture + "\n````", wantPath: "/Msg/SendApp"},
		{name: "SpacedLanguage", body: "```` wx_xml\n" + portalXMLFixture + "\n````", wantPath: "/Msg/SendApp"},
		{name: "RenderedHTML", body: portalXMLFixture, html: true, wantPath: "/Msg/SendApp"},
		{name: "OrdinaryText", body: "hello :wechat_smile:", wantPath: "/Msg/SendTxt", wantText: "hello [微笑]"},
		{name: "OtherCodeBlock", body: "```xml\n<msg/>\n```", wantPath: "/Msg/SendTxt", wantText: "```xml\n<msg/>\n```"},
		{name: "InvalidXML", body: "```wx_xml\n<appmsg><type>5</type>\n```", wantErr: true},
		{name: "InvalidFence", body: "```wx_xml\n<appmsg/>", wantErr: true},
		{name: "UpstreamRejects", body: "```wx_xml\n" + portalXMLFixture + "\n```", wantPath: "/Msg/SendApp", response: `{"Success":false,"Code":-1,"Message":"invalid XML"}`, wantErr: true},
		{name: "ProtocolRejects", body: "```wx_xml\n" + portalXMLFixture + "\n```", wantPath: "/Msg/SendApp", response: `{"Success":true,"Code":0,"Data":{"BaseResponse":{"ret":-2}}}`, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != test.wantPath {
					t.Errorf("path=%s want=%s", r.URL.Path, test.wantPath)
				}
				var payload struct {
					Wxid, ToWxid, Xml, Content string
					Type                       int
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				if payload.Wxid != "wxid_self" || payload.ToWxid != "wxid_friend" {
					t.Errorf("wrong account/recipient: %#v", payload)
				}
				if test.wantPath == "/Msg/SendApp" && (payload.Xml != portalXMLFixture || payload.Type != 5) {
					t.Errorf("XML changed or wrong type: %#v", payload)
				}
				if test.wantPath == "/Msg/SendTxt" && (payload.Content != test.wantText || payload.Type != 1) {
					t.Errorf("text changed: %#v", payload)
				}
				response := test.response
				if response == "" {
					response = `{"Success":true,"Code":0,"Data":{"BaseResponse":{"ret":0},"newMsgId":123}}`
				}
				_, _ = w.Write([]byte(response))
			}))
			defer upstream.Close()
			api, err := (wechat.Factory{}).New(remoteapi.Config{BaseURL: upstream.URL, Session: []byte(`{"wxid":"wxid_self"}`)})
			if err != nil {
				t.Fatal(err)
			}
			s, rec := newPortalMessageTestServer(t, &portalMessageAPI{})
			s.api = api
			notices := 0
			homeserver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/send/m.room.message/") {
					var notice event.MessageEventContent
					if err := json.NewDecoder(r.Body).Decode(&notice); err != nil {
						t.Error(err)
					}
					if notice.MsgType == event.MsgNotice && strings.Contains(notice.Body, "发送到 WeChat 失败") {
						notices++
					}
				}
				_, _ = w.Write([]byte(`{"event_id":"$notice"}`))
			}))
			defer homeserver.Close()
			if err := s.as.SetHomeserverURL(homeserver.URL); err != nil {
				t.Fatal(err)
			}
			evt := portalTextEvent(rec, test.body)
			if test.html {
				evt.Content.AsMessage().Format = event.FormatHTML
				evt.Content.AsMessage().FormattedBody = `<pre><code class="language-wx_xml">` + html.EscapeString(test.body) + `</code></pre>`
			}
			s.handleMatrixEvent(context.Background(), evt)
			wantCalls := 1
			if test.wantPath == "" {
				wantCalls = 0
			}
			if calls != wantCalls || (notices == 1) != test.wantErr {
				t.Fatalf("upstream calls=%d notices=%d; want calls=%d error=%v", calls, notices, wantCalls, test.wantErr)
			}
		})
	}
}
