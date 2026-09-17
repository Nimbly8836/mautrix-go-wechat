package matrixas

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"maunium.net/go/mautrix/event"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"github.com/leat/mautrix-go-wechat/internal/wechat"
)

func TestSolitaireCallbackToMatrixReplyToHTTP(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/Msg/SendSolitaire" {
			t.Errorf("unexpected upstream request %s %s", r.Method, r.URL)
		}
		var request map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if len(request) != 5 || string(request["Wxid"]) != `"wxid_self"` || string(request["ToWxid"]) != `"group@chatroom"` || string(request["MsgID"]) != "123" || string(request["NewMsgID"]) != "9007199254740993" || string(request["Content"]) != `"张三，1份"` {
			t.Errorf("incorrect source IDs/entry/account/chat: %s", request)
		}
		_, _ = w.Write([]byte(`{"Code":0,"Success":true,"Data":{"Xml":"generated","SendResult":{"BaseResponse":{"ret":0},"newMsgId":9007199254740995}}}`))
	}))
	defer upstream.Close()
	api, err := (wechat.Factory{}).New(remoteapi.Config{BaseURL: upstream.URL, Session: []byte(`{"wxid":"wxid_self"}`)})
	if err != nil {
		t.Fatal(err)
	}
	s, rec := newPortalMessageTestServer(t, &portalMessageAPI{})
	rec.ChatID, rec.IsGroup = "group@chatroom", true
	s.portals.data.Portals = map[string]portalRecord{string(rec.ChatID): rec}
	s.portals.path = filepath.Join(t.TempDir(), "portals.json")
	s.api = api
	var bridgedContent json.RawMessage
	var notices []string
	homeserver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/event/$source") {
			_ = json.NewEncoder(w).Encode(map[string]any{"event_id": "$source", "room_id": rec.RoomID, "sender": s.as.BotMXID(), "type": "m.room.message", "content": bridgedContent})
			return
		}
		if strings.Contains(r.URL.Path, "/send/m.room.message/") {
			var raw json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
				t.Error(err)
			}
			var content event.MessageEventContent
			if err := json.Unmarshal(raw, &content); err != nil {
				t.Error(err)
			}
			if content.MsgType == event.MsgText {
				bridgedContent = raw
			} else {
				notices = append(notices, content.Body)
			}
		}
		_, _ = w.Write([]byte(`{"event_id":"$source"}`))
	}))
	defer homeserver.Close()
	if err := s.as.SetHomeserverURL(homeserver.URL); err != nil {
		t.Fatal(err)
	}
	const xml = `<msg><appmsg><title><![CDATA[#接龙

周末活动

1. 小明，1份]]></title><type>53</type></appmsg></msg>`
	// Use the real webhook decoder, not only convertInbound, to ensure the
	// 64-bit ID survives every layer before it reaches SendSolitaire.
	payload := []byte(`{"MsgID":123,"NewMsgID":9007199254740993,"MsgType":49,"Content":` + strconv.Quote(xml) + `,"ContentText":"[接龙]","Talker":"group@chatroom","SenderUserName":"wxid_self","IsGroup":true,"IsSendBySelf":true,"CreateTime":1710000000}`)
	if err := api.(remoteapi.WebhookReceiver).HandleWebhook(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
	select {
	case received := <-api.Events():
		if received.Message == nil || received.Message.Solitaire == nil {
			t.Fatalf("solitaire source missing after webhook: %#v", received)
		}
		s.OnMessage(context.Background(), received.Message)
	default:
		t.Fatal("webhook emitted no message")
	}
	if len(bridgedContent) == 0 || !strings.Contains(string(bridgedContent), solitaireSourceKey) || !strings.Contains(string(bridgedContent), "9007199254740993") {
		t.Fatalf("source metadata missing/rounded in Matrix event: %s", bridgedContent)
	}
	evt := solitaireReplyEvent(rec, "> <@wechatbot:example.com> #接龙\n> 1. 小明，1份\n\n!wx 接龙 张三，1份")
	evt.Content.AsMessage().Format = event.FormatHTML
	evt.Content.AsMessage().FormattedBody = "<mx-reply><blockquote>原接龙</blockquote></mx-reply>!wx 接龙 张三，1份"
	s.handleMatrixEvent(context.Background(), evt)
	s.handleMatrixEvent(context.Background(), evt)
	if calls != 1 || !strings.Contains(strings.Join(notices, "\n"), "协议服务确认已发送") || !strings.Contains(strings.Join(notices, "\n"), "不会重复发送") {
		t.Fatalf("calls=%d notices=%v", calls, notices)
	}
}
