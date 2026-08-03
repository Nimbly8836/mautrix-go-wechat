package matrixas

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseBridgeCommandUsesWxPrefix(t *testing.T) {
	cmd, rest, ok := parseBridgeCommand("!wx portal-mode auto", "!wx")
	if !ok || cmd != "portal-mode" || rest != "auto" {
		t.Fatalf("expected !wx command, got cmd=%q rest=%q ok=%v", cmd, rest, ok)
	}
}

func TestParseBridgeCommandDefaultsToHelp(t *testing.T) {
	cmd, rest, ok := parseBridgeCommand("!wx", "!wx")
	if !ok || cmd != "help" || rest != "" {
		t.Fatalf("expected !wx to map to help, got cmd=%q rest=%q ok=%v", cmd, rest, ok)
	}
}

func TestParseBridgeCommandIgnoresOldBangPrefix(t *testing.T) {
	_, _, ok := parseBridgeCommand("!help", "!wx")
	if ok {
		t.Fatalf("expected !help to be ignored; commands must use !wx")
	}
}

func TestParseBridgeCommandKeepsBareCompatibility(t *testing.T) {
	cmd, rest, ok := parseBridgeCommand("status now", "!wx")
	if !ok || cmd != "status" || rest != "now" {
		t.Fatalf("expected bare command compatibility, got cmd=%q rest=%q ok=%v", cmd, rest, ok)
	}
}

func TestParseBridgeCommandRelogin(t *testing.T) {
	cmd, rest, ok := parseBridgeCommand("!wx relogin wxid_me", "!wx")
	if !ok || cmd != "relogin" || rest != "wxid_me" {
		t.Fatalf("expected relogin command, got cmd=%q rest=%q ok=%v", cmd, rest, ok)
	}
}

func TestSpaceCommandDefaultNoticeIncludesTransitionStatus(t *testing.T) {
	for _, test := range []struct {
		name       string
		configure  func(*Server)
		wantStatus string
	}{
		{
			name:       "success",
			configure:  func(s *Server) { s.rawImagePackState = newFakeRawImagePackState() },
			wantStatus: "Built-in emoji pack: skipped (0/0 rooms); personal account data: skipped",
		},
		{
			name: "failure",
			configure: func(s *Server) {
				state := newFakeRawImagePackState()
				state.saveErrors = map[int]error{1: errors.New("stable pack failed")}
				s.rawImagePackState = state
				s.builtinEmojiStore = &builtinEmojiStore{data: builtinEmojiTestData(builtinEmojiRecord{Shortcode: "wechat_smile", MXC: "mxc://example.org/smile"})}
			},
			wantStatus: "Built-in emoji pack: failed (0/1 rooms); personal account data: skipped",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			accounts := newAccountStore(filepath.Join(t.TempDir(), "accounts.json"))
			account := savedAccount{ID: "wxid_self"}
			if test.name == "success" {
				account.SpaceRoom = "!space:example.org"
				account.SpaceFolders = builtinEmojiTestFolders("command")
			}
			if err := accounts.put(account); err != nil {
				t.Fatal(err)
			}
			s, matrix := newSpacePersistenceTestServerWithRecorder(t, accounts)
			s.api = portalCreationAPI{}
			test.configure(s)

			s.handleCommand(context.Background(), "!management:example.org", "@owner:example.org", "!wx space")

			notices := matrix.noticeBodies()
			if len(notices) == 0 {
				t.Fatal("space command sent no notice")
			}
			if body := notices[len(notices)-1]; !strings.Contains(body, test.wantStatus) {
				t.Fatalf("space command notice = %q, want transition status %q", body, test.wantStatus)
			}
		})
	}
}

func TestParsePortalCommandRequiresWxPrefix(t *testing.T) {
	cmd, rest, ok := parsePortalCommand("!wx sync", "!wx")
	if !ok || cmd != "sync" || rest != "" {
		t.Fatalf("expected portal !wx command, got cmd=%q rest=%q ok=%v", cmd, rest, ok)
	}
	_, _, ok = parsePortalCommand("!sync", "!wx")
	if ok {
		t.Fatalf("expected !sync to be sent to WeChat, not handled locally")
	}
	_, _, ok = parsePortalCommand("help", "!wx")
	if ok {
		t.Fatalf("expected bare help to be sent to WeChat, not handled locally")
	}
}
