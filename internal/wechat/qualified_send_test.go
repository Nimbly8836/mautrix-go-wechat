package wechat

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

func TestQualifiedOrdinarySendPreservesLoggedOutRecovery(t *testing.T) {
	oldDelays := transientSendRetryDelays
	transientSendRetryDelays = []time.Duration{0}
	defer func() { transientSendRetryDelays = oldDelays }()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/Msg/SendTxt" {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			_, _ = w.Write([]byte(`{"Success":false,"Code":-13,"Message":"用户可能退出"}`))
			return
		}
		_, _ = w.Write([]byte(`{"Success":true,"Data":{"MsgId":123,"NewMsgId":9007199254740993}}`))
	}))
	defer srv.Close()
	c := &Client{wxid: "wxid_self", t: newTransport(srv.URL)}
	result, err := c.SendMessageWithResult(t.Context(), "wxid_friend", remoteapi.OutgoingMessage{Type: remoteapi.MsgText, Text: "hello"})
	if err != nil || calls != 2 || result.ID != "9007199254740993" || !reflect.DeepEqual(result.Keys, []string{"new:9007199254740993", "msg:123", "id:9007199254740993"}) {
		t.Fatalf("qualified send regression: %+v calls=%d err=%v", result, calls, err)
	}
}
