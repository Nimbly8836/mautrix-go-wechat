package wechat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

func TestJoinSolitaireReturnsOnlyConfirmedChain(t *testing.T) {
	const title = "#接龙\n📢 午餐 &lt;菜单&gt;\n\n1. 小明\n2. 张三，1份 :wechat_smile:"
	const validXML = `<msg><appmsg><title><![CDATA[` + title + `]]></title><type>53</type></appmsg></msg>`
	for _, tc := range []struct {
		name   string
		xml    any
		want   string
		ret    int
		legacy bool
	}{
		{name: "complete returned title", xml: validXML, want: title},
		{name: "appmsg root and single entity decoding", xml: `<appmsg><title>&lt;标题&gt; &amp;lt;\n1. 甲</title><type>53</type></appmsg>`, want: "<标题> &lt;\\n1. 甲"},
		{name: "legacy sent ID", xml: validXML, want: title, legacy: true},
		{name: "missing XML"},
		{name: "empty XML", xml: ""},
		{name: "wrong XML JSON type", xml: 123},
		{name: "malformed XML", xml: `<msg><appmsg><title>new</title><type>53</type>`},
		{name: "wrong type", xml: `<appmsg><title>new</title><type>5</type></appmsg>`},
		{name: "no title", xml: `<appmsg><type>53</type></appmsg>`},
		{name: "blank title", xml: `<appmsg><title>  </title><type>53</type></appmsg>`},
		{name: "duplicate title", xml: `<appmsg><title>one</title><title>two</title><type>53</type></appmsg>`},
		{name: "nested title is not full chain", xml: `<appmsg><refer><title>quoted</title></refer><type>53</type></appmsg>`},
		{name: "title child elements", xml: `<appmsg><title>first <nested>missing</nested> last</title><type>53</type></appmsg>`},
		{name: "namespaced title", xml: `<appmsg xmlns:x="urn:test"><x:title>not the title</x:title><type>53</type></appmsg>`},
		{name: "namespaced app before target", xml: `<msg xmlns:x="urn:test"><x:appmsg><title>not the title</title></x:appmsg><appmsg><type>53</type></appmsg></msg>`},
		{name: "namespaced app after target", xml: `<msg xmlns:x="urn:test"><appmsg><type>53</type></appmsg><x:appmsg><title>not the title</title></x:appmsg></msg>`},
		{name: "ignore namespaced app sibling", xml: `<msg xmlns:x="urn:test"><x:appmsg><title>not the title</title></x:appmsg><appmsg><title>real title</title><type>53</type></appmsg></msg>`, want: "real title"},
		{name: "DTD", xml: `<!DOCTYPE msg><appmsg><title>new</title><type>53</type></appmsg>`},
		{name: "oversized", xml: strings.Repeat(" ", maxAppXMLBytes+1)},
		{name: "rejected send cannot publish returned XML", xml: validXML, ret: -2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				result := map[string]any{"BaseResponse": map[string]int{"ret": tc.ret}, "msgId": 456, "createTime": 1710000001}
				if !tc.legacy {
					result["newMsgId"] = int64(9007199254740995)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"Code": 0, "Success": true, "Data": map[string]any{"Xml": tc.xml, "SendResult": result}})
			}))
			defer server.Close()
			client := &Client{t: newTransport(server.URL), wxid: "wxid_self"}
			source := newSolitaireSource("wxid_self", "group@chatroom", 123, 9007199254740993)
			got, err := client.JoinSolitaire(context.Background(), *source, "INPUT MUST NOT BE USED TO REBUILD THE TITLE")
			if calls != 1 || (err != nil) != (tc.ret != 0) {
				t.Fatalf("calls=%d result=%#v err=%v", calls, got, err)
			}
			if tc.want == "" {
				if got.Message != nil || (tc.ret == 0 && got.ID == "") {
					t.Fatalf("missing/invalid XML fabricated a chain or lost confirmation: %#v", got)
				}
				return
			}
			wantID := remoteapi.MessageID("9007199254740995")
			wantSource := newSolitaireSource("wxid_self", "group@chatroom", 456, 9007199254740995)
			wantKeys := []string{"new:9007199254740995", "msg:456"}
			if tc.legacy {
				wantID = "456"
				wantSource = newSolitaireSource("wxid_self", "group@chatroom", 456, 0)
				wantKeys = []string{"msg:456"}
			}
			m := got.Message
			if got.ID != wantID || m == nil || m.ID != wantID || m.Text != tc.want || m.Chat != source.Chat || m.Sender != source.Account || m.Type != remoteapi.MsgApp || m.Timestamp.Unix() != 1710000001 || m.Solitaire == nil || *m.Solitaire != *wantSource || !reflect.DeepEqual(m.DeduplicationKeys, wantKeys) {
				t.Fatalf("wrong title, identity or fresh source: result=%#v message=%#v", got, m)
			}
			// Response, push and local history must use the same opaque aliases.
			callback := remoteapi.InboundMessage{Chat: source.Chat, Sender: source.Account}
			newID := int64(9007199254740995)
			if tc.legacy {
				newID = 0
			}
			client.attachSolitaireSource(&callback, tc.xml.(string), 456, newID)
			if !reflect.DeepEqual(callback.DeduplicationKeys, wantKeys) || callback.Text != tc.want {
				t.Fatalf("callback title/identity differs: %#v", callback)
			}
		})
	}
}
