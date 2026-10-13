package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestMessageReferenceJournalCannotAliasBridgeState(t *testing.T) {
	for _, kind := range []string{"account", "contact", "session", "manifest", "config", "registration"} {
		t.Run(kind, func(t *testing.T) {
			portal := filepath.Join(t.TempDir(), "portals.json")
			journal := portal + ".messages.jsonl"
			cfg := &Config{Bridge: Bridge{PortalFile: portal}}
			configPath, registrationPath := "", ""
			switch kind {
			case "account":
				cfg.Bridge.AccountFile = journal
			case "contact":
				cfg.Bridge.ContactFile = journal
			case "session":
				cfg.Bridge.SessionFile = journal
			case "manifest":
				cfg.Bridge.WechatBuiltinEmojiFile = journal
			case "config":
				configPath = journal
			case "registration":
				registrationPath = journal
			}
			err := cfg.ValidateBridgePathCollisions(configPath, registrationPath)
			if err == nil || !strings.Contains(err.Error(), "message reference journal collides") {
				t.Fatalf("collision allowed: %v", err)
			}
		})
	}
}
