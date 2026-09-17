package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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

func TestBridgeIgnoredCommandPrefixes(t *testing.T) {
	for _, tc := range []struct {
		name string
		yaml string
		want []string
	}{
		{name: "legacy config"},
		{name: "empty list", yaml: "  ignored_command_prefixes: []\n", want: []string{}},
		{name: "bot commands", yaml: "  ignored_command_prefixes: [\"!da_bot\", \"/tool\", \"!\"]\n", want: []string{"!da_bot", "/tool", "!"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.Replace(fmt.Sprintf(baseConfigYAML, ""), "bridge:\n", "bridge:\n"+tc.yaml, 1)
			cfg, err := Load(writeConfig(t, body))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if !reflect.DeepEqual(cfg.Bridge.IgnoredCommandPrefixes, tc.want) {
				t.Fatalf("prefixes = %#v, want %#v", cfg.Bridge.IgnoredCommandPrefixes, tc.want)
			}
			path := filepath.Join(t.TempDir(), "saved.yaml")
			if err = cfg.Save(path); err != nil {
				t.Fatalf("Save: %v", err)
			}
			reloaded, err := Load(path)
			if err != nil {
				t.Fatalf("reload: %v", err)
			}
			if strings.Join(reloaded.Bridge.IgnoredCommandPrefixes, "\x00") != strings.Join(tc.want, "\x00") {
				t.Fatalf("prefixes changed after Save/Load: %#v", reloaded.Bridge.IgnoredCommandPrefixes)
			}
		})
	}
}

func TestBridgeWechatBuiltinEmojiFileDefaultsForLegacyConfig(t *testing.T) {
	cfg, err := Load(writeConfig(t, fmt.Sprintf(baseConfigYAML, "")))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Bridge.WechatBuiltinEmojiFile != "/data/wechat-builtin-emoji-pack.json" {
		t.Fatalf("expected default built-in emoji file, got %q", cfg.Bridge.WechatBuiltinEmojiFile)
	}
}

func TestBridgeWechatBuiltinEmojiFileRejectsBridgeStatePathCollisions(t *testing.T) {
	for _, tc := range []struct {
		name       string
		stateField string
	}{
		{name: "session file", stateField: "session_file"},
		{name: "portal file", stateField: "portal_file"},
		{name: "account file", stateField: "account_file"},
		{name: "contact file", stateField: "contact_file"},
		{name: "emoji directory", stateField: "emoji_dir"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bridgePaths := fmt.Sprintf("bridge:\n  %s: /custom/shared-state\n  wechat_builtin_emoji_file: /custom/../custom/shared-state\n", tc.stateField)
			body := strings.Replace(baseConfigYAML, "bridge:\n", bridgePaths, 1)
			_, err := Load(writeConfig(t, fmt.Sprintf(body, "")))
			if err == nil || !strings.Contains(err.Error(), "wechat_builtin_emoji_file") || !strings.Contains(err.Error(), tc.stateField) {
				t.Fatalf("Load collision error = %v, want built-in/store path names", err)
			}
		})
	}
}

func TestBridgeWechatBuiltinEmojiFileRejectsSymlinkAliasCollisions(t *testing.T) {
	t.Run("existing file symlink", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "state.json")
		if err := os.WriteFile(target, []byte("state"), 0o600); err != nil {
			t.Fatal(err)
		}
		alias := filepath.Join(dir, "manifest.json")
		if err := os.Symlink(target, alias); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		body := strings.Replace(baseConfigYAML, "bridge:\n", fmt.Sprintf("bridge:\n  session_file: %s\n  wechat_builtin_emoji_file: %s\n", target, alias), 1)
		if _, err := Load(writeConfig(t, fmt.Sprintf(body, ""))); err == nil || !strings.Contains(err.Error(), "session_file") {
			t.Fatalf("existing symlink collision error = %v", err)
		}
	})

	t.Run("symlinked parent for nonexistent leaf", func(t *testing.T) {
		dir := t.TempDir()
		realParent := filepath.Join(dir, "real")
		if err := os.Mkdir(realParent, 0o700); err != nil {
			t.Fatal(err)
		}
		aliasParent := filepath.Join(dir, "alias")
		if err := os.Symlink(realParent, aliasParent); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		realLeaf := filepath.Join(realParent, "future.json")
		aliasLeaf := filepath.Join(aliasParent, "future.json")
		body := strings.Replace(baseConfigYAML, "bridge:\n", fmt.Sprintf("bridge:\n  account_file: %s\n  wechat_builtin_emoji_file: %s\n", realLeaf, aliasLeaf), 1)
		if _, err := Load(writeConfig(t, fmt.Sprintf(body, ""))); err == nil || !strings.Contains(err.Error(), "account_file") {
			t.Fatalf("parent symlink collision error = %v", err)
		}
	})
}

