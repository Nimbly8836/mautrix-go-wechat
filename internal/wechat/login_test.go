package wechat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

func TestQRLoginWaitPollsUntilConfirmedAndEnablesHooks(t *testing.T) {
	var checkCalls int32
	var twiceCalls int32
	var heartCalls int32
	var initCalls int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Login/LoginGetQR":
			_ = json.NewEncoder(w).Encode(map[string]any{"Code": 0, "Success": true, "Data": map[string]any{"Uuid": "uuid-1", "QrBase64": "data:qr"}})
		case "/Login/LoginCheckQR":
			count := atomic.AddInt32(&checkCalls, 1)
			status := 1
			if count >= 2 {
				status = 2
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"Code": 0, "Success": true, "Data": map[string]any{"Status": status, "Wxid": "wxid_me", "NickName": "me"}})
		case "/Login/LoginTwiceAutoAuth":
			atomic.AddInt32(&twiceCalls, 1)
			_ = json.NewEncoder(w).Encode(map[string]any{"Code": 0, "Success": true, "Data": map[string]any{}})
		case "/Login/Newinit":
			atomic.AddInt32(&initCalls, 1)
			_ = json.NewEncoder(w).Encode(map[string]any{"Code": 0, "Success": true, "Data": map[string]any{}})
		case "/Login/AutoHeartBeat":
			atomic.AddInt32(&heartCalls, 1)
			_ = json.NewEncoder(w).Encode(map[string]any{"Code": 0, "Success": true, "Data": map[string]any{}})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	c := &Client{t: newTransport(server.URL)}
	login := &qrLogin{c: c, flow: qrFlow{ID: "ipad", Name: "iPad QR", Endpoint: "/Login/LoginGetQR", LoginType: "ipad"}}
	step, err := login.Start(context.Background())
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if step.Type != remoteapi.LoginDisplayAndWait || step.Display == nil || step.Display.Data == "" {
		t.Fatalf("unexpected start step: %#v", step)
	}

	step, err = login.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait failed: %v", err)
	}
	if step.Type != remoteapi.LoginComplete || step.Self != "wxid_me" {
		t.Fatalf("unexpected completion step: %#v", step)
	}
	if got := atomic.LoadInt32(&checkCalls); got < 2 {
		t.Fatalf("expected repeated QR polling, got %d calls", got)
	}
	if got := atomic.LoadInt32(&twiceCalls); got != 1 {
		t.Fatalf("expected LoginTwiceAutoAuth once, got %d", got)
	}
	if got := atomic.LoadInt32(&initCalls); got != 1 {
		t.Fatalf("expected Newinit once, got %d", got)
	}
	if got := atomic.LoadInt32(&heartCalls); got != 1 {
		t.Fatalf("expected AutoHeartBeat once, got %d", got)
	}
}
