package matrixas

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"github.com/leat/mautrix-go-wechat/internal/wechat"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func TestWechatGroupQuotedReplyToMatrix(t *testing.T) {
	bridge := newMessageRelationBridge(t)
	bridge.callback(t, 123, 9007199254740993, 1, "原消息：明天开会", "原消息：明天开会")
	bridge.callback(t, 124, 9007199254740995, 49, `<msg><appmsg><title><![CDATA[收到，下午见]]></title><type>57</type><refermsg><type>1</type><svrid>9007199254740993</svrid><fromusr>group@chatroom</fromusr><chatusr>wxid_self</chatusr><displayname>群成员甲</displayname><content><![CDATA[原消息：明天开会]]></content></refermsg></appmsg></msg>`, "[引用消息]")
	messages, _ := bridge.snapshot()
	if len(messages) != 2 {
		t.Fatalf("expected original and quoted reply, got %d messages", len(messages))
	}
	reply := messages[1].Content
	if reply.RelatesTo == nil || reply.RelatesTo.GetReplyTo() != messages[0].ID || !strings.Contains(reply.Body, "收到，下午见") {
		t.Fatalf("group quotation lost its native reply or new text: relation=%+v body=%q", reply.RelatesTo, reply.Body)
	}
}

func TestWechatGroupRecallRedactsOriginalAndShowsNotice(t *testing.T) {
	bridge := newMessageRelationBridge(t)
	bridge.callback(t, 123, 9007199254740993, 1, "准备撤回的消息", "准备撤回的消息")
	bridge.callback(t, 124, 9007199254740995, 10002, `<sysmsg type="revokemsg"><revokemsg><session>group@chatroom</session><msgid>123</msgid><newmsgid>9007199254740993</newmsgid><replacemsg><![CDATA[群成员甲撤回了一条消息]]></replacemsg></revokemsg></sysmsg>`, "[系统消息]")
	messages, redacted := bridge.snapshot()
	if len(redacted) != 1 || redacted[0] != messages[0].ID {
		t.Fatalf("WeChat recall did not redact the original Matrix event: redacted=%v messages=%+v", redacted, messages)
	}
	if len(messages) != 2 || messages[1].Content.MsgType != event.MsgNotice || messages[1].Content.Body != "群成员甲撤回了一条消息" {
		t.Fatalf("recall lost its readable notice: %+v", messages)
	}
}

type relationMatrixMessage struct {
	ID      id.EventID
	Room    id.RoomID
	Sender  id.UserID
	Content event.MessageEventContent
}

type messageRelationBridge struct {
	upstreamURL         string
	api                 remoteapi.PuppetAPI
	server              *Server
	mu                  sync.Mutex
	messages            []relationMatrixMessage
	redacted            []id.EventID
	redactionSenders    []id.UserID
	redactionTxns       []string
	redactionStatus     int
	missingRedactionAck bool
	redactionTimeout    bool
}

