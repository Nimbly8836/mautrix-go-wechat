// Package config loads the bridge configuration and generates the appservice
// registration file, mirroring the layout used by mautrix-telegram and other
// mautrix bridges.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"

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
	// SessionFile stores the opaque remoteapi session blob (WeChat wxid/sync key).
	SessionFile string `yaml:"session_file"`
	// PortalFile stores Matrix room mappings for remote chats.
	PortalFile string `yaml:"portal_file"`
	// AccountFile stores multiple logged-in backend account sessions.
	AccountFile string `yaml:"account_file"`
	// PortalCreation controls how new remote chats become Matrix portal rooms.
	// Values: "auto" (default) or "manual".
	PortalCreation string `yaml:"portal_creation"`
	// CommandPrefix is used for management and portal-local commands.
	CommandPrefix string `yaml:"command_prefix"`
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
	if c.Bridge.CommandPrefix[0] != '!' {
		c.Bridge.CommandPrefix = "!" + c.Bridge.CommandPrefix
	}
	return nil
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
