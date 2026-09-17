package wechat

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const incomingSolitaireXML = `<msg><appmsg><title><![CDATA[#接龙

周末活动

1. 小明，1份]]></title><type>53</type><solitaire_info><s>2</s></solitaire_info></appmsg></msg>`

func TestSolitaireSourcesFromLocalAndCallback(t *testing.T) {
	client := &Client{wxid: "wxid_self"}
	for _, callback := range []bool{false, true} {
		name := "Local"
		if callback {
			name = "Callback"
		}
		t.Run(name, func(t *testing.T) {
			local := localMessage{
				MsgID: 321, NewMsgID: 9007199254740993, MsgType: wxApp,
				Talker: "group@chatroom", SenderUserName: "wxid_friend", IsGroup: true,
				Content: "wxid_friend:\n" + incomingSolitaireXML, ContentText: "[接龙]", CreateTime: 123,
			}
			evt, ok := client.convertLocal(context.Background(), local)
			if callback {
				var raw rawMessage
				// Exercise the real JSON shape/large-number decoding as well.
				data, _ := json.Marshal(local)
				if err := json.Unmarshal(data, &raw); err != nil {
					t.Fatal(err)
				}
				evt, ok = client.convertInbound(context.Background(), raw)
			}
			if !ok || evt.Message == nil || evt.Message.Solitaire == nil {
				t.Fatalf("no source: %#v", evt.Message)
			}
			source := evt.Message.Solitaire
			if source.Account != "wxid_self" || source.Chat != "group@chatroom" || source.Reference != `{"MsgID":321,"NewMsgID":9007199254740993}` {
				t.Fatalf("wrong source: %#v", source)
			}
			if !strings.Contains(evt.Message.Text, "1. 小明，1份") || strings.Contains(evt.Message.Text, "<appmsg") {
				t.Fatalf("chain title not shown: %q", evt.Message.Text)
			}
		})
	}
}

func TestNonSolitaireAndUnverifiableMessagesHaveNoSource(t *testing.T) {
	client := &Client{wxid: "wxid_self"}
	for _, tt := range []struct {
		name, xml string
		msgType   int
		id        int64
	}{
		{"OtherSubtype", strings.Replace(incomingSolitaireXML, "<type>53</type>", "<type>5</type>", 1), wxApp, 1},
		{"PlainMessage", "#接龙\n1. 小明", wxText, 1},
		{"PlainTitleOnly", "#接龙\n1. 小明", wxApp, 1},
		{"NoIDs", incomingSolitaireXML, wxApp, 0},
		{"MalformedOriginal", "<msg><appmsg><type>53</type>", wxApp, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			evt, ok := client.convertLocal(context.Background(), localMessage{MsgType: tt.msgType, MsgID: tt.id, Talker: "group@chatroom", Content: tt.xml, ContentText: incomingSolitaireXML})
			if !ok || evt.Message.Solitaire != nil {
				t.Fatalf("unexpected source or dropped message: %#v", evt.Message)
			}
		})
	}
}
