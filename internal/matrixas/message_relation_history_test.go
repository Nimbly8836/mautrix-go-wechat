package matrixas

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"github.com/leat/mautrix-go-wechat/internal/wechat"
)

func TestWechatRelationsFromLocalHistory(t *testing.T) {
	for _, kind := range []int{49, 10002} {
		for _, prefixed := range []bool{false, true} {
			t.Run(fmt.Sprintf("type_%d/prefixed_%t", kind, prefixed), func(t *testing.T) {
				bridge := newMessageRelationBridge(t)
				body := relationXML(kind, "9007199254740993", "group@chatroom")
				if prefixed {
					body = "wxid_self:\n" + body
				}
				original := map[string]any{"MsgID": 123, "NewMsgID": int64(9007199254740993), "MsgType": 1, "Content": "原消息甲", "Talker": "group@chatroom", "SenderUserName": "wxid_self", "IsGroup": true, "IsSendBySelf": true, "CreateTime": 1710000000}
				related := map[string]any{"MsgID": 124, "NewMsgID": int64(9007199254740995), "MsgType": kind, "Content": body, "ContentText": "[系统摘要]", "Talker": "group@chatroom", "SenderUserName": "wxid_self", "IsGroup": true, "IsSendBySelf": true, "CreateTime": 1710000001}
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					switch r.URL.Path {
					case "/Login/HeartBeat":
						_, _ = w.Write([]byte(`{"Code":0,"Success":true}`))
					case "/Msg/QueryLocal":
						var request struct{ Desc bool }
						if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
							t.Error(err)
						}
						if !request.Desc {
							t.Error("expected initial descending history query")
						}
						_ = json.NewEncoder(w).Encode(map[string]any{"Code": 0, "Success": true, "Data": map[string]any{"total": 2, "list": []any{related, original}}})
					default:
						t.Errorf("unexpected history request: %s", r.URL.Path)
						http.Error(w, "unexpected", http.StatusNotFound)
					}
				}))
				t.Cleanup(upstream.Close)
				api, err := (wechat.Factory{}).New(remoteapi.Config{BaseURL: upstream.URL, ReceiveMode: "poll", Session: []byte(`{"wxid":"wxid_self"}`)})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(api.Disconnect)
				bridge.server.api = api
				if err := api.Connect(t.Context()); err != nil {
					t.Fatal(err)
				}
				timer := time.NewTimer(5 * time.Second)
				defer timer.Stop()
				for count := 0; count < 2; {
					select {
					case received := <-api.Events():
						if received.Error != nil {
							t.Fatal(received.Error)
						}
						if received.Message != nil {
							bridge.server.OnMessage(t.Context(), received.Message)
							count++
						}
						if received.Redaction != nil {
							bridge.server.OnRedaction(t.Context(), received.Redaction)
							count++
						}
					case <-timer.C:
						t.Fatal("history did not emit both original and related event")
					}
				}
				api.Disconnect()
				messages, redacted := bridge.snapshot()
				if len(messages) != 2 {
					t.Fatalf("history lost an event: %+v", messages)
				}
				if kind == 10002 {
					if len(redacted) != 1 || redacted[0] != messages[0].ID {
						t.Fatalf("history recall did not redact original: %v", redacted)
					}
				} else if messages[1].Content.RelatesTo == nil || messages[1].Content.RelatesTo.GetReplyTo() != messages[0].ID || messages[1].Content.Body != "这是新正文" {
					t.Fatalf("history quotation lost its target/body: %+v", messages[1])
				}
			})
		}
	}
}

func TestWechatGroupQuotedReplyKeepsAuthoritativeSender(t *testing.T) {
	for _, supplied := range []string{"", "wxid_alice", "wxid_bob"} {
		t.Run("sender_"+supplied, func(t *testing.T) {
			b := newMessageRelationBridge(t)
			b.callback(t, 123, 456, 1, "原消息甲", "原消息甲")
			wantSender := "wxid_alice"
			if supplied != "" {
				wantSender = supplied
			}
			b.callbackPayload(t, map[string]any{"MsgID": 122, "NewMsgID": 455, "MsgType": 1, "Content": "同一作者的普通消息", "Talker": "group@chatroom", "SenderUserName": wantSender, "IsGroup": true, "CreateTime": 1710000000})
			raw := "wxid_alice:\n" + relationXML(49, "456", "group@chatroom")
			b.callbackPayload(t, map[string]any{"AddMsgs": []any{map[string]any{"MsgId": 124, "NewMsgId": 789, "MsgType": 49, "Content": map[string]string{"Str": raw}, "ContentText": "[引用消息]", "SenderUserName": supplied, "FromUserName": "group@chatroom", "ToUserName": "wxid_self", "CreateTime": 1710000001}}})
			messages, _ := b.snapshot()
			if len(messages) != 3 {
				t.Fatalf("missing quoted message: %+v", messages)
			}
			reply := messages[2]
			if supplied == "wxid_bob" {
				if reply.Content.RelatesTo != nil || reply.Content.Body != "[引用消息]" {
					t.Fatalf("conflicting sender envelope was trusted: %+v", reply)
				}
			} else if reply.Content.RelatesTo == nil || reply.Content.RelatesTo.GetReplyTo() != messages[0].ID || reply.Content.Body != "这是新正文" {
				t.Fatalf("group quote lost its original target/body: %+v", reply)
			}
			if reply.Sender != messages[1].Sender {
				t.Fatalf("group reply replaced sender identity: %s", reply.Sender)
			}
		})
	}
}

func TestWechatGroupMemberRecallUsesOriginalMatrixSender(t *testing.T) {
	bridge := newMessageRelationBridge(t)
	original := map[string]any{"MsgID": 123, "NewMsgID": 456, "MsgType": 1, "Content": "原消息甲", "Talker": "group@chatroom", "SenderUserName": "wxid_alice", "IsGroup": true, "CreateTime": 1710000000}
	bridge.callbackPayload(t, original)
	bridge.callbackPayload(t, map[string]any{"AddMsgs": []any{map[string]any{"MsgId": 124, "NewMsgId": 789, "MsgType": 10002, "Content": map[string]string{"Str": "wxid_alice:\n" + relationXML(10002, "456", "group@chatroom")}, "ContentText": "[系统摘要]", "FromUserName": map[string]string{"Str": "group@chatroom"}, "ToUserName": "wxid_self", "CreateTime": 1710000001}}})
	messages, redacted := bridge.snapshot()
	if len(redacted) != 1 || redacted[0] != messages[0].ID {
		t.Fatalf("member recall lost its target: %v", redacted)
	}
	if messages[0].Sender == bridge.server.as.BotMXID() || !strings.HasPrefix(string(messages[0].Sender), "@wechat_") {
		t.Fatalf("original was not sent by its ghost: %s", messages[0].Sender)
	}
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	if bridge.redactionSenders[0] != messages[0].Sender {
		t.Fatalf("recall used a different Matrix sender: %s vs %s", bridge.redactionSenders[0], messages[0].Sender)
	}
}
