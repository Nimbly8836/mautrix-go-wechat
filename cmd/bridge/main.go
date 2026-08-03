package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/leat/mautrix-go-wechat/internal/config"
	"github.com/leat/mautrix-go-wechat/internal/matrixas"
	"github.com/leat/mautrix-go-wechat/internal/remoteapi"

	// Side-effect imports register protocols. Add internal/qq here to enable QQ.
	_ "github.com/leat/mautrix-go-wechat/internal/wechat"

	"github.com/spf13/pflag"
	"gopkg.in/yaml.v3"
)

func main() {
	var (
		configPath = pflag.StringP("config", "c", "/data/config.yaml", "path to config file")
		regPath    = pflag.StringP("registration", "r", "/data/registration.yaml", "path to appservice registration file")
		genReg     = pflag.BoolP("generate-registration", "g", false, "generate registration + tokens, write both files, then exit")
		version    = pflag.BoolP("version", "v", false, "print version and exit")
	)
	pflag.Parse()

	if *version {
		fmt.Println("mautrix-go-wechat (protocols:", remoteapi.Names(), ")")
		return
	}

	if *genReg {
		if err := generateRegistration(*configPath, *regPath); err != nil {
			log.Fatalf("generate registration: %v", err)
		}
		log.Printf("wrote %s and updated %s — restart without -g to run", *regPath, *configPath)
		return
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	if err = cfg.ValidateBridgePathCollisions(*configPath, *regPath); err != nil {
		log.Fatalf("validate bridge paths: %v", err)
	}
	if cfg.Bridge.SessionFile == "" {
		cfg.Bridge.SessionFile = "/data/session.json"
	}
	if cfg.Bridge.PortalFile == "" {
		cfg.Bridge.PortalFile = "/data/portals.json"
	}
	if cfg.Bridge.AccountFile == "" {
		cfg.Bridge.AccountFile = "/data/accounts.json"
	}
	if cfg.Bridge.PortalCreation == "" {
		cfg.Bridge.PortalCreation = "auto"
	}

	if cfg.Network.Extra == nil {
		cfg.Network.Extra = map[string]any{}
	}
	if _, ok := cfg.Network.Extra["emoji_dir"]; !ok {
		cfg.Network.Extra["emoji_dir"] = cfg.Bridge.EmojiDir
	}
	apiCfg := remoteapi.Config{
		BaseURL:     cfg.Network.BaseURL,
		Token:       cfg.Network.Token,
		ReceiveMode: cfg.Network.Receive.Mode,
		Extra:       cfg.Network.Extra,
	}
	// matrixas.New loads the current account-store session first and only uses
	// the legacy session_file when no current account exists.
	srv, err := matrixas.New(cfg, *regPath, apiCfg)
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	log.Printf("starting Matrix appservice on %s:%d; bot @%s:%s", cfg.Appservice.Hostname, cfg.Appservice.Port, cfg.Appservice.BotUsername, cfg.Homeserver.Domain)
	if err := srv.Run(ctx); err != nil && ctx.Err() == nil {
		log.Fatal(err)
	}
}

func generateRegistration(configPath, regPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		cfg, err = loadForGen(configPath)
		if err != nil {
			return err
		}
	}
	if err = cfg.ValidateBridgePathCollisions(configPath, regPath); err != nil {
		return err
	}
	reg := cfg.GenerateRegistration()
	if err := reg.Save(regPath); err != nil {
		return err
	}
	return cfg.Save(configPath)
}

// loadForGen loads config without token validation (so -g can fill them in).
func loadForGen(path string) (*config.Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c config.Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}
