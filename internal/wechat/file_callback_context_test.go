package wechat

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

// The real upstream callback times out after five seconds. Attachment I/O must
// not delay its acknowledgement or inherit the lifetime of that HTTP request.
func TestFileCallbackAcknowledgesBeforeDownloadCompletes(t *testing.T) {
	releaseDownload := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Tools/DownloadFile" {
			t.Errorf("unexpected path %s", r.URL.Path)
			http.Error(w, "unexpected", 500)
			return
		}
		select {
		case <-releaseDownload:
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = w.Write([]byte("%PDF-test"))
		case <-r.Context().Done():
		}
	}))
	defer upstream.Close()
	c := &Client{t: newTransport(upstream.URL), wxid: "wxid_self", events: make(chan remoteapi.Event, 4), done: make(chan struct{})}
	callback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err == nil {
			err = c.HandleWebhook(r.Context(), body)
		}
		if err != nil {
			http.Error(w, "callback failed", 400)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer callback.Close()
	defer close(releaseDownload)
	raw := `<msg><appmsg appid=""><title>document.pdf</title><type>6</type><appattach><totallen>9</totallen><attachid>synthetic-attachment</attachid><fileext>pdf</fileext></appattach></appmsg><fromusername>wxid_friend</fromusername></msg>`
	payload, _ := json.Marshal(map[string]any{"MsgType": 49, "MsgID": 1, "NewMsgID": 2, "Talker": "wxid_friend", "SenderUserName": "wxid_friend", "Content": raw, "ContentText": raw})
	caller := &http.Client{Timeout: 250 * time.Millisecond}
	response, err := caller.Post(callback.URL, "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("callback waited for attachment I/O and was canceled: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("callback status=%d", response.StatusCode)
	}
}
