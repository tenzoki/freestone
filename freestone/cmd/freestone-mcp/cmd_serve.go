package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kai/freestone/internal/calendar"
	icloudcal "github.com/kai/freestone/internal/calendar/providers/icloud"
	"github.com/kai/freestone/internal/config"
	"github.com/kai/freestone/internal/files"
	"github.com/kai/freestone/internal/logger"
	"github.com/kai/freestone/internal/mail"
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
		drv, err := icloudcal.New(ctx, ic.Endpoint, ic.Username, password, log)
		if err != nil {
			log.Error("icloud calendar init failed", "err", err)
		} else {
			calProviders = append(calProviders, drv)
		}
	}
	// Google calendar provider wired in Phase 1B.
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
	// Google mail provider wired in Phase 1B.
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

func defaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "config.toml"
	}
	return filepath.Join(home, ".config", "freestone", "config.toml")
}
