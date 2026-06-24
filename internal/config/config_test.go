package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const baseConfigYAML = `
homeserver:
  address: http://synapse:8008
  domain: example.com
appservice:
  address: http://mautrix-wechat:29318
  hostname: 0.0.0.0
  port: 29318
  as_token: as
  hs_token: hs
network:
  base_url: http://wechat-api:8080/api
%sbridge:
  permissions:
    example.com: user
`

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestNetworkReceiveDefaultsToPolling(t *testing.T) {
	cfg, err := Load(writeConfig(t, fmt.Sprintf(baseConfigYAML, "")))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Network.Receive.Mode != "poll" {
		t.Fatalf("expected default receive mode poll, got %q", cfg.Network.Receive.Mode)
	}
	if cfg.Network.Receive.WebhookPath != "" {
		t.Fatalf("expected no webhook path in poll mode, got %q", cfg.Network.Receive.WebhookPath)
	}
}

func TestNetworkReceivePushRequiresWebhookPath(t *testing.T) {
	_, err := Load(writeConfig(t, fmt.Sprintf(baseConfigYAML, "  receive:\n    mode: push\n")))
	if err == nil || !strings.Contains(err.Error(), "network.receive.webhook_path is required") {
		t.Fatalf("expected webhook path validation error, got %v", err)
	}
}

func TestBridgeCommandPrefixDefaultsToWx(t *testing.T) {
	cfg, err := Load(writeConfig(t, fmt.Sprintf(baseConfigYAML, "")))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Bridge.CommandPrefix != "!wx" {
		t.Fatalf("expected default command prefix !wx, got %q", cfg.Bridge.CommandPrefix)
	}
}

func TestBridgeCommandPrefixNormalizesBang(t *testing.T) {
	cfg, err := Load(writeConfig(t, fmt.Sprintf(strings.Replace(baseConfigYAML, "bridge:\n", "bridge:\n  command_prefix: wx\n", 1), "")))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Bridge.CommandPrefix != "!wx" {
		t.Fatalf("expected normalized command prefix !wx, got %q", cfg.Bridge.CommandPrefix)
	}
}

func TestNetworkReceivePushNormalizesWebhookPath(t *testing.T) {
	cfg, err := Load(writeConfig(t, fmt.Sprintf(baseConfigYAML, "  receive:\n    mode: push\n    webhook_path: wechat/callback\n")))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Network.Receive.Mode != "push" {
		t.Fatalf("expected push mode, got %q", cfg.Network.Receive.Mode)
	}
	if cfg.Network.Receive.WebhookPath != "/wechat/callback" {
		t.Fatalf("expected normalized webhook path, got %q", cfg.Network.Receive.WebhookPath)
	}
}
