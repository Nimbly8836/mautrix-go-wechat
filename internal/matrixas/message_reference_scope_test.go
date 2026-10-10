package matrixas

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"github.com/leat/mautrix-go-wechat/internal/wechat"
	"maunium.net/go/mautrix/event"
)

func relationXML(kind int, target, chat string) string {
	if kind == 10002 {
		return fmt.Sprintf(`<sysmsg type="revokemsg"><revokemsg><session>%s</session><newmsgid>%s</newmsgid><replacemsg>群成员甲撤回了一条消息</replacemsg></revokemsg></sysmsg>`, chat, target)
	}
	return fmt.Sprintf(`<msg><appmsg><title>这是新正文</title><type>57</type><refermsg><type>1</type><svrid>%s</svrid><fromusr>%s</fromusr><displayname>群成员甲</displayname><content>原消息甲</content></refermsg></appmsg></msg>`, target, chat)
}

func TestWechatMessageReferencesSeparateIDNamespaces(t *testing.T) {
	for _, kind := range []int{49, 10002} {
		t.Run(fmt.Sprint(kind), func(t *testing.T) {
			bridge := newMessageRelationBridge(t)
			bridge.callback(t, 123, 456, 1, "原消息甲", "原消息甲")
			// A short MsgID equal to a different message's NewMsgID must not
			// replace the latter's reference, especially for destructive recalls.
			bridge.callback(t, 456, 789, 1, "无关消息乙", "无关消息乙")
			bridge.callback(t, 999, 1000, kind, relationXML(kind, "456", "group@chatroom"), "[系统消息]")
			messages, redacted := bridge.snapshot()
			if kind == 10002 {
				if len(redacted) != 1 || redacted[0] != messages[0].ID {
					t.Fatalf("recall confused short and long IDs: redacted=%v original=%s", redacted, messages[0].ID)
				}
			} else if len(messages) != 3 || messages[2].Content.RelatesTo == nil || messages[2].Content.RelatesTo.GetReplyTo() != messages[0].ID {
				t.Fatalf("quote confused short and long IDs: %+v", messages)
			}
		})
	}
}

func TestWechatMessageReferencesDoNotCrossPortalRooms(t *testing.T) {
	for _, kind := range []int{49, 10002} {
		t.Run(fmt.Sprint(kind), func(t *testing.T) {
			bridge := newMessageRelationBridge(t)
			bridge.callback(t, 123, 456, 1, "原消息甲", "原消息甲")
			rec, _ := bridge.server.portals.get("group@chatroom")
			rec.RoomID = "!replacement:example.com"
			bridge.server.portals.data.Portals = map[string]portalRecord{"group@chatroom": rec}
			bridge.callback(t, 999, 1000, kind, relationXML(kind, "456", "group@chatroom"), "[系统消息]")
			messages, redacted := bridge.snapshot()
			if len(redacted) != 0 {
				t.Fatalf("recalled an event from the previous portal room: %v", redacted)
			}
			last := messages[len(messages)-1].Content
			if last.RelatesTo != nil && last.RelatesTo.GetReplyTo() != "" {
				t.Fatalf("linked an event from the previous portal room: %+v", last)
			}
			if kind == 10002 && last.MsgType != event.MsgNotice {
				t.Fatalf("missing recall fallback: %+v", last)
			}
		})
	}
}

func TestWechatMessageReferencesRejectPreviousAccountEvents(t *testing.T) {
	for _, kind := range []int{49, 10002} {
		t.Run(fmt.Sprint(kind), func(t *testing.T) {
			bridge := newMessageRelationBridge(t)
			bridge.callback(t, 123, 456, 1, "原消息甲", "原消息甲")
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Errorf("unexpected request after account switch: %s", r.URL.Path)
				http.Error(w, "unexpected", http.StatusNotFound)
			}))
			t.Cleanup(upstream.Close)
			other, err := (wechat.Factory{}).New(remoteapi.Config{BaseURL: upstream.URL, ReceiveMode: "push", Session: []byte(`{"wxid":"wxid_other_account"}`)})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(other.Disconnect)
			bridge.server.api = other
			if self, err := other.Self(t.Context()); err != nil || self != "wxid_other_account" {
				t.Fatalf("account switch fixture failed: %s / %v", self, err)
			}
			// A delayed callback from the previous API must not act in the
			// context of the newly selected account.
			bridge.callback(t, 999, 1000, kind, relationXML(kind, "456", "group@chatroom"), "[系统消息]")
			messages, redacted := bridge.snapshot()
			if len(redacted) != 0 || len(messages) != 1 {
				t.Fatalf("previous account callback acted after account switch: messages=%d redacted=%v", len(messages), redacted)
			}
		})
	}
}

func TestWechatMessageReferencesDoNotCrossAccounts(t *testing.T) {
	for _, kind := range []int{49, 10002} {
		t.Run(fmt.Sprint(kind), func(t *testing.T) {
			b := newMessageRelationBridge(t)
			b.callback(t, 123, 456, 1, "原账号的消息", "原账号的消息")
			other, err := (wechat.Factory{}).New(remoteapi.Config{BaseURL: b.upstreamURL, ReceiveMode: "push", Session: []byte(`{"wxid":"wxid_other_account"}`)})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(other.Disconnect)
			b.api, b.server.api = other, other
			b.callbackPayload(t, map[string]any{"MsgID": 124, "NewMsgID": 789, "MsgType": kind, "Content": relationXML(kind, "456", "group@chatroom"), "Talker": "group@chatroom", "SenderUserName": "wxid_other_account", "IsGroup": true, "IsSendBySelf": true, "CreateTime": 1710000000})
			messages, redacted := b.snapshot()
			if len(redacted) != 0 || len(messages) != 2 || (messages[1].Content.RelatesTo != nil && messages[1].Content.RelatesTo.GetReplyTo() != "") {
				t.Fatalf("new account reused another account's event: redacted=%v messages=%+v", redacted, messages)
			}
		})
	}
}

func TestWechatRecallRejectsDifferentSourceSession(t *testing.T) {
	bridge := newMessageRelationBridge(t)
	bridge.callback(t, 123, 456, 1, "原消息甲", "原消息甲")
	rec, _ := bridge.server.portals.get("group@chatroom")
	other := rec
	other.ChatID, other.RoomID = remoteapi.ChatID("another@chatroom"), "!another:example.com"
	bridge.server.portals.data.Portals[string(other.ChatID)] = other
	bridge.callback(t, 999, 1000, 10002, relationXML(10002, "456", "another@chatroom"), "[系统消息]")
	_, redacted := bridge.snapshot()
	if len(redacted) != 0 {
		t.Fatalf("untrusted XML redirected the recall: %v", redacted)
	}
}