func TestBridgeWechatBuiltinEmojiFileRejectsRuntimeConfigPathSymlinkAlias(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("config"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifestAlias := filepath.Join(dir, "manifest.json")
	if err := os.Symlink(configPath, manifestAlias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	cfg := &Config{Bridge: Bridge{WechatBuiltinEmojiFile: manifestAlias}}

	if err := cfg.ValidateBridgePathCollisions(configPath, ""); err == nil || !strings.Contains(err.Error(), "config path") {
		t.Fatalf("config path collision error = %v", err)
	}
}

func TestBridgeWechatBuiltinEmojiFileRejectsSymlinkTraversalCollisions(t *testing.T) {
	dir := t.TempDir()
	safeDir := filepath.Join(dir, "safe")
	victimDir := filepath.Join(dir, "victim")
	victimSubdir := filepath.Join(victimDir, "subdir")
	if err := os.MkdirAll(safeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(victimSubdir, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(safeDir, "link")
	if err := os.Symlink(victimSubdir, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	protectedPath := filepath.Join(victimDir, "config.yaml")
	if err := os.WriteFile(protectedPath, []byte("protected"), 0o600); err != nil {
		t.Fatal(err)
	}
	separator := string(os.PathSeparator)
	candidate := link + separator + ".." + separator + "config.yaml"

	t.Run("config path", func(t *testing.T) {
		cfg := &Config{Bridge: Bridge{WechatBuiltinEmojiFile: candidate}}
		if err := cfg.ValidateBridgePathCollisions(protectedPath, ""); err == nil || !strings.Contains(err.Error(), "config path") {
			t.Fatalf("symlink traversal config collision error = %v", err)
		}
	})

	t.Run("state path", func(t *testing.T) {
		cfg := &Config{Bridge: Bridge{
			SessionFile:            protectedPath,
			WechatBuiltinEmojiFile: candidate,
		}}
		if err := cfg.ValidateBridgePathCollisions("", ""); err == nil || !strings.Contains(err.Error(), "session_file") {
			t.Fatalf("symlink traversal state collision error = %v", err)
		}
	})

	t.Run("relative path", func(t *testing.T) {
		oldWorkingDir, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if err = os.Chdir(dir); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if chdirErr := os.Chdir(oldWorkingDir); chdirErr != nil {
				t.Errorf("restore working directory: %v", chdirErr)
			}
		})
		relativeCandidate := "safe" + separator + "link" + separator + ".." + separator + "config.yaml"
		cfg := &Config{Bridge: Bridge{WechatBuiltinEmojiFile: relativeCandidate}}
		if err = cfg.ValidateBridgePathCollisions(protectedPath, ""); err == nil || !strings.Contains(err.Error(), "config path") {
			t.Fatalf("relative symlink traversal collision error = %v", err)
		}
	})

	t.Run("dangling symlink fails closed", func(t *testing.T) {
		danglingDir := filepath.Join(dir, "dangling")
		if err := os.Mkdir(danglingDir, 0o700); err != nil {
			t.Fatal(err)
		}
		danglingLink := filepath.Join(danglingDir, "link")
		if err := os.Symlink(filepath.Join(victimDir, "future-subdir"), danglingLink); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		absoluteCandidate := danglingLink + separator + ".." + separator + "config.yaml"
		cfg := &Config{Bridge: Bridge{WechatBuiltinEmojiFile: absoluteCandidate}}
		if err := cfg.ValidateBridgePathCollisions(protectedPath, ""); err == nil || !strings.Contains(err.Error(), "resolve bridge.wechat_builtin_emoji_file") {
			t.Fatalf("dangling absolute symlink error = %v, want fail-closed resolution", err)
		}

		oldWorkingDir, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if err = os.Chdir(dir); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if chdirErr := os.Chdir(oldWorkingDir); chdirErr != nil {
				t.Errorf("restore working directory: %v", chdirErr)
			}
		})
		relativeCandidate := "dangling" + separator + "link" + separator + ".." + separator + "config.yaml"
		cfg.Bridge.WechatBuiltinEmojiFile = relativeCandidate
		if err = cfg.ValidateBridgePathCollisions(protectedPath, ""); err == nil || !strings.Contains(err.Error(), "resolve bridge.wechat_builtin_emoji_file") {
			t.Fatalf("dangling relative symlink error = %v, want fail-closed resolution", err)
		}
	})

	t.Run("nonexistent component without symlink", func(t *testing.T) {
		nonColliding := safeDir + separator + "missing" + separator + ".." + separator + "config.yaml"
		cfg := &Config{Bridge: Bridge{WechatBuiltinEmojiFile: nonColliding}}
		if err := cfg.ValidateBridgePathCollisions(protectedPath, ""); err != nil {
			t.Fatalf("distinct traversal path rejected: %v", err)
		}
	})
}

func TestResolveComparablePathPreservesUnicodeComponents(t *testing.T) {
	dir := t.TempDir()
	canonicalDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	separator := string(os.PathSeparator)
	path := dir + separator + "\u012f" + separator + "future.yaml"
	resolved, err := resolveComparablePath(path)
	if err != nil {
		t.Fatalf("resolveComparablePath() error = %v", err)
	}
	want := filepath.Join(canonicalDir, "\u012f", "future.yaml")
	if resolved != want {
		t.Fatalf("resolveComparablePath() = %q, want %q", resolved, want)
	}
}

func TestBridgeWechatBuiltinEmojiFilePathResolutionFailsClosed(t *testing.T) {
	dir := t.TempDir()
	loop := filepath.Join(dir, "loop")
	if err := os.Symlink("loop", loop); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	body := strings.Replace(baseConfigYAML, "bridge:\n", fmt.Sprintf("bridge:\n  wechat_builtin_emoji_file: %s\n", filepath.Join(loop, "manifest.json")), 1)
	if _, err := Load(writeConfig(t, fmt.Sprintf(body, ""))); err == nil || !strings.Contains(err.Error(), "resolve bridge.wechat_builtin_emoji_file") {
		t.Fatalf("path resolution error = %v, want fail-closed manifest resolution", err)
	}
}

func TestBridgeWechatBuiltinEmojiFileRejectsDefaultStatePathCollisions(t *testing.T) {
	for _, tc := range []struct {
		name       string
		stateField string
		path       string
	}{
		{name: "session file", stateField: "session_file", path: "/data/session.json"},
		{name: "portal file", stateField: "portal_file", path: "/data/portals.json"},
		{name: "account file", stateField: "account_file", path: "/data/accounts.json"},
		{name: "contact file", stateField: "contact_file", path: "/data/contacts.json"},
		{name: "emoji directory", stateField: "emoji_dir", path: "/data/emojis"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bridgePaths := fmt.Sprintf("bridge:\n  wechat_builtin_emoji_file: %s\n", tc.path)
			body := strings.Replace(baseConfigYAML, "bridge:\n", bridgePaths, 1)
			_, err := Load(writeConfig(t, fmt.Sprintf(body, "")))
			if err == nil || !strings.Contains(err.Error(), tc.stateField) {
				t.Fatalf("Load default collision error = %v, want %s collision", err, tc.stateField)
			}
		})
	}
}

