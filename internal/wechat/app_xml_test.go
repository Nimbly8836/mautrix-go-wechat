package wechat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

func TestAppMessageTypeFromXML(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    int
	}{
		{"Link", `<msg><appmsg appid=""><title>链接</title><type>5</type></appmsg></msg>`, 5},
		{"BareApp", `<appmsg><type>6</type></appmsg>`, 6},
		{"DeclarationCDATA", "<?xml version=\"1.0\"?>\n<msg><appmsg><title><![CDATA[a & b]]></title><type> 19 </type></appmsg></msg>\n<!--end-->", 19},
		{"NestedTypeIgnored", `<msg><appmsg><type>57</type><refermsg><type>1</type></refermsg></appmsg></msg>`, 57},
		{"Empty", "", 0},
		{"PlainText", "hello", 0},
		{"Broken", `<msg><appmsg><type>5</type></msg>`, 0},
		{"Truncated", `<msg><appmsg><type>5</type>`, 0},
		{"MissingType", `<appmsg><title>missing</title></appmsg>`, 0},
		{"NestedTypeOnly", `<appmsg><refermsg><type>5</type></refermsg></appmsg>`, 0},
		{"WrongRoot", `<image><type>3</type></image>`, 0},
		{"NestedAppOnly", `<msg><other><appmsg><type>5</type></appmsg></other></msg>`, 0},
		{"Zero", `<appmsg><type>0</type></appmsg>`, 0},
		{"Negative", `<appmsg><type>-1</type></appmsg>`, 0},
		{"NotInteger", `<appmsg><type>link</type></appmsg>`, 0},
		{"Overflow", `<appmsg><type>2147483648</type></appmsg>`, 0},
		{"TypeElement", `<appmsg><type><b>5</b></type></appmsg>`, 0},
		{"DuplicateType", `<appmsg><type>5</type><type>6</type></appmsg>`, 0},
		{"DuplicateApp", `<msg><appmsg><type>5</type></appmsg><appmsg><type>6</type></appmsg></msg>`, 0},
		{"DuplicateRoot", `<appmsg><type>5</type></appmsg><appmsg><type>6</type></appmsg>`, 0},
		{"TrailingGarbage", `<appmsg><type>5</type></appmsg>extra`, 0},
		{"LeadingGarbage", `extra<appmsg><type>5</type></appmsg>`, 0},
		{"TrailingMalformed", `<appmsg><type>5</type></appmsg><`, 0},
		{"UnknownEntity", `<appmsg><type>5</type><title>&missing;</title></appmsg>`, 0},
		{"DTD", `<!DOCTYPE appmsg [<!ENTITY x SYSTEM "file:///etc/passwd">]><appmsg><type>5</type><title>&x;</title></appmsg>`, 0},
		{"Namespace", `<appmsg xmlns="urn:other"><type>5</type></appmsg>`, 0},
		{"TooLarge", `<appmsg><type>5</type><title>` + strings.Repeat("x", maxAppXMLBytes) + `</title></appmsg>`, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := appMessageTypeFromXML(tt.payload)
			if tt.want == 0 {
				if err == nil {
					t.Fatalf("expected rejection, got type %d", got)
				}
			} else if err != nil || got != tt.want {
				t.Fatalf("got type %d, err %v; want %d", got, err, tt.want)
			}
		})
	}
}

func TestSendAppXMLPreservesPayloadAndReportsFailures(t *testing.T) {
	const payload = " <msg><appmsg><title><![CDATA[:wechat_smile: & <test>]]></title><type>5</type><url>https://example.com/?x=1&amp;y=2</url></appmsg></msg>\n"
	tests := []struct {
		name     string
		response string
		wantID   remoteapi.MessageID
	}{
		{"CurrentUpstream", `{"Code":0,"Success":true,"Data":{"BaseResponse":{"ret":0},"msgId":12,"newMsgId":1234567890123}}`, "1234567890123"},
		{"Legacy", `{"Code":0,"Success":true,"Data":{"MsgId":12}}`, "12"},
		{"EnvelopeFailure", `{"Code":-1,"Success":false,"Message":"invalid xml"}`, ""},
		{"ProtocolFailure", `{"Code":0,"Success":true,"Data":{"BaseResponse":{"ret":-2},"msgId":12}}`, ""},
		{"NoMessageID", `{"Code":0,"Success":true,"Data":{"BaseResponse":{"ret":0}}}`, ""},
		{"NoData", `{"Code":0,"Success":true}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/Msg/SendApp" || r.Method != http.MethodPost {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
				}
				var request sendAppReq
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if request.Wxid != "wxid_self" || request.ToWxid != "group@chatroom" || request.Type != 5 || request.Xml != payload {
					t.Errorf("unexpected request: %#v", request)
				}
				_, _ = w.Write([]byte(tt.response))
			}))
			defer server.Close()
			c := &Client{t: newTransport(server.URL), wxid: "wxid_self"}
			got, err := c.SendMessage(context.Background(), "group@chatroom", remoteapi.OutgoingMessage{Type: remoteapi.MsgApp, Text: payload})
			if (tt.wantID == "") != (err != nil) || got != tt.wantID || calls != 1 {
				t.Fatalf("id=%q err=%v calls=%d; want id=%q calls=1", got, err, calls, tt.wantID)
			}
		})
	}
}

func TestSendAppXMLInvalidPayloadNeverCallsUpstream(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"Code":0,"Success":true,"Data":{"MsgId":1}}`))
	}))
	defer server.Close()
	c := &Client{t: newTransport(server.URL), wxid: "wxid_self"}
	_, err := c.SendMessage(context.Background(), "wxid_friend", remoteapi.OutgoingMessage{Type: remoteapi.MsgApp, Text: `<appmsg><type>oops</type></appmsg>`})
	if err == nil || calls != 0 {
		t.Fatalf("err=%v upstream calls=%d; want rejection before any request", err, calls)
	}
}
