package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/kai/freestone/internal/config"
	"github.com/kai/freestone/internal/gauth"
	"github.com/kai/freestone/internal/secrets"
)

// runAuth handles the `auth` subcommand.
func runAuth(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: freestone-mcp auth google [--revoke] [--config PATH]")
	}
	switch args[0] {
	case "google":
		return runAuthGoogle(args[1:])
	default:
		return fmt.Errorf("unknown auth provider: %s", args[0])
	}
}

func runAuthGoogle(args []string) error {
	fs := flag.NewFlagSet("auth google", flag.ExitOnError)
	configPath := fs.String("config", defaultConfigPath(), "path to config.toml")
	revoke := fs.Bool("revoke", false, "delete stored Google token from Keychain")
	fs.Parse(args) //nolint:errcheck

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	// Determine which Google provider configs are present.
	var gcal *config.GoogleProviderCfg
	var gmail *config.GoogleProviderCfg
	if cfg.Calendar.Google != nil {
		gcal = cfg.Calendar.Google
	}
	if cfg.Mail.Google != nil {
		gmail = cfg.Mail.Google
	}
	if gcal == nil && gmail == nil {
		return fmt.Errorf("no Google provider configured in %s; enable [calendar.google] or [mail.google]", *configPath)
	}

	// Use the first available token_ref as the canonical ref.
	// Both calendar and mail should use the same token_ref in practice.
	tokenRef := ""
	if gcal != nil {
		tokenRef = gcal.TokenRef
	}
	if tokenRef == "" && gmail != nil {
		tokenRef = gmail.TokenRef
	}
	if tokenRef == "" {
		return fmt.Errorf("no token_ref configured for Google provider")
	}

	if *revoke {
		if err := gauth.DeleteToken(tokenRef); err != nil {
			return fmt.Errorf("revoke: %w", err)
		}
		fmt.Println("Google token deleted from Keychain.")
		return nil
	}

	// Resolve client credentials from Keychain.
	resolver, err := secrets.New()
	if err != nil {
		return err
	}

	// Pick client ID/secret from whichever Google config is available.
	var clientIDRef, clientSecretRef string
	if gcal != nil {
		clientIDRef = gcal.ClientIDRef
		clientSecretRef = gcal.ClientSecretRef
	} else {
		clientIDRef = gmail.ClientIDRef
		clientSecretRef = gmail.ClientSecretRef
	}

	clientID, err := resolver.Resolve(clientIDRef)
	if err != nil {
		return fmt.Errorf("resolve client ID: %w", err)
	}
	clientSecret, err := resolver.Resolve(clientSecretRef)
	if err != nil {
		return fmt.Errorf("resolve client secret: %w", err)
	}

	// Build scopes based on enabled providers.
	var scopes []string
	if gcal != nil {
		scopes = append(scopes, "https://www.googleapis.com/auth/calendar.readonly")
	}
	if gmail != nil {
		scopes = append(scopes, "https://www.googleapis.com/auth/gmail.readonly")
	}

	oauthCfg := gauth.NewConfig(clientID, clientSecret, scopes)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	fmt.Printf("Requesting scopes: %v\n", scopes)
	tok, err := gauth.RunLocalFlow(ctx, oauthCfg)
	if err != nil {
		return fmt.Errorf("auth flow: %w", err)
	}

	if err := gauth.SaveToken(tokenRef, tok); err != nil {
		return fmt.Errorf("save token: %w", err)
	}

	fmt.Printf("Google token saved to Keychain as %q.\n", tokenRef)
	fmt.Println("freestone-mcp can now access Google APIs.")
	if os.Getenv("FREESTONE_SECRETS") == "env" {
		fmt.Println("Note: running with FREESTONE_SECRETS=env; token was still saved to Keychain.")
	}
	return nil
}
