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

func withQRSettings(poll time.Duration, maxFailures int, fallback time.Duration) func() {
	oldPoll := qrPollInterval
	oldMaxFailures := qrMaxConsecutiveCheckFailures
	oldFallback := qrFallbackTimeout
	qrPollInterval = poll
	qrMaxConsecutiveCheckFailures = maxFailures
	qrFallbackTimeout = fallback
	return func() {
		qrPollInterval = oldPoll
		qrMaxConsecutiveCheckFailures = oldMaxFailures
		qrFallbackTimeout = oldFallback
	}
}

func TestQRLoginStartAcceptsStringExpiredTime(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Login/LoginGetQRx":
			_ = json.NewEncoder(w).Encode(map[string]any{"Code": 0, "Success": true, "Data": map[string]any{"Uuid": "uuid-1", "QrBase64": "data:qr", "ExpiredTime": "120"}})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	c := &Client{t: newTransport(server.URL)}
	login := &qrLogin{c: c, flow: qrFlow{ID: "ipad-x", Name: "iPad QR bypass", Endpoint: "/Login/LoginGetQRx", LoginType: "ipad"}}
	step, err := login.Start(context.Background())
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if step.Type != remoteapi.LoginDisplayAndWait || step.Display == nil || step.Display.Data != "data:qr" {
		t.Fatalf("unexpected start step: %#v", step)
	}
	if ttl := time.Until(login.expiresAt); ttl <= 0 || ttl > 121*time.Second {
		t.Fatalf("unexpected QR expiry deadline: %s", ttl)
	}
}

func TestRefreshLoginUsesCurrentAccountAndEnablesHooks(t *testing.T) {
	var twiceCalls int32
	var heartCalls int32
	var initCalls int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Login/LoginTwiceAutoAuth":
			atomic.AddInt32(&twiceCalls, 1)
			if r.URL.Query().Get("wxid") != "wxid_me" {
				t.Fatalf("unexpected wxid for twice auth: %q", r.URL.Query().Get("wxid"))
			}
		case "/Login/Newinit":
			atomic.AddInt32(&initCalls, 1)
		case "/Login/AutoHeartBeat":
			atomic.AddInt32(&heartCalls, 1)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Code": 0, "Success": true, "Data": map[string]any{}})
	}))
	defer server.Close()

	c := &Client{t: newTransport(server.URL), wxid: "wxid_me"}
	if err := c.RefreshLogin(context.Background(), ""); err != nil {
		t.Fatalf("RefreshLogin failed: %v", err)
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

func TestQRLoginStopsOnFatalCheckError(t *testing.T) {
	defer withQRSettings(time.Millisecond, 3, time.Second)()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Login/LoginGetQR":
			_ = json.NewEncoder(w).Encode(map[string]any{"Code": 0, "Success": true, "Data": map[string]any{"Uuid": "uuid-1", "QrBase64": "data:qr"}})
		case "/Login/LoginCheckQR":
			_ = json.NewEncoder(w).Encode(map[string]any{"Code": -8, "Success": false, "Message": "登录异常"})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	c := &Client{t: newTransport(server.URL)}
	login := &qrLogin{c: c, flow: qrFlow{ID: "ipad", Name: "iPad QR", Endpoint: "/Login/LoginGetQR", LoginType: "ipad"}}
	if _, err := login.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	step, err := login.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait failed: %v", err)
	}
	if step.Type != remoteapi.LoginError || step.Err == nil {
		t.Fatalf("expected fatal QR error step, got %#v", step)
	}
}

func TestQRLoginStopsAfterConsecutiveCheckFailures(t *testing.T) {
	defer withQRSettings(time.Millisecond, 2, time.Second)()
	var checkCalls int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Login/LoginGetQR":
			_ = json.NewEncoder(w).Encode(map[string]any{"Code": 0, "Success": true, "Data": map[string]any{"Uuid": "uuid-1", "QrBase64": "data:qr"}})
		case "/Login/LoginCheckQR":
			atomic.AddInt32(&checkCalls, 1)
			_ = json.NewEncoder(w).Encode(map[string]any{"Code": -9, "Success": false, "Message": "temporary backend error"})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	c := &Client{t: newTransport(server.URL)}
	login := &qrLogin{c: c, flow: qrFlow{ID: "ipad", Name: "iPad QR", Endpoint: "/Login/LoginGetQR", LoginType: "ipad"}}
	if _, err := login.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	step, err := login.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait failed: %v", err)
	}
	if step.Type != remoteapi.LoginError || step.Err == nil {
		t.Fatalf("expected check failure step, got %#v", step)
	}
	if got := atomic.LoadInt32(&checkCalls); got != 2 {
		t.Fatalf("expected polling to stop after 2 failures, got %d calls", got)
	}
}

func TestQRLoginStopsAfterQRCodeExpiry(t *testing.T) {
	defer withQRSettings(10*time.Millisecond, 20, 5*time.Second)()
	var checkCalls int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Login/LoginGetQR":
			_ = json.NewEncoder(w).Encode(map[string]any{"Code": 0, "Success": true, "Data": map[string]any{"Uuid": "uuid-1", "QrBase64": "data:qr", "ExpiredTime": 1}})
		case "/Login/LoginCheckQR":
			atomic.AddInt32(&checkCalls, 1)
			_ = json.NewEncoder(w).Encode(map[string]any{"Code": 0, "Success": true, "Data": map[string]any{"Status": 0}})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	c := &Client{t: newTransport(server.URL)}
	login := &qrLogin{c: c, flow: qrFlow{ID: "ipad", Name: "iPad QR", Endpoint: "/Login/LoginGetQR", LoginType: "ipad"}}
	if _, err := login.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	started := time.Now()
	step, err := login.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait failed: %v", err)
	}
	if step.Type != remoteapi.LoginError || step.Err == nil {
		t.Fatalf("expected QR expiry step, got %#v", step)
	}
	if elapsed := time.Since(started); elapsed > 1500*time.Millisecond {
		t.Fatalf("expected Wait to stop at QR expiry, took %s", elapsed)
	}
	if got := atomic.LoadInt32(&checkCalls); got == 0 {
		t.Fatalf("expected at least one QR check before expiry")
	}
}

func TestQRLoginWaitPollsUntilConfirmedAndEnablesHooks(t *testing.T) {
	defer withQRSettings(time.Millisecond, 3, time.Second)()
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
