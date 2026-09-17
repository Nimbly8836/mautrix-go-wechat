// Package config loads the bridge configuration and generates the appservice
// registration file, mirroring the layout used by mautrix-telegram and other
// mautrix bridges.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the top-level bridge config (config.yaml).
type Config struct {
	Homeserver Homeserver `yaml:"homeserver"`
	Appservice Appservice `yaml:"appservice"`
	Network    Network    `yaml:"network"`
	Bridge     Bridge     `yaml:"bridge"`
	Database   Database   `yaml:"database"`
	Logging    Logging    `yaml:"logging"`
}

// Homeserver describes the Matrix homeserver to connect to.
type Homeserver struct {
	Address string `yaml:"address"` // e.g. http://synapse:8008
	Domain  string `yaml:"domain"`  // e.g. example.com
}

// Appservice is the application-service registration data.
type Appservice struct {
	Address     string `yaml:"address"`      // URL the homeserver calls back, e.g. http://mautrix-wechat:29318
	Hostname    string `yaml:"hostname"`     // bind host inside the container
	Port        int    `yaml:"port"`         // bind port
	ID          string `yaml:"id"`           // appservice id, e.g. wechat
	BotUsername string `yaml:"bot_username"` // e.g. wechatbot

	ASToken string `yaml:"as_token"`
	HSToken string `yaml:"hs_token"`
}

// Network holds backend-specific options. protocol selects the registered
// remoteapi backend ("wechat", "qq", ...).
type Network struct {
	Protocol string         `yaml:"protocol"`
	BaseURL  string         `yaml:"base_url"`
	Token    string         `yaml:"token"`
	Receive  Receive        `yaml:"receive"`
	Extra    map[string]any `yaml:"extra"`
}

// Receive controls how backend messages are delivered into the bridge.
// Mode is intentionally a strict choice: either poll the API or accept pushes.
type Receive struct {
	// Mode is "poll" (default) or "push".
	Mode string `yaml:"mode"`
	// WebhookPath is the appservice-local HTTP path for push callbacks.
	WebhookPath string `yaml:"webhook_path"`
}

// Bridge holds generic bridge behaviour toggles.
type Bridge struct {
	UsernameTemplate    string            `yaml:"username_template"`
	DisplaynameTemplate string            `yaml:"displayname_template"`
	Permissions         map[string]string `yaml:"permissions"`
	// SessionFile is a deprecated read-only startup fallback for a legacy
	// opaque remoteapi session blob. AccountFile is authoritative.
	SessionFile string `yaml:"session_file"`
	// PortalFile stores Matrix room mappings for remote chats.
	PortalFile string `yaml:"portal_file"`
	// AccountFile stores multiple logged-in backend account sessions.
	AccountFile string `yaml:"account_file"`
	// ContactFile stores cached backend contact metadata per account.
	ContactFile string `yaml:"contact_file"`
	// EmojiDir stores shared WeChat emoji/sticker files across all accounts.
	EmojiDir string `yaml:"emoji_dir"`
	// WechatBuiltinEmojiFile stores the canonical global shortcode-to-MXC manifest.
	WechatBuiltinEmojiFile string `yaml:"wechat_builtin_emoji_file"`
	// PortalCreation controls how new remote chats become Matrix portal rooms.
	// Values: "auto" (default) or "manual".
	PortalCreation string `yaml:"portal_creation"`
	// OfficialAccountMode controls portal handling for WeChat official accounts.
	// Values: "invite" (default), "mute" (create without inviting owner), or "reject".
	OfficialAccountMode string `yaml:"official_account_mode"`
	// PersonalStickerUserID and PersonalStickerAccessToken optionally allow the bridge
	// to write m.image_pack.rooms account_data for the real Matrix user, which an
	// appservice cannot normally masquerade as.
	PersonalStickerUserID      string `yaml:"personal_sticker_user_id"`
	PersonalStickerAccessToken string `yaml:"personal_sticker_access_token"`
	// CommandPrefix is used for management and portal-local commands.
	CommandPrefix string `yaml:"command_prefix"`
	// IgnoredCommandPrefixes keeps matching Matrix portal text/notice messages
	// out of the remote chat. Bridge commands take precedence. Matching is
	// literal and case-sensitive; whitespace-only prefixes are ignored.
	IgnoredCommandPrefixes []string `yaml:"ignored_command_prefixes"`
}

// Database connection settings.
type Database struct {
	Type string `yaml:"type"` // sqlite3 | postgres
	URI  string `yaml:"uri"`
}

