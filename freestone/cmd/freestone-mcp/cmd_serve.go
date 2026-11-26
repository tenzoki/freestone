package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/oauth2"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kai/freestone/internal/calendar"
	googlecal "github.com/kai/freestone/internal/calendar/providers/google"
	icloudcal "github.com/kai/freestone/internal/calendar/providers/icloud"
	"github.com/kai/freestone/internal/config"
	"github.com/kai/freestone/internal/files"
	"github.com/kai/freestone/internal/gauth"
	"github.com/kai/freestone/internal/logger"
	"github.com/kai/freestone/internal/mail"
	googlemail "github.com/kai/freestone/internal/mail/providers/google"
	icloudmail "github.com/kai/freestone/internal/mail/providers/icloud"
	"github.com/kai/freestone/internal/mcpsrv"
	"github.com/kai/freestone/internal/secrets"
)

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	configPath := fs.String("config", defaultConfigPath(), "path to config.toml")
	fs.Parse(args) //nolint:errcheck

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	log, logCloser, err := logger.New(cfg.Server.LogFile)
	if err != nil {
		return err
	}
	defer logCloser.Close()

	resolver, err := secrets.New()
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	deps := mcpsrv.Deps{Log: log}

	// Wire calendar providers.
	var calProviders []calendar.Provider
	if ic := cfg.Calendar.ICloud; ic != nil {
		password, err := resolver.Resolve(ic.SecretRef)
		if err != nil {
			return fmt.Errorf("resolve icloud calendar secret: %w", err)
		}
		// Floating iCal times are the user's wall clock; this server runs on
		// the user's machine, so its local zone is that clock.
		drv, err := icloudcal.New(ctx, ic.Endpoint, ic.Username, password, time.Local, log)
		if err != nil {
			log.Error("icloud calendar init failed", "err", err)
		} else {
			calProviders = append(calProviders, drv)
		}
	}
	if g := cfg.Calendar.Google; g != nil {
		ts, err := googleTokenSource(ctx, cfg, resolver)
		if err != nil {
			log.Error("google calendar: token not available, skipping provider",
				"err", err, "hint", "run: freestone-mcp auth google")
		} else {
			drv, err := googlecal.New(ctx, ts)
			if err != nil {
				log.Error("google calendar init failed", "err", err)
			} else {
				calProviders = append(calProviders, drv)
			}
		}
	}
	if len(calProviders) > 0 {
		reg, err := calendar.NewRegistry(calProviders...)
		if err != nil {
			return err
		}
		deps.Calendar = reg
	}

	// Wire mail providers.
	var mailProviders []mail.Provider
	if ic := cfg.Mail.ICloud; ic != nil {
		password, err := resolver.Resolve(ic.SecretRef)
		if err != nil {
			return fmt.Errorf("resolve icloud mail secret: %w", err)
		}
		drv := icloudmail.New(ic.Host, ic.Port, ic.Username, password, ic.DraftsMailbox)
		mailProviders = append(mailProviders, drv)
	}
	if g := cfg.Mail.Google; g != nil {
		ts, err := googleTokenSource(ctx, cfg, resolver)
		if err != nil {
			log.Error("gmail: token not available, skipping provider",
				"err", err, "hint", "run: freestone-mcp auth google")
		} else {
			drv, err := googlemail.New(ctx, ts)
			if err != nil {
				log.Error("gmail init failed", "err", err)
			} else {
				mailProviders = append(mailProviders, drv)
			}
		}
	}
	if len(mailProviders) > 0 {
		reg, err := mail.NewRegistry(mailProviders...)
		if err != nil {
			return err
		}
		deps.Mail = reg
	}

	// Wire files.
	if fc := cfg.Files; fc != nil {
		f, err := os.Open(fc.RulesFile)
		if err != nil {
			return fmt.Errorf("open rules file: %w", err)
		}
		rules, err := files.ParseRules(f)
		f.Close()
		if err != nil {
			return err
		}
		deps.Files = files.NewLocalFS(rules, fc.MaxReadBytes, log)
	}

	srv := mcpsrv.Build(deps)
	log.Info("freestone-mcp starting")
	if err := srv.Run(ctx, &mcp.StdioTransport{}); err != nil {
		return fmt.Errorf("mcp server: %w", err)
	}
	return nil
}

// googleTokenSource builds an oauth2.TokenSource for Google APIs.
// It uses the first available Google provider config to find the client
// credentials and token ref. Both calendar and mail share the same token.
func googleTokenSource(ctx context.Context, cfg *config.Config, resolver secrets.Resolver) (oauth2.TokenSource, error) {
	// This function is only called when at least one Google provider is
	// configured, so at least one of these will be non-nil.
	var clientIDRef, clientSecretRef, tokenRef string
	if g := cfg.Calendar.Google; g != nil {
		clientIDRef = g.ClientIDRef
		clientSecretRef = g.ClientSecretRef
		tokenRef = g.TokenRef
	}
	if g := cfg.Mail.Google; g != nil {
		if clientIDRef == "" {
			clientIDRef = g.ClientIDRef
			clientSecretRef = g.ClientSecretRef
		}
		if tokenRef == "" {
			tokenRef = g.TokenRef
		}
	}

	clientID, err := resolver.Resolve(clientIDRef)
	if err != nil {
		return nil, fmt.Errorf("resolve google client ID: %w", err)
	}
	clientSecret, err := resolver.Resolve(clientSecretRef)
	if err != nil {
		return nil, fmt.Errorf("resolve google client secret: %w", err)
	}

	oauthCfg := gauth.NewConfig(clientID, clientSecret, googleScopes(cfg))
	return gauth.TokenSource(ctx, oauthCfg, tokenRef, resolver)
}

func defaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "config.toml"
	}
	return filepath.Join(home, ".config", "freestone", "config.toml")
}
