package matrixas

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"github.com/leat/mautrix-go-wechat/internal/wechat"
	"maunium.net/go/mautrix/event"
)

const locationXMLFixture = `<msg>
<location x="31.234567" y="121.456789" scale="15" label="示例省示例市示例区示例大道166号示例培训大楼第一层104-02室" maptype="" poiname="示例餐饮店" poiid="synthetic-poi" buildingId="" floorName="" fromusername="wxid_self" />
</msg>`

func TestLocationCallbackToMatrixPreservesFullAddress(t *testing.T) {
	content := bridgeLocationCallback(t, locationXMLFixture, locationXMLFixture, "[位置]")
	if content.MsgType != event.MsgLocation || content.GeoURI != "geo:31.234567,121.456789" {
		t.Fatalf("incomplete Matrix location: msgtype=%s geo_uri=%q body=%q", content.MsgType, content.GeoURI, content.Body)
	}
	const wantBody = "示例餐饮店\n示例省示例市示例区示例大道166号示例培训大楼第一层104-02室"
	if content.Body != wantBody {
		t.Fatalf("location name/address changed or truncated: got %q want %q", content.Body, wantBody)
	}
}

func TestLocationCallbackUsesOriginalXMLNotRenderedSummary(t *testing.T) {
	content := bridgeLocationCallback(t, locationXMLFixture, "[位置]", "发送者 : [位置]")
	if content.MsgType != event.MsgLocation || content.GeoURI != "geo:31.234567,121.456789" || content.Body != "示例餐饮店\n示例省示例市示例区示例大道166号示例培训大楼第一层104-02室" {
		t.Fatalf("rendered summary replaced the original location: %+v", content)
	}
}

// Exercise the real webhook, backend event stream, and Matrix HTTP boundary.
func bridgeLocationCallback(t *testing.T, raw, rendered, push string) event.MessageEventContent {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a location callback should need no upstream request: %s %s", r.Method, r.URL.Path)
		http.Error(w, "unexpected request", http.StatusNotFound)
	}))
	t.Cleanup(upstream.Close)
	api, err := (wechat.Factory{}).New(remoteapi.Config{BaseURL: upstream.URL, ReceiveMode: "push", Session: []byte(`{"wxid":"wxid_self"}`)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(api.Disconnect)
	payload, err := json.Marshal(map[string]any{
		"MsgID": 123, "NewMsgID": 456, "MsgType": 48,
		"Content": raw, "ContentText": rendered, "PushContent": push,
		"Talker": "wxid_friend", "SenderUserName": "wxid_self", "IsSendBySelf": true,
		"FromUserName": "wxid_self", "ToUserName": "wxid_friend", "CreateTime": 1710000000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := api.(remoteapi.WebhookReceiver).HandleWebhook(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
	select {
	case received := <-api.Events():
		if received.Message == nil {
			t.Fatalf("location callback did not produce a message: %+v", received)
		}
		return bridgeLocationMessageToMatrix(t, api, received.Message)
	default:
		t.Fatal("location callback emitted no event")
		return event.MessageEventContent{}
	}
}

func bridgeLocationMessageToMatrix(t *testing.T, api remoteapi.PuppetAPI, message *remoteapi.InboundMessage) event.MessageEventContent {
	t.Helper()
	s, rec := newPortalMessageTestServer(t, &portalMessageAPI{})
	s.api = api
	rec.ChatID = message.Chat
	s.portals.data.Portals = map[string]portalRecord{string(rec.ChatID): rec}
	messages := make(chan event.MessageEventContent, 2)
	homeserver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || !strings.Contains(r.URL.Path, "/send/m.room.message/") {
			t.Errorf("unexpected Matrix request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		var content event.MessageEventContent
		if err := json.NewDecoder(r.Body).Decode(&content); err != nil {
			t.Error(err)
		}
		messages <- content
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"event_id":"$location"}`))
	}))
	t.Cleanup(homeserver.Close)
	if err := s.as.SetHomeserverURL(homeserver.URL); err != nil {
		t.Fatal(err)
	}
	s.OnMessage(t.Context(), message)
	select {
	case content := <-messages:
		return content
	default:
		t.Fatal("location did not send a Matrix event")
		return event.MessageEventContent{}
	}
}
