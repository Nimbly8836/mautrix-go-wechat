package matrixas

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"github.com/leat/mautrix-go-wechat/internal/wechat"
)

func TestSolitaireCallbackToMatrixReplyToHTTP(t *testing.T) {
	for _, callbackFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("callback_first=%v", callbackFirst), func(t *testing.T) {
			const originalTitle = "#接龙\n\n周末活动\n\n1. 小明，1份"
			const sentTitle = originalTitle + "\n2. 张三，1份"
			const nextTitle = sentTitle + "\n3. 李四，2份"
			xmlFor := func(title string) string {
				return `<msg><appmsg><title><![CDATA[` + title + `]]></title><type>53</type></appmsg></msg>`
			}
			var echo func() error
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
				oldID, oldNewID, entry := "123", "9007199254740993", `"张三，1份"`
				newID, newLongID, title := 456, int64(9007199254740995), sentTitle
				if calls == 2 {
					oldID, oldNewID, entry = "456", "9007199254740995", `"李四，2份"`
					newID, newLongID, title = 789, 9007199254740997, nextTitle
				}
				if len(request) != 5 || string(request["Wxid"]) != `"wxid_self"` || string(request["ToWxid"]) != `"group@chatroom"` || string(request["MsgID"]) != oldID || string(request["NewMsgID"]) != oldNewID || string(request["Content"]) != entry {
					t.Errorf("incorrect source IDs/entry/account/chat: %s", request)
				}
				if calls == 1 && callbackFirst {
					if err := echo(); err != nil {
						t.Error(err)
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"Code": 0, "Success": true, "Data": map[string]any{"Xml": xmlFor(title), "SendResult": map[string]any{"BaseResponse": map[string]int{"ret": 0}, "msgId": newID, "newMsgId": newLongID}}})
			}))
			defer upstream.Close()
			api, err := (wechat.Factory{}).New(remoteapi.Config{BaseURL: upstream.URL, Session: []byte(`{"wxid":"wxid_self"}`)})
			if err != nil {
				t.Fatal(err)
			}
			s, rec := newPortalMessageTestServer(t, &portalMessageAPI{})
			s.cfg.Bridge.IgnoredCommandPrefixes = []string{"!"}
			s.cfg.Bridge.Permissions = map[string]string{"@daily_bot:example.com": "user"}
			rec.ChatID, rec.IsGroup = "group@chatroom", true
			s.portals.data.Portals = map[string]portalRecord{string(rec.ChatID): rec}
			s.portals.path = filepath.Join(t.TempDir(), "portals.json")
			s.api = api
			var mu sync.Mutex
			messages := map[id.EventID]json.RawMessage{}
			var notices []string
			homeserver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/event/") {
					eventID := id.EventID(r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:])
					_ = json.NewEncoder(w).Encode(map[string]any{"event_id": eventID, "room_id": rec.RoomID, "sender": s.as.BotMXID(), "type": "m.room.message", "content": messages[eventID]})
					return
				}
				eventID := id.EventID("$notice")
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
						eventID = id.EventID(fmt.Sprintf("$chain-%d", len(messages)+1))
						messages[eventID] = raw
					} else {
						notices = append(notices, content.Body)
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"event_id": eventID})
			}))
			defer homeserver.Close()
			if err := s.as.SetHomeserverURL(homeserver.URL); err != nil {
				t.Fatal(err)
			}
			callback := func(msgID, newMsgID int64, title string) error {
				payload := []byte(fmt.Sprintf(`{"MsgID":%d,"NewMsgID":%d,"MsgType":49,"Content":%s,"ContentText":"[接龙]","Talker":"group@chatroom","SenderUserName":"wxid_self","IsGroup":true,"IsSendBySelf":true,"CreateTime":1710000000}`, msgID, newMsgID, strconv.Quote(xmlFor(title))))
				if err := api.(remoteapi.WebhookReceiver).HandleWebhook(context.Background(), payload); err != nil {
					return err
				}
				select {
				case received := <-api.Events():
					if received.Message == nil || received.Message.Solitaire == nil {
						return fmt.Errorf("solitaire source missing: %#v", received)
					}
					s.OnMessage(context.Background(), received.Message)
				default:
					return fmt.Errorf("webhook emitted no message")
				}
				return nil
			}
			echo = func() error { return callback(456, 9007199254740995, sentTitle) }
			assertMessages := func(count int, eventID id.EventID, title, reference string) {
				t.Helper()
				mu.Lock()
				defer mu.Unlock()
				var content struct {
					event.MessageEventContent
					Source remoteapi.SolitaireSource `json:"com.leat.wechat.solitaire_source"`
				}
				if err := json.Unmarshal(messages[eventID], &content); err != nil {
					t.Fatalf("missing chain %s: messages=%d notices=%v err=%v", eventID, len(messages), notices, err)
				}
				if len(messages) != count || len(notices) != 0 || content.Body != title+"\n\nReply: !wx jl <text>" || content.Source.Account != "wxid_self" || content.Source.Chat != rec.ChatID || content.Source.Reference != reference {
					t.Fatalf("incorrect/duplicate chain: messages=%d notices=%v content=%#v", len(messages), notices, content)
				}
			}
			if err := callback(123, 9007199254740993, originalTitle); err != nil {
				t.Fatal(err)
			}
			assertMessages(1, "$chain-1", originalTitle, `{"MsgID":123,"NewMsgID":9007199254740993}`)
			evt := solitaireReplyEvent(rec, "> <@wechatbot:example.com> #接龙\n> 1. 小明，1份\n\n!wx jl 张三，1份")
			evt.Sender = "@daily_bot:example.com"
			evt.Content.AsMessage().RelatesTo.SetReplyTo("$chain-1")
			evt.Content.AsMessage().Format = event.FormatHTML
			evt.Content.AsMessage().FormattedBody = "<mx-reply><blockquote>原接龙</blockquote></mx-reply>!wx jl 张三，1份"
			s.handleMatrixEvent(context.Background(), evt)
			assertMessages(2, "$chain-2", sentTitle, `{"MsgID":456,"NewMsgID":9007199254740995}`)
			if !callbackFirst {
				if err := echo(); err != nil {
					t.Fatal(err)
				}
				assertMessages(2, "$chain-2", sentTitle, `{"MsgID":456,"NewMsgID":9007199254740995}`)
			}
			// The rendered response is a real new source: reply again as another
			// authorized Matrix bot and verify the new IDs reach the backend.
			next := solitaireReplyEvent(rec, "!wx jl 李四，2份")
			next.ID, next.Sender = "$command-next", evt.Sender
			next.Content.AsMessage().RelatesTo.SetReplyTo("$chain-2")
			s.handleMatrixEvent(context.Background(), next)
			assertMessages(3, "$chain-3", nextTitle, `{"MsgID":789,"NewMsgID":9007199254740997}`)
			s.handleMatrixEvent(context.Background(), evt)
			s.handleMatrixEvent(context.Background(), next)
			mu.Lock()
			defer mu.Unlock()
			if calls != 2 || len(messages) != 3 || len(notices) != 2 || !strings.Contains(strings.Join(notices, "\n"), "不会重复发送") {
				t.Fatalf("commands replayed: calls=%d messages=%d notices=%v", calls, len(messages), notices)
			}
		})
	}
}
