package wechat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

// These envelopes follow fk-zhang's CheckUuid / CheckSecManualAuth return
// shapes. Authentication results are NOT LoginQRCodeNotify objects: they have
// no top-level status, and fresh logins put the account in acctSectResp.
const qrManualAuthSuccess = `{"Code":0,"Success":true,"Message":"登录成功","Data":{"baseResponse":{"ret":0},"unifyAuthSectFlag":15,"acctSectResp":{"userName":"wxid_me","nickName":"me","status":0}}}`
const qrCachedAuthSuccess = `{"Code":0,"Success":true,"Message":"登录成功","Data":{"Wxid":"wxid_me","NickName":"me","AlreadyLoggedIn":true}}`
const qrFlatAuthSuccess = `{"Code":0,"Success":true,"Data":{"Status":2,"Wxid":"wxid_me"}}`

func TestQRLoginUpstreamResponses(t *testing.T) {
	defer withQRSettings(time.Millisecond, 3, time.Second)()
	tests := []struct {
		name      string
		responses []string
	}{
		{
			name: "manual auth after scan",
			responses: []string{
				`{"Code":0,"Success":true,"Data":{"status":0}}`,
				`{"Code":0,"Success":true,"Data":{"status":1,"userName":"wxid_me","nickName":"me"}}`,
				qrManualAuthSuccess,
			},
		},
		{name: "already logged in", responses: []string{qrCachedAuthSuccess}},
		{name: "legacy flat confirmed", responses: []string{qrFlatAuthSuccess}},
		{
			name:      "legacy username alias",
			responses: []string{`{"Code":0,"Success":true,"Data":{"status":2,"userName":"wxid_me"}}`},
		},
		{
			name:      "explicit waiting with wxid is not success",
			responses: []string{`{"Code":0,"Success":true,"Data":{"Status":0,"Wxid":"wxid_me"}}`, qrFlatAuthSuccess},
		},
		{
			name:      "scanned with wxid is not success",
			responses: []string{`{"Code":0,"Success":true,"Data":{"Status":1,"Wxid":"wxid_me"}}`, qrFlatAuthSuccess},
		},
		{
			name:      "expired status with wxid is not success",
			responses: []string{`{"Code":0,"Success":true,"Data":{"Status":3,"Wxid":"wxid_me"}}`, qrFlatAuthSuccess},
		},
		{
			name:      "wxid alone is not success",
			responses: []string{`{"Code":0,"Success":true,"Data":{"Wxid":"wxid_me"}}`, qrFlatAuthSuccess},
		},
		{
			name:      "nested rejection is not success",
			responses: []string{`{"Code":0,"Success":true,"Data":{"baseResponse":{"ret":-13},"unifyAuthSectFlag":15,"acctSectResp":{"userName":"wxid_me"}}}`, qrFlatAuthSuccess},
		},
		{
			name:      "missing nested ret is not zero",
			responses: []string{`{"Code":0,"Success":true,"Data":{"baseResponse":{},"unifyAuthSectFlag":15,"acctSectResp":{"userName":"wxid_me"}}}`, qrFlatAuthSuccess},
		},
		{
			name:      "missing auth flag is not success",
			responses: []string{`{"Code":0,"Success":true,"Data":{"baseResponse":{"ret":0},"acctSectResp":{"userName":"wxid_me"}}}`, qrFlatAuthSuccess},
		},
		{
			name:      "missing account is not success",
			responses: []string{`{"Code":0,"Success":true,"Data":{"baseResponse":{"ret":0},"unifyAuthSectFlag":15}}`, qrFlatAuthSuccess},
		},
		{
			name:      "cached marker without account is not success",
			responses: []string{`{"Code":0,"Success":true,"Data":{"AlreadyLoggedIn":true}}`, qrFlatAuthSuccess},
		},
		{
			name:      "missing data is not success",
			responses: []string{`{"Code":0,"Success":true}`, qrFlatAuthSuccess},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var checks, twiceCalls, initCalls, heartCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/Login/LoginGetQR":
					_, _ = w.Write([]byte(`{"Code":1,"Success":true,"Data":{"Uuid":"uuid-1","QrBase64":"data:qr"}}`))
					return
				case "/Login/LoginCheckQR":
					if got := r.URL.Query().Get("uuid"); got != "uuid-1" {
						t.Errorf("check uuid = %q", got)
					}
					index := int(checks.Add(1)) - 1
					if index < len(test.responses) {
						_, _ = w.Write([]byte(test.responses[index]))
					} else {
						// Stop an erroneous extra poll immediately instead of waiting
						// for a real QR expiry, so this repro stays fast and bounded.
						_, _ = w.Write([]byte(`{"Code":-8,"Success":false,"Message":"polled past confirmed response"}`))
					}
					return
				case "/Login/LoginTwiceAutoAuth":
					twiceCalls.Add(1)
				case "/Login/Newinit":
					initCalls.Add(1)
				case "/Login/AutoHeartBeat":
					heartCalls.Add(1)
				default:
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				if got := r.URL.Query().Get("wxid"); got != "wxid_me" {
					t.Errorf("hook wxid = %q", got)
				}
				_, _ = w.Write([]byte(`{"Code":0,"Success":true,"Data":{}}`))
			}))
			defer server.Close()

			client := &Client{t: newTransport(server.URL)}
			flow, _ := findQRFlow("ipad")
			login := &qrLogin{c: client, flow: flow}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if _, err := login.Start(ctx); err != nil {
				t.Fatalf("Start: %v", err)
			}
			step, err := login.Wait(ctx)
			if err != nil {
				t.Fatalf("Wait: %v", err)
			}
			if step.Type != remoteapi.LoginComplete || step.Self != "wxid_me" {
				t.Fatalf("expected login completion at confirmed response, got %#v", step)
			}
			if got := int(checks.Load()); got != len(test.responses) {
				t.Errorf("QR checks = %d, want %d (must neither complete early nor keep polling)", got, len(test.responses))
			}
			if got := client.selfWxid(); got != "wxid_me" {
				t.Errorf("stored account = %q", got)
			}
			if twiceCalls.Load() != 1 || initCalls.Load() != 1 || heartCalls.Load() != 1 {
				t.Errorf("hook calls twice/init/heartbeat = %d/%d/%d, want 1/1/1", twiceCalls.Load(), initCalls.Load(), heartCalls.Load())
			}
		})
	}
}