// The only substitutes are external HTTP servers. Backend conversion, Events,
// Server.OnMessage and the Matrix SDK all run normally.
func newMessageRelationBridge(t *testing.T) *messageRelationBridge {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Login/HeartBeat" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Code":0,"Success":true}`))
			return
		}
		if r.URL.Path != "/Friend/GetContractDetail" {
			t.Errorf("unexpected WeChat request: %s %s", r.Method, r.URL.Path)
		}
		http.Error(w, "contact unavailable", http.StatusNotFound)
	}))
	t.Cleanup(upstream.Close)
	api, err := (wechat.Factory{}).New(remoteapi.Config{BaseURL: upstream.URL, ReceiveMode: "push", Session: []byte(`{"wxid":"wxid_self"}`)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(api.Disconnect)
	s, rec := newPortalMessageTestServer(t, &portalMessageAPI{})
	s.api = api
	s.cfg.Homeserver.Domain, s.cfg.Appservice.ID, s.as.Registration.ID = "example.com", "wechat", "wechat"
	rec.ChatID, rec.IsGroup = "group@chatroom", true
	s.portals.data.Portals = map[string]portalRecord{string(rec.ChatID): rec}
	bridge := &messageRelationBridge{api: api, server: s, upstreamURL: upstream.URL}
	homeserver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bridge.mu.Lock()
		defer bridge.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/register"):
			var registration struct {
				Username string `json:"username"`
			}
			if err := json.NewDecoder(r.Body).Decode(&registration); err != nil {
				t.Error(err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"user_id": "@" + registration.Username + ":example.com"})
		case strings.Contains(r.URL.Path, "/profile/") || (r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/invite")):
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/join"):
			_ = json.NewEncoder(w).Encode(map[string]any{"room_id": rec.RoomID})
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/send/m.room.message/"):
			var content event.MessageEventContent
			if err := json.NewDecoder(r.Body).Decode(&content); err != nil {
				t.Error(err)
			}
			messageID := id.EventID(fmt.Sprintf("$message-%d", len(bridge.messages)+1))
			sender := id.UserID(r.URL.Query().Get("user_id"))
			if sender == "" {
				sender = s.as.BotMXID()
			}
			roomPath := strings.SplitN(r.URL.Path, "/rooms/", 2)[1]
			room := id.RoomID(strings.SplitN(roomPath, "/", 2)[0])
			bridge.messages = append(bridge.messages, relationMatrixMessage{messageID, room, sender, content})
			_ = json.NewEncoder(w).Encode(map[string]any{"event_id": messageID})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/event/"):
			for _, message := range bridge.messages {
				if strings.HasSuffix(r.URL.Path, "/"+string(message.ID)) {
					_ = json.NewEncoder(w).Encode(map[string]any{"event_id": message.ID, "room_id": message.Room, "sender": message.Sender, "type": "m.room.message", "content": message.Content})
					return
				}
			}
			http.Error(w, `{"errcode":"M_NOT_FOUND"}`, http.StatusNotFound)
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/redact/"):
			parts := strings.Split(r.URL.Path, "/redact/")
			bridge.redacted = append(bridge.redacted, id.EventID(strings.Split(parts[1], "/")[0]))
			bridge.redactionSenders = append(bridge.redactionSenders, id.UserID(r.URL.Query().Get("user_id")))
			bridge.redactionTxns = append(bridge.redactionTxns, strings.Split(parts[1], "/")[1])
			var request struct {
				Reason string `json:"reason"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			if bridge.redactionTimeout {
				<-r.Context().Done()
				return
			}
			if bridge.redactionStatus != 0 {
				w.WriteHeader(bridge.redactionStatus)
				_, _ = w.Write([]byte(`{"errcode":"M_FORBIDDEN","error":"redaction denied"}`))
			} else if bridge.missingRedactionAck {
				_, _ = w.Write([]byte(`{}`))
			} else {
				_ = json.NewEncoder(w).Encode(map[string]any{"event_id": "$redaction"})
			}
		default:
			t.Errorf("unexpected Matrix request: %s %s", r.Method, r.URL.Path)
			http.Error(w, `{"errcode":"M_NOT_FOUND"}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(homeserver.Close)
	if err := s.as.SetHomeserverURL(homeserver.URL); err != nil {
		t.Fatal(err)
	}
	return bridge
}

func (b *messageRelationBridge) callback(t *testing.T, msgID, newMsgID int64, kind int, raw, rendered string) {
	t.Helper()
	b.callbackPayload(t, map[string]any{"MsgID": msgID, "NewMsgID": newMsgID, "MsgType": kind, "Content": raw, "ContentText": rendered, "Talker": "group@chatroom", "SenderUserName": "wxid_self", "IsGroup": true, "IsSendBySelf": true, "CreateTime": 1710000000})
}

func (b *messageRelationBridge) callbackPayload(t *testing.T, data any) {
	t.Helper()
	payload, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.api.(remoteapi.WebhookReceiver).HandleWebhook(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
	select {
	case received := <-b.api.Events():
		switch {
		case received.Message != nil:
			b.server.OnMessage(t.Context(), received.Message)
		case received.Redaction != nil:
			b.server.OnRedaction(t.Context(), received.Redaction)
		default:
			t.Fatalf("callback emitted no message or recall: %+v", received)
		}
	default:
		t.Fatal("callback emitted no event")
	}
}

func (b *messageRelationBridge) snapshot() ([]relationMatrixMessage, []id.EventID) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]relationMatrixMessage(nil), b.messages...), append([]id.EventID(nil), b.redacted...)
}