// Logging settings.
type Logging struct {
	Level string `yaml:"level"`
}

// Load reads and parses a config file.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

const (
	defaultSessionFile            = "/data/session.json"
	defaultPortalFile             = "/data/portals.json"
	defaultAccountFile            = "/data/accounts.json"
	defaultContactFile            = "/data/contacts.json"
	defaultEmojiDir               = "/data/emojis"
	defaultWechatBuiltinEmojiFile = "/data/wechat-builtin-emoji-pack.json"
)

func (c *Config) resolveBridgePathDefaults() {
	if c.Bridge.SessionFile == "" {
		c.Bridge.SessionFile = defaultSessionFile
	}
	if c.Bridge.PortalFile == "" {
		c.Bridge.PortalFile = defaultPortalFile
	}
	if c.Bridge.AccountFile == "" {
		c.Bridge.AccountFile = defaultAccountFile
	}
	if c.Bridge.ContactFile == "" {
		c.Bridge.ContactFile = defaultContactFile
	}
	if c.Bridge.EmojiDir == "" {
		c.Bridge.EmojiDir = defaultEmojiDir
	}
	if c.Bridge.WechatBuiltinEmojiFile == "" {
		c.Bridge.WechatBuiltinEmojiFile = defaultWechatBuiltinEmojiFile
	}
}

// ValidateBridgePathCollisions rejects a built-in emoji manifest path that
// aliases bridge state, the bridge config, or the appservice registration file.
func (c *Config) ValidateBridgePathCollisions(configPath, registrationPath string) error {
	c.resolveBridgePathDefaults()
	builtinPath, err := resolveComparablePath(c.Bridge.WechatBuiltinEmojiFile)
	if err != nil {
		return fmt.Errorf("resolve bridge.wechat_builtin_emoji_file %q: %w", c.Bridge.WechatBuiltinEmojiFile, err)
	}
	statePaths := []struct {
		name string
		path string
	}{
		{name: "session_file", path: c.Bridge.SessionFile},
		{name: "portal_file", path: c.Bridge.PortalFile},
		{name: "account_file", path: c.Bridge.AccountFile},
		{name: "contact_file", path: c.Bridge.ContactFile},
		{name: "emoji_dir", path: c.Bridge.EmojiDir},
	}
	for _, runtimePath := range []struct {
		name string
		path string
	}{
		{name: "config path", path: configPath},
		{name: "registration path", path: registrationPath},
	} {
		if runtimePath.path != "" {
			statePaths = append(statePaths, runtimePath)
		}
	}
	for _, statePath := range statePaths {
		resolved, resolveErr := resolveComparablePath(statePath.path)
		if resolveErr != nil {
			return fmt.Errorf("resolve %s %q: %w", statePath.name, statePath.path, resolveErr)
		}
		if builtinPath == resolved {
			return fmt.Errorf("bridge.wechat_builtin_emoji_file collides with %s at %q", statePath.name, builtinPath)
		}
	}
	return nil
}

// resolveComparablePath resolves path components in filesystem order so that
// an existing symlink is followed before a subsequent ".." is applied. Missing
// components are retained until later components (including "..") are handled.
func resolveComparablePath(path string) (string, error) {
	absolute := path
	if !filepath.IsAbs(absolute) {
		workingDir, err := os.Getwd()
		if err != nil {
			return "", err
		}
		absolute = workingDir + string(os.PathSeparator) + absolute
	}

	volume := filepath.VolumeName(absolute)
	resolved := volume + string(os.PathSeparator)
	components := strings.FieldsFunc(absolute[len(volume):], func(character rune) bool {
		return character == rune(os.PathSeparator) || os.PathSeparator == '\\' && character == '/'
	})
	for _, component := range components {
		switch component {
		case ".":
			continue
		case "..":
			resolved = filepath.Dir(resolved)
			continue
		}

		candidate := filepath.Join(resolved, component)
		if _, err := os.Lstat(candidate); err != nil {
			if !os.IsNotExist(err) {
				return "", err
			}
			resolved = candidate
			continue
		}
		canonical, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			// Lstat proved that this component exists. If evaluating it still
			// fails (including ENOENT for a dangling symlink), treating it as an
			// ordinary missing path would make later ".." components resolve
			// differently once the symlink target appears. Fail closed instead.
			return "", err
		}
		resolved = canonical
	}
	return filepath.Clean(resolved), nil
}

