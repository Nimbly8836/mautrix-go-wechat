package matrixas

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	const wantBody = "示例餐饮店\n示例省示例市示例区示例大道166号示例培训大楼第一层104-02室"
	assertLocationText(t, content, wantBody, "geo:31.234567,121.456789")
}

func TestLocationCallbackUsesOriginalXMLNotRenderedSummary(t *testing.T) {
	content := bridgeLocationCallback(t, locationXMLFixture, "[位置]", "发送者 : [位置]")
	assertLocationText(t, content, "示例餐饮店\n示例省示例市示例区示例大道166号示例培训大楼第一层104-02室", "geo:31.234567,121.456789")
}

func TestLocationCallbackUsesPlainTextForClientCompatibility(t *testing.T) {
	const raw = `<msg><location x="31.234567" y="121.456789" scale="15" label="示例省示例市示例区示例大道166号示例大楼1号楼607-608" maptype="0" poiname="示例事务所" poiid="synthetic-poi" buildingId="" floorName="" poiCategoryTips="生活服务:事务所" poiBusinessHour="周一至周五 09:00-18:00 " poiPhone="00000000000" poiPriceTips="0.0" isFromPoiList="true" adcode="" cityname="" fromusername="wxid_self" /></msg>`
	content := bridgeLocationCallback(t, raw, "[位置]", "发送者 : [位置]")
	const wantBody = "示例事务所\n示例省示例市示例区示例大道166号示例大楼1号楼607-608\n坐标：31.234567,121.456789\n地图：https://www.openstreetmap.org/?mlat=31.234567&mlon=121.456789#map=16/31.234567/121.456789"
	if content.MsgType != event.MsgText || content.Body != wantBody || content.GeoURI != "" || content.FormattedBody != "" {
		t.Fatalf("location must be readable without location-card support: type=%s body=%q geo_uri=%q formatted_body=%q", content.MsgType, content.Body, content.GeoURI, content.FormattedBody)
	}
}

func assertLocationText(t *testing.T, content event.MessageEventContent, description, geoURI string) {
	t.Helper()
	if content.MsgType != event.MsgText || content.GeoURI != "" || content.FormattedBody != "" {
		t.Fatalf("location must use ordinary text: %+v", content)
	}
	if geoURI == "" {
		if content.Body != description {
			t.Fatalf("location without coordinates changed its readable description: got %q want %q", content.Body, description)
		}
		return
	}
	lines := strings.Split(content.Body, "\n")
	if len(lines) < 3 {
		t.Fatalf("location is missing its description, coordinates or map link: %q", content.Body)
	}
	if got := strings.Join(lines[:len(lines)-2], "\n"); got != description {
		t.Fatalf("location description changed or was truncated: got %q want %q", got, description)
	}
	coordinates, ok := strings.CutPrefix(lines[len(lines)-2], "坐标：")
	if !ok || "geo:"+coordinates != geoURI {
		t.Fatalf("displayed coordinates do not match source: %q want %q", coordinates, geoURI)
	}
	link, ok := strings.CutPrefix(lines[len(lines)-1], "地图：")
	parsed, err := url.Parse(link)
	if !ok || err != nil || parsed.Scheme != "https" || parsed.Host != "www.openstreetmap.org" || parsed.Path != "/" {
		t.Fatalf("invalid map link: %q", link)
	}
	query := parsed.Query()
	if len(query) != 2 || "geo:"+query.Get("mlat")+","+query.Get("mlon") != geoURI {
		t.Fatalf("map marker coordinates do not match source: %q want %q", link, geoURI)
	}
	viewport := strings.Split(parsed.Fragment, "/")
	if len(viewport) != 3 || viewport[0] != "map=16" || "geo:"+viewport[1]+","+viewport[2] != geoURI {
		t.Fatalf("map viewport coordinates do not match source: %q want %q", link, geoURI)
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
