package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateRegistrationRejectsManifestCollisionBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "wechat-builtin.json")
	const original = `{"schema_version":1,"sentinel":"preserve-me"}`
	if err := os.WriteFile(manifest, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.yaml")
	configBody := fmt.Sprintf(`
homeserver:
  address: http://localhost:8008
  domain: example.org
appservice:
  address: http://localhost:29318
  hostname: 127.0.0.1
  port: 29318
  bot_username: wechatbot
network:
  protocol: wechat
  base_url: http://localhost:8080
bridge:
  wechat_builtin_emoji_file: %q
  permissions:
    example.org: user
`, manifest)
	if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}

	err := generateRegistration(configPath, manifest)
	if err == nil || !strings.Contains(err.Error(), "registration path") {
		t.Fatalf("generateRegistration collision error = %v", err)
	}
	after, readErr := os.ReadFile(manifest)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(after) != original {
		t.Fatalf("colliding -g write modified manifest: %q", after)
	}
}

func TestGenerateRegistrationRejectsConfigSymlinkCollisionBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	manifestAlias := filepath.Join(dir, "wechat-builtin.json")
	if err := os.Symlink(configPath, manifestAlias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	configBody := fmt.Sprintf(`
homeserver:
  address: http://localhost:8008
  domain: example.org
appservice:
  address: http://localhost:29318
  hostname: 127.0.0.1
  port: 29318
  bot_username: wechatbot
network:
  protocol: wechat
  base_url: http://localhost:8080
bridge:
  wechat_builtin_emoji_file: %q
  permissions:
    example.org: user
`, manifestAlias)
	if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}
	regPath := filepath.Join(dir, "registration.yaml")

	err := generateRegistration(configPath, regPath)
	if err == nil || !strings.Contains(err.Error(), "config path") {
		t.Fatalf("generateRegistration config collision error = %v", err)
	}
	if _, statErr := os.Stat(regPath); !os.IsNotExist(statErr) {
		t.Fatalf("registration was written before config collision rejection: %v", statErr)
	}
	after, readErr := os.ReadFile(configPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(after) != configBody {
		t.Fatalf("colliding -g write modified config: %q", after)
	}
}
