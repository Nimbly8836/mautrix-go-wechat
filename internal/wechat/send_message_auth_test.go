package wechat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

func TestSendMessageRetriesTransientLoggedOutWithoutCallingLoginRefresh(t *testing.T) {
	oldDelays := transientSendRetryDelays
	transientSendRetryDelays = []time.Duration{0}
	defer func() { transientSendRetryDelays = oldDelays }()

	var sendCalls int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Msg/SendTxt":
			call := atomic.AddInt32(&sendCalls, 1)
			var req sendTxtReq
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode SendTxt request: %v", err)
			}
			if req.Wxid != "wxid_self" || req.ToWxid != "wxid_friend" || req.Content != "hello" {
				t.Fatalf("unexpected SendTxt request: %#v", req)
			}
			if call == 1 {
				_, _ = w.Write([]byte(`{"Code":-13,"Success":false,"Message":"用户可能退出"}`))
				return
			}
			_, _ = w.Write([]byte(`{"Code":0,"Success":true,"Data":{"NewMsgId":456}}`))
		case "/Login/LoginTwiceAutoAuth", "/Login/Newinit", "/Login/AutoHeartBeat":
			t.Fatalf("SendMessage must not call login refresh endpoint %s; upstream handles automatic twice-login", r.URL.Path)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	c := &Client{t: newTransport(server.URL), wxid: "wxid_self"}
	msgID, err := c.SendMessage(context.Background(), "wxid_friend", remoteapi.OutgoingMessage{Type: remoteapi.MsgText, Text: "hello"})
	if err != nil {
		t.Fatalf("SendMessage returned error: %v", err)
	}
	if msgID != "456" {
		t.Fatalf("unexpected message ID after retry: %q", msgID)
	}
	if got := atomic.LoadInt32(&sendCalls); got != 2 {
		t.Fatalf("expected SendTxt to be retried once, got %d calls", got)
	}
}