func (c *Config) validate() error {
	if c.Homeserver.Address == "" {
		return fmt.Errorf("homeserver.address is required")
	}
	if c.Homeserver.Domain == "" {
		return fmt.Errorf("homeserver.domain is required")
	}
	if c.Appservice.ASToken == "" || c.Appservice.HSToken == "" {
		return fmt.Errorf("appservice tokens missing; run with -g to generate registration first")
	}
	if c.Network.Protocol == "" {
		c.Network.Protocol = "wechat"
	}
	if c.Network.Receive.Mode == "" {
		c.Network.Receive.Mode = "poll"
	}
	if c.Network.Receive.Mode != "poll" && c.Network.Receive.Mode != "push" {
		return fmt.Errorf("network.receive.mode must be poll or push")
	}
	if c.Network.Receive.Mode == "push" {
		if c.Network.Receive.WebhookPath == "" {
			return fmt.Errorf("network.receive.webhook_path is required when network.receive.mode is push")
		}
		if c.Network.Receive.WebhookPath[0] != '/' {
			c.Network.Receive.WebhookPath = "/" + c.Network.Receive.WebhookPath
		}
	}
	if c.Bridge.CommandPrefix == "" {
		c.Bridge.CommandPrefix = "!wx"
	}
	c.Bridge.OfficialAccountMode = normalizeOfficialAccountModeForConfig(c.Bridge.OfficialAccountMode)
	if err := c.ValidateBridgePathCollisions("", ""); err != nil {
		return err
	}
	if c.Bridge.CommandPrefix[0] != '!' {
		c.Bridge.CommandPrefix = "!" + c.Bridge.CommandPrefix
	}
	return nil
}

func normalizeOfficialAccountModeForConfig(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "mute", "reject":
		return strings.ToLower(strings.TrimSpace(mode))
	default:
		return "invite"
	}
}

// Registration is the appservice registration file (registration.yaml) that the
// homeserver loads.
type Registration struct {
	ID              string     `yaml:"id"`
	URL             string     `yaml:"url"`
	ASToken         string     `yaml:"as_token"`
	HSToken         string     `yaml:"hs_token"`
	SenderLocalpart string     `yaml:"sender_localpart"`
	RateLimited     bool       `yaml:"rate_limited"`
	Namespaces      Namespaces `yaml:"namespaces"`
}

type Namespaces struct {
	Users   []Namespace `yaml:"users"`
	Aliases []Namespace `yaml:"aliases"`
}

type Namespace struct {
	Exclusive bool   `yaml:"exclusive"`
	Regex     string `yaml:"regex"`
}

// GenerateRegistration creates (or fills in) tokens and writes both the
// registration file and the tokens back into the config, mirroring the
// `-g`/`--generate-registration` flow of mautrix bridges.
func (c *Config) GenerateRegistration() *Registration {
	if c.Appservice.ASToken == "" {
		c.Appservice.ASToken = randToken()
	}
	if c.Appservice.HSToken == "" {
		c.Appservice.HSToken = randToken()
	}
	if c.Appservice.ID == "" {
		c.Appservice.ID = "wechat"
	}
	if c.Appservice.BotUsername == "" {
		c.Appservice.BotUsername = "wechatbot"
	}
	prefix := c.Appservice.ID
	return &Registration{
		ID:              c.Appservice.ID,
		URL:             c.Appservice.Address,
		ASToken:         c.Appservice.ASToken,
		HSToken:         c.Appservice.HSToken,
		SenderLocalpart: c.Appservice.BotUsername,
		RateLimited:     false,
		Namespaces: Namespaces{
			Users: []Namespace{
				{Exclusive: true, Regex: fmt.Sprintf("@%s_.*:%s", prefix, escapeDomain(c.Homeserver.Domain))},
				{Exclusive: true, Regex: fmt.Sprintf("@%s:%s", c.Appservice.BotUsername, escapeDomain(c.Homeserver.Domain))},
			},
			Aliases: []Namespace{
				{Exclusive: true, Regex: fmt.Sprintf("#%s_.*:%s", prefix, escapeDomain(c.Homeserver.Domain))},
			},
		},
	}
}

// Save writes the config back to disk (used after generating tokens).
func (c *Config) Save(path string) error {
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// SaveRegistration writes the registration file.
func (r *Registration) Save(path string) error {
	b, err := yaml.Marshal(r)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

func randToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func escapeDomain(d string) string {
	out := make([]rune, 0, len(d)+4)
	for _, r := range d {
		if r == '.' {
			out = append(out, '\\', '.')
		} else {
			out = append(out, r)
		}
	}
	return string(out)
}
