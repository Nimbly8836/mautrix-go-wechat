package matrixas

import "testing"

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
