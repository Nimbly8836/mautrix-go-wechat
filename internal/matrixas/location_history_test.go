package matrixas

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"github.com/leat/mautrix-go-wechat/internal/wechat"
	"maunium.net/go/mautrix/event"
)

func TestLocationLocalHistoryToMatrix(t *testing.T) {
	for _, tc := range []struct {
		name, raw, rendered, body, geo string
		group                          bool
	}{
		{"raw XML before rendered summary", locationXMLFixture, "[位置]", "示例餐饮店\n示例省示例市示例区示例大道166号示例培训大楼第一层104-02室", "geo:31.234567,121.456789", false},
		{"rendered XML when raw absent", "", locationXMLFixture, "示例餐饮店\n示例省示例市示例区示例大道166号示例培训大楼第一层104-02室", "geo:31.234567,121.456789", false},
		{"invalid coordinates keep address", `<msg><location x="1" label="完整历史地址"/></msg>`, "[位置]", "完整历史地址", "", false},
		{"prefixed group history", "wxid_self:\n" + locationXMLFixture, "[位置]", "示例餐饮店\n示例省示例市示例区示例大道166号示例培训大楼第一层104-02室", "geo:31.234567,121.456789", true},
		{"readable rendered fallback", `<msg><location x="NaN" y="2"/></msg>`, "地点\n完整历史地址", "地点\n完整历史地址", "", false},
		{"malformed XML with rendered fallback", `<msg>`, "地点\n完整历史地址", "地点\n完整历史地址", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			talker, wantChat := "wxid_self", "wxid_friend"
			if tc.group {
				talker, wantChat = "group@chatroom", "group@chatroom"
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/Login/HeartBeat":
					_, _ = w.Write([]byte(`{"Code":0,"Success":true}`))
				case "/Msg/QueryLocal":
					_ = json.NewEncoder(w).Encode(map[string]any{"Code": 0, "Success": true, "Data": map[string]any{"total": 1, "list": []any{map[string]any{
						"MsgID": 123, "NewMsgID": 456, "MsgType": 48,
						"Content": tc.raw, "ContentText": tc.rendered, "PushContent": "[位置]",
						"Talker": talker, "SenderUserName": "wxid_self", "FromUserName": "wxid_self", "ToUserName": wantChat,
						"IsSendBySelf": true, "IsGroup": tc.group, "CreateTime": 1710000000,
					}}}})
				default:
					t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
					http.Error(w, "unexpected request", http.StatusNotFound)
				}
			}))
			t.Cleanup(upstream.Close)
			api, err := (wechat.Factory{}).New(remoteapi.Config{BaseURL: upstream.URL, ReceiveMode: "poll", Session: []byte(`{"wxid":"wxid_self"}`)})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(api.Disconnect)
			if err := api.Connect(t.Context()); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			var message *remoteapi.InboundMessage
			for message == nil {
				select {
				case ev := <-api.Events():
					if ev.Error != nil {
						t.Fatal(ev.Error)
					}
					message = ev.Message
				case <-ctx.Done():
					t.Fatal("polling emitted no location message")
				}
			}
			// The one-message batch is consumed; stop before bridging so no
			// extra poll is needed while the Matrix HTTP boundary is exercised.
			api.Disconnect()
			if string(message.Chat) != wantChat || message.Sender != "wxid_self" {
				t.Fatalf("wrong history conversation/sender: %s / %s", message.Chat, message.Sender)
			}
			content := bridgeLocationMessageToMatrix(t, api, message)
			wantType := event.MsgLocation
			if tc.geo == "" {
				wantType = event.MsgText
			}
			if content.MsgType != wantType || content.GeoURI != tc.geo || content.Body != tc.body {
				t.Fatalf("wrong history content: type=%s geo=%q body=%q", content.MsgType, content.GeoURI, content.Body)
			}
		})
	}
}

func TestLocationGroupCallbackToMatrix(t *testing.T) {
	t.Run("sender from prefix", func(t *testing.T) { checkLocationGroupCallback(t, "", "wxid_self", true) })
	t.Run("sender already supplied", func(t *testing.T) { checkLocationGroupCallback(t, "wxid_self", "wxid_self", true) })
	t.Run("mismatch does not override sender", func(t *testing.T) { checkLocationGroupCallback(t, "wxid_self", "wxid_other", false) })
}

func checkLocationGroupCallback(t *testing.T, sender, prefix string, wantLocation bool) {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected upstream request: %s", r.URL.Path)
		http.Error(w, "unexpected request", http.StatusNotFound)
	}))
	t.Cleanup(upstream.Close)
	api, err := (wechat.Factory{}).New(remoteapi.Config{BaseURL: upstream.URL, ReceiveMode: "push", Session: []byte(`{"wxid":"wxid_self"}`)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(api.Disconnect)
	payload, err := json.Marshal(map[string]any{"AddMsgs": []any{map[string]any{
		"MsgId": 123, "NewMsgId": 456, "MsgType": 48, "SenderUserName": sender,
		"Content": map[string]string{"Str": prefix + ":\n" + locationXMLFixture}, "ContentText": "[位置]",
		"FromUserName": map[string]string{"Str": "group@chatroom"}, "ToUserName": "wxid_self", "CreateTime": 1710000000,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := api.(remoteapi.WebhookReceiver).HandleWebhook(t.Context(), payload); err != nil {
		t.Fatal(err)
	}
	select {
	case received := <-api.Events():
		if received.Message == nil || received.Message.Chat != "group@chatroom" || received.Message.Sender != "wxid_self" {
			t.Fatalf("wrong group routing: %+v", received.Message)
		}
		content := bridgeLocationMessageToMatrix(t, api, received.Message)
		if !wantLocation {
			if content.MsgType != event.MsgText || content.GeoURI != "" || content.Body != "[位置]" {
				t.Fatalf("mismatched envelope created a map or leaked XML: %+v", content)
			}
			return
		}
		if content.MsgType != event.MsgLocation || content.GeoURI != "geo:31.234567,121.456789" || content.Body != "示例餐饮店\n示例省示例市示例区示例大道166号示例培训大楼第一层104-02室" {
			t.Fatalf("group location lost its full content: %+v", content)
		}
	default:
		t.Fatal("group callback emitted no event")
	}
}
