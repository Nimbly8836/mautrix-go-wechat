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
	"time"

	"github.com/leat/mautrix-go-wechat/internal/connector"
	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"maunium.net/go/mautrix/event"
)

func TestWechatRelationsWaitForOriginalMatrixAcknowledgement(t *testing.T) {
	for _, tc := range []struct {
		kind        int
		cancelEarly bool
	}{{49, false}, {10002, false}, {49, true}, {10002, true}} {
		t.Run(fmt.Sprintf("%d/cancel_%t", tc.kind, tc.cancelEarly), func(t *testing.T) {
			kind := tc.kind
			b := newMessageRelationBridge(t)
			otherPortal, _ := b.server.portals.get("group@chatroom")
			otherPortal.ChatID, otherPortal.RoomID = "other@chatroom", "!other:example.com"
			b.server.portals.data.Portals[string(otherPortal.ChatID)] = otherPortal
			entered, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			related := make(chan event.MessageEventContent, 4)
			redacted := make(chan string, 4)
			otherSeen := make(chan struct{}, 1)
			hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/send/m.room.message/"):
					var content event.MessageEventContent
					if err := json.NewDecoder(r.Body).Decode(&content); err != nil {
						t.Error(err)
					}
					if content.Body == "阻塞的原消息" {
						close(entered)
						select {
						case <-release:
						case <-r.Context().Done():
							return
						}
						_, _ = w.Write([]byte(`{"event_id":"$original"}`))
					} else if content.Body == "另一群的普通消息" {
						otherSeen <- struct{}{}
						_, _ = w.Write([]byte(`{"event_id":"$other"}`))
					} else {
						related <- content
						_, _ = w.Write([]byte(`{"event_id":"$related"}`))
					}
				case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/redact/"):
					redacted <- strings.Split(strings.Split(r.URL.Path, "/redact/")[1], "/")[0]
					_, _ = w.Write([]byte(`{"event_id":"$redaction"}`))
				case strings.HasSuffix(r.URL.Path, "/register"):
					_, _ = w.Write([]byte(`{"user_id":"@wechatbot:example.com"}`))
				case strings.Contains(r.URL.Path, "/join"):
					_, _ = w.Write([]byte(`{"room_id":"!portal:example.com"}`))
				default:
					t.Errorf("unexpected Matrix request: %s", r.URL.Path)
					http.Error(w, "unexpected", http.StatusNotFound)
				}
			}))
			t.Cleanup(hs.Close)
			if err := b.server.as.SetHomeserverURL(hs.URL); err != nil {
				t.Fatal(err)
			}
			conn, err := connector.New("wechat", remoteapi.Config{BaseURL: b.upstreamURL, ReceiveMode: "push", Session: []byte(`{"wxid":"wxid_self"}`)}, b.server)
			if err != nil {
				t.Fatal(err)
			}
			b.server.api = conn.API()
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			consumed := false
			go func() { done <- conn.Run(ctx) }()
			defer func() {
				unblock()
				cancel()
				if consumed {
					return
				}
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Error("connector did not stop after cancellation")
				}
			}()
			push := func(chat string, msgID, newID int, msgType int, text string) {
				payload, err := json.Marshal(map[string]any{"MsgID": msgID, "NewMsgID": newID, "MsgType": msgType, "Content": text, "Talker": chat, "SenderUserName": "wxid_self", "IsGroup": true, "IsSendBySelf": true, "CreateTime": 1710000000})
				if err != nil {
					t.Fatal(err)
				}
				if err := conn.API().(remoteapi.WebhookReceiver).HandleWebhook(ctx, payload); err != nil {
					t.Fatal(err)
				}
			}
			push("group@chatroom", 123, 456, 1, "阻塞的原消息")
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("original Matrix request did not start")
			}
			push("group@chatroom", 124, 789, kind, relationXML(kind, "456", "group@chatroom"))
			push("other@chatroom", 125, 790, 1, "另一群的普通消息")
			select {
			case <-otherSeen:
			case <-time.After(time.Second):
				t.Fatal("related delivery blocked another chat")
			}
			select {
			case premature := <-related:
				t.Fatalf("related message escaped before original acknowledgement: %+v", premature)
			case <-time.After(150 * time.Millisecond):
			}
			if tc.cancelEarly {
				cancel()
				select {
				case <-done:
					consumed = true
				case <-time.After(time.Second):
					t.Fatal("cancellation waited for original message")
				}
				return
			}
			unblock()
			select {
			case content := <-related:
				if content.RelatesTo == nil || content.RelatesTo.GetReplyTo() != "$original" {
					t.Fatalf("related message lost its acknowledged target: %+v", content)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("related Matrix message did not arrive after original acknowledgement")
			}
			if kind == 10002 {
				select {
				case target := <-redacted:
					if target != "$original" {
						t.Fatalf("wrong recall target: %s", target)
					}
				case <-time.After(time.Second):
					t.Fatal("recall did not redact original")
				}
			}
		})
	}
}
