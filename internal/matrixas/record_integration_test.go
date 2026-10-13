package matrixas

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"github.com/leat/mautrix-go-wechat/internal/wechat"
	"maunium.net/go/mautrix/event"
)

// Replays the user's two-item shape through the real backend, event model,
// Matrix renderer and SDK. Only the CDN API and homeserver are substituted.
func TestMergedRecordWebhookToNativeMatrixVideo(t *testing.T) {
	video, err := os.ReadFile("../wechat/testdata/record-video.mp4")
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Tools/CdnDownloadRecordItem" {
			t.Errorf("unexpected request: %s", r.URL.Path)
			http.Error(w, "unexpected", 500)
			return
		}
		var request struct {
			Wxid, CdnDataUrl, CdnDataKey, DataId string
			DataSize, IsThumb, DataType          int64
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Wxid != "wxid_self" || request.CdnDataUrl != "synthetic-cdn" || request.CdnDataKey != "synthetic-key" || request.DataId != "video-id" || request.DataSize != 12 || request.IsThumb != 0 || request.DataType != 4 {
			t.Errorf("request=%+v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"Success": true, "Data": map[string]string{"Image": base64.StdEncoding.EncodeToString(video)}})
	}))
	defer upstream.Close()
	api, err := (wechat.Factory{}).New(remoteapi.Config{BaseURL: upstream.URL, ReceiveMode: "push", Session: []byte(`{"wxid":"wxid_self"}`)})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Disconnect()
	h := newRecordTestMatrix(t, nil)
	h.server.api = api
	raw := `<?xml version="1.0"?><msg><appmsg><title>Hayek与undefined的聊天记录</title><des>Hayek: 没有视频
Hayek: [视频]</des><type>19</type><url>https://support.weixin.qq.com/unsupported</url><recorditem><![CDATA[<recordinfo><title>Hayek与undefined的聊天记录</title><datalist count="2"><dataitem datatype="1"><datadesc>没有视频</datadesc><sourcename>Hayek</sourcename><sourcetime>2026-10-13&#x20;10:16:24</sourcetime><fromnewmsgid>8059886386067371644</fromnewmsgid></dataitem><dataitem datatype="4" dataid="video-id"><datadesc>[视频]</datadesc><cdndataurl>synthetic-cdn</cdndataurl><cdndatakey>synthetic-key</cdndatakey><datasize>12</datasize><datafmt>mp4</datafmt><duration>2</duration><sourcename>Hayek</sourcename><sourcetime>2026-10-13&#x20;10:16:38</sourcetime><fromnewmsgid>9106390367682012849</fromnewmsgid></dataitem></datalist></recordinfo>]]></recorditem></appmsg><extcommoninfo><media_expire_at>1793067409</media_expire_at></extcommoninfo></msg>`
	payload, _ := json.Marshal(map[string]any{"MsgType": 49, "MsgID": 123, "NewMsgID": 456, "Talker": h.portal.ChatID, "SenderUserName": "wxid_self", "Content": raw, "ContentText": "[聊天记录]", "CreateTime": 1791857809})
	if err = api.(remoteapi.WebhookReceiver).HandleWebhook(t.Context(), payload); err != nil {
		t.Fatal(err)
	}
	select {
	case received := <-api.Events():
		if received.Message == nil {
			t.Fatal("no converted message")
		}
		h.server.OnMessage(t.Context(), received.Message)
	default:
		t.Fatal("webhook emitted no event")
	}
	messages, uploads, _ := h.snapshot()
	if len(messages) != 2 || len(uploads) != 1 {
		t.Fatalf("messages=%+v uploads=%+v", messages, uploads)
	}
	root, attachment := messages[0], messages[1]
	for _, text := range []string{"Hayek", "没有视频", "2026-10-13 10:16:24", "2026-10-13 10:16:38", "[视频]"} {
		if !strings.Contains(root.Content.Body, text) {
			t.Errorf("lost %q", text)
		}
	}
	if root.Content.MsgType != event.MsgText || attachment.Content.MsgType != event.MsgVideo || attachment.Content.URL == "" || attachment.Content.RelatesTo.GetReplyTo() != root.ID || uploads[0].body != string(video) {
		t.Fatalf("not native media: %+v", messages)
	}
	if strings.Contains(root.Content.Body, "support.weixin.qq.com") || strings.Contains(root.Content.Body, "synthetic-key") {
		t.Fatal("leaked source transport details")
	}
	ref := h.server.getMessageReference("wxid_self", h.portal, "456", "new:456")
	if ref.EventID != root.ID || len(ref.RelatedEventIDs) != 1 || ref.RelatedEventIDs[0] != attachment.ID {
		t.Fatalf("outer record mapping lost: %+v", ref)
	}
}
