package matrixas

import (
	"testing"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

func TestApplyCurrentAccountSessionFallbackUsesSavedCurrentAccountWhenSessionMissing(t *testing.T) {
	accounts := &accountStore{data: accountStoreData{Current: "wxid_me", Accounts: map[string]savedAccount{
		"wxid_me": {ID: "wxid_me", Session: []byte(`{"wxid":"wxid_me","sync_key":"saved"}`)},
	}}}

	cfg := applyCurrentAccountSessionFallback(remoteapi.Config{}, accounts)
	if string(cfg.Session) != `{"wxid":"wxid_me","sync_key":"saved"}` {
		t.Fatalf("expected current saved account session fallback, got %q", cfg.Session)
	}
}

func TestApplyCurrentAccountSessionFallbackKeepsExplicitSession(t *testing.T) {
	accounts := &accountStore{data: accountStoreData{Current: "wxid_me", Accounts: map[string]savedAccount{
		"wxid_me": {ID: "wxid_me", Session: []byte(`{"wxid":"wxid_me"}`)},
	}}}

	cfg := applyCurrentAccountSessionFallback(remoteapi.Config{Session: []byte(`{"wxid":"explicit"}`)}, accounts)
	if string(cfg.Session) != `{"wxid":"explicit"}` {
		t.Fatalf("expected explicit session to be kept, got %q", cfg.Session)
	}
}
