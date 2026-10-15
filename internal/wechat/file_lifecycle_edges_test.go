package wechat

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFileExplicitInvalidHintDoesNotEnableNameHeuristics(t *testing.T) {
	for _, hint := range []string{"<overwrite_newmsgid>bad</overwrite_newmsgid>", "<overwrite_newmsgid>1</overwrite_newmsgid><overwrite_newmsgid>2</overwrite_newmsgid>", "<overwrite_newmsgid>0</overwrite_newmsgid>"} {
		c := &Client{wxid: "wxid_self"}
		m := convertDeferredFile(t, c, t.Context(), false, lifecycleFileXML(6, "9", hint, ""), "wxid_friend", "wxid_friend", "wxid_self", false)
		if m.FileTransfer == nil || m.FileTransfer.Replaces != "" || m.FileTransfer.TargetKey != "" {
			t.Fatal("invalid explicit target was lost or guessed")
		}
	}
}

func TestFileCDNInFlightCancellationAndAccountFence(t *testing.T) {
	for _, mode := range []string{"cancel", "account-change"} {
		t.Run(mode, func(t *testing.T) {
			started := make(chan struct{})
			release := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				close(started)
				select {
				case <-r.Context().Done():
					return
				case <-release:
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"Success":true,"Code":0,"Data":{"Image":"emlwLWJ5dGVz"}}`)
			}))
			defer srv.Close()
			c := &Client{wxid: "wxid_self", t: newTransport(srv.URL)}
			m := convertDeferredFile(t, c, t.Context(), false, lifecycleFileXML(6, "9", "", ""), "wxid_friend", "wxid_friend", "wxid_self", false)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				media, err := m.FetchMedia(ctx)
				if media != nil {
					err = errors.New("unexpected published media")
				}
				done <- err
			}()
			<-started
			if mode == "cancel" {
				cancel()
			} else {
				c.mu.Lock()
				c.wxid = "different-account"
				c.mu.Unlock()
			}
			close(release)
			err := <-done
			if err == nil {
				t.Fatal("unsafe in-flight result accepted")
			}
			if mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
			if mode == "account-change" && !strings.Contains(err.Error(), "account") {
				t.Fatalf("account change lost: %v", err)
			}
		})
	}
}