func TestBridgeStatePathDefaultsRemainRuntimeDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, fmt.Sprintf(baseConfigYAML, "")))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	got := []string{
		cfg.Bridge.SessionFile,
		cfg.Bridge.PortalFile,
		cfg.Bridge.AccountFile,
		cfg.Bridge.ContactFile,
		cfg.Bridge.EmojiDir,
		cfg.Bridge.WechatBuiltinEmojiFile,
	}
	want := []string{
		"/data/session.json",
		"/data/portals.json",
		"/data/accounts.json",
		"/data/contacts.json",
		"/data/emojis",
		"/data/wechat-builtin-emoji-pack.json",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolved bridge paths = %v, want %v", got, want)
	}
}

func TestBridgeWechatBuiltinEmojiFilePreservesCustomPath(t *testing.T) {
	body := strings.Replace(baseConfigYAML, "bridge:\n", "bridge:\n  wechat_builtin_emoji_file: /custom/wechat-pack.json\n", 1)
	cfg, err := Load(writeConfig(t, fmt.Sprintf(body, "")))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Bridge.WechatBuiltinEmojiFile != "/custom/wechat-pack.json" {
		t.Fatalf("expected custom built-in emoji file, got %q", cfg.Bridge.WechatBuiltinEmojiFile)
	}
}

func TestBridgeWechatBuiltinEmojiFileAllowsDistinctCustomBridgePaths(t *testing.T) {
	body := strings.Replace(baseConfigYAML, "bridge:\n", `bridge:
  session_file: /custom/session.json
  portal_file: /custom/portals.json
  account_file: /custom/accounts.json
  contact_file: /custom/contacts.json
  emoji_dir: /custom/emojis
  wechat_builtin_emoji_file: /custom/builtin.json
`, 1)
	cfg, err := Load(writeConfig(t, fmt.Sprintf(body, "")))
	if err != nil {
		t.Fatalf("Load returned error for distinct paths: %v", err)
	}
	if cfg.Bridge.WechatBuiltinEmojiFile != "/custom/builtin.json" {
		t.Fatalf("custom built-in path = %q", cfg.Bridge.WechatBuiltinEmojiFile)
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
