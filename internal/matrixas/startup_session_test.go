package matrixas

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

func TestApplyStartupSessionUsesCurrentAccountWhenSessionMissing(t *testing.T) {
	accounts := &accountStore{data: accountStoreData{Current: "wxid_me", Accounts: map[string]savedAccount{
		"wxid_me": {ID: "wxid_me", Session: []byte(`{"wxid":"wxid_me","sync_key":"saved"}`)},
	}}}

	cfg := applyStartupSession(remoteapi.Config{}, accounts, filepath.Join(t.TempDir(), "legacy.json"))
	if string(cfg.Session) != `{"wxid":"wxid_me","sync_key":"saved"}` {
		t.Fatalf("expected current saved account session, got %q", cfg.Session)
	}
}

func TestApplyStartupSessionCurrentAccountOverridesStaleLegacySession(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "session.json")
	if err := os.WriteFile(legacy, []byte(`{"wxid":"stale"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	accounts := &accountStore{data: accountStoreData{Current: "wxid_me", Accounts: map[string]savedAccount{
		"wxid_me": {ID: "wxid_me", Session: []byte(`{"wxid":"wxid_me"}`)},
	}}}

	cfg := applyStartupSession(remoteapi.Config{}, accounts, legacy)
	if string(cfg.Session) != `{"wxid":"wxid_me"}` {
		t.Fatalf("stale legacy session overrode account store: %q", cfg.Session)
	}
}

func TestApplyStartupSessionReadsLegacyOnlyWithoutCurrentAccount(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "session.json")
	if err := os.WriteFile(legacy, []byte(`{"wxid":"legacy"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	accounts := &accountStore{data: accountStoreData{Accounts: map[string]savedAccount{}}}

	cfg := applyStartupSession(remoteapi.Config{}, accounts, legacy)
	if string(cfg.Session) != `{"wxid":"legacy"}` {
		t.Fatalf("expected legacy session fallback, got %q", cfg.Session)
	}
}

func TestApplyStartupSessionKeepsExplicitSessionWithoutCurrentAccount(t *testing.T) {
	accounts := &accountStore{data: accountStoreData{Accounts: map[string]savedAccount{}}}
	cfg := applyStartupSession(remoteapi.Config{Session: []byte(`{"wxid":"explicit"}`)}, accounts, filepath.Join(t.TempDir(), "missing"))
	if string(cfg.Session) != `{"wxid":"explicit"}` {
		t.Fatalf("expected explicit session to be kept, got %q", cfg.Session)
	}
}
