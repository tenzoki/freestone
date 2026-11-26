// Package config loads and validates the freestone TOML configuration.
package config

import (
	"fmt"
	"net/mail"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config is the top-level configuration.
type Config struct {
	Server   ServerCfg   `toml:"server"`
	Calendar CalendarCfg `toml:"calendar"`
	Mail     MailCfg     `toml:"mail"`
	Files    *FilesCfg   `toml:"files,omitempty"`
}

// ServerCfg holds server-wide settings.
type ServerCfg struct {
	LogFile string `toml:"log_file"`
	// AuditFile is the append-only JSONL audit log of write tools. Optional:
	// when empty, serve writes ~/.config/freestone/audit.jsonl.
	AuditFile string `toml:"audit_file"`
}

// CalendarCfg groups calendar providers.
type CalendarCfg struct {
	ICloud *ICloudCalendarCfg `toml:"icloud,omitempty"`
	Google *GoogleProviderCfg `toml:"google,omitempty"`
}

// MailCfg groups mail providers.
type MailCfg struct {
	ICloud *ICloudMailCfg     `toml:"icloud,omitempty"`
	Google *GoogleProviderCfg `toml:"google,omitempty"`
}

// ICloudCalendarCfg configures the iCloud CalDAV provider.
type ICloudCalendarCfg struct {
	Endpoint  string `toml:"endpoint"`
	Username  string `toml:"username"`
	SecretRef string `toml:"secret_ref"`
}

// ICloudMailCfg configures the iCloud IMAP provider.
type ICloudMailCfg struct {
	Host          string `toml:"host"`
	Port          int    `toml:"port"`
	Username      string `toml:"username"`
	SecretRef     string `toml:"secret_ref"`
	DraftsMailbox string `toml:"drafts_mailbox"`
	// FromAddress is the sender written into saved drafts. Optional: when
	// empty the driver uses Username, which must then be a mail address.
	FromAddress string `toml:"from_address"`
}

// GoogleProviderCfg configures a Google API provider (calendar or mail).
type GoogleProviderCfg struct {
	ClientIDRef     string `toml:"client_id_ref"`
	ClientSecretRef string `toml:"client_secret_ref"`
	TokenRef        string `toml:"token_ref"`
	UserEmail       string `toml:"user_email"`
}

// FilesCfg configures the sandboxed file access.
type FilesCfg struct {
	RulesFile    string `toml:"rules_file"`
	MaxReadBytes int64  `toml:"max_read_bytes"`
}

// Load reads and validates a config file at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}

	var cfg Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	return &cfg, nil
}

func (c *Config) validate() error {
	// A relative path would resolve against whatever directory the MCP
	// client starts the server in.
	if c.Server.AuditFile != "" && !filepath.IsAbs(c.Server.AuditFile) {
		return fmt.Errorf("server.audit_file must be an absolute path")
	}

	hasProvider := false

	if ic := c.Calendar.ICloud; ic != nil {
		hasProvider = true
		if err := validateICloudCalendar(ic); err != nil {
			return err
		}
	}
	if g := c.Calendar.Google; g != nil {
		hasProvider = true
		if err := validateGoogle("calendar.google", g); err != nil {
			return err
		}
	}
	if ic := c.Mail.ICloud; ic != nil {
		hasProvider = true
		if err := validateICloudMail(ic); err != nil {
			return err
		}
	}
	if g := c.Mail.Google; g != nil {
		hasProvider = true
		if err := validateGoogle("mail.google", g); err != nil {
			return err
		}
	}
	if f := c.Files; f != nil {
		hasProvider = true
		if err := validateFiles(f); err != nil {
			return err
		}
	}

	if !hasProvider {
		return fmt.Errorf("at least one provider or files must be enabled")
	}

	return nil
}

func validateICloudCalendar(c *ICloudCalendarCfg) error {
	if c.Endpoint == "" {
		return fmt.Errorf("calendar.icloud.endpoint is required")
	}
	if c.Username == "" {
		return fmt.Errorf("calendar.icloud.username is required")
	}
	if c.SecretRef == "" {
		return fmt.Errorf("calendar.icloud.secret_ref is required")
	}
	return nil
}

func validateICloudMail(c *ICloudMailCfg) error {
	if c.Host == "" {
		return fmt.Errorf("mail.icloud.host is required")
	}
	if c.Port == 0 {
		return fmt.Errorf("mail.icloud.port is required")
	}
	if c.Username == "" {
		return fmt.Errorf("mail.icloud.username is required")
	}
	if c.SecretRef == "" {
		return fmt.Errorf("mail.icloud.secret_ref is required")
	}
	if c.FromAddress != "" {
		// A line break would pass ParseAddress only to be rejected on every
		// draft; checking here makes a bad config fail at startup instead.
		if strings.ContainsAny(c.FromAddress, "\r\n") {
			return fmt.Errorf("mail.icloud.from_address must not contain a line break")
		}
		if _, err := mail.ParseAddress(c.FromAddress); err != nil {
			return fmt.Errorf("mail.icloud.from_address %q is not a mail address: %w", c.FromAddress, err)
		}
	}
	return nil
}

func validateGoogle(prefix string, c *GoogleProviderCfg) error {
	if c.ClientIDRef == "" {
		return fmt.Errorf("%s.client_id_ref is required", prefix)
	}
	if c.ClientSecretRef == "" {
		return fmt.Errorf("%s.client_secret_ref is required", prefix)
	}
	if c.TokenRef == "" {
		return fmt.Errorf("%s.token_ref is required", prefix)
	}
	return nil
}

func validateFiles(c *FilesCfg) error {
	if c.RulesFile == "" {
		return fmt.Errorf("files.rules_file is required")
	}
	if !filepath.IsAbs(c.RulesFile) {
		return fmt.Errorf("files.rules_file must be an absolute path")
	}
	if c.MaxReadBytes < 0 {
		return fmt.Errorf("files.max_read_bytes must be non-negative")
	}
	// Default to 5 MiB if not set.
	if c.MaxReadBytes == 0 {
		c.MaxReadBytes = 5 * 1024 * 1024
	}
	return nil
}
