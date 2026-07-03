package matrixas

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/leat/mautrix-go-wechat/internal/config"
	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

type webhookAPI struct {
	remoteapi.PuppetAPI
	payload string
}

func (a *webhookAPI) HandleWebhook(ctx context.Context, payload []byte) error {
	a.payload = string(payload)
	return nil
}

func TestHandleWebhookPassesPayloadToBackend(t *testing.T) {
	api := &webhookAPI{}
	s := &Server{api: api}
	req := httptest.NewRequest(http.MethodPost, "/wechat/callback", io.NopCloser(strings.NewReader(`{"ok":true}`)))
	resp := httptest.NewRecorder()
	s.handleWebhook(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.Code)
	}
	if api.payload != `{"ok":true}` {
		t.Fatalf("unexpected payload %q", api.payload)
	}
}

func TestHandleWebhookRejectsNonPost(t *testing.T) {
	s := &Server{api: &webhookAPI{}}
	req := httptest.NewRequest(http.MethodGet, "/wechat/callback", nil)
	resp := httptest.NewRecorder()
	s.handleWebhook(resp, req)
	if resp.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status 405, got %d", resp.Code)
	}
}

func TestShouldStartRemoteOnRunStartsPushEvenWhenLoginProbeFails(t *testing.T) {
	s := &Server{cfg: &config.Config{Network: config.Network{Receive: config.Receive{Mode: "push"}}}}
	if !s.shouldStartRemoteOnRun(false) {
		t.Fatalf("push mode must start remote event pump even when login probe fails")
	}
}

func TestShouldStartRemoteOnRunKeepsPollLoginGate(t *testing.T) {
	s := &Server{cfg: &config.Config{Network: config.Network{Receive: config.Receive{Mode: "poll"}}}}
	if s.shouldStartRemoteOnRun(false) {
		t.Fatalf("poll mode should not start remote event pump when login probe fails")
	}
	if !s.shouldStartRemoteOnRun(true) {
		t.Fatalf("poll mode should start remote event pump when logged in")
	}
}
