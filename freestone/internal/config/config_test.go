package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTOML(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoad_MinimalICloudCalendar(t *testing.T) {
	p := writeTOML(t, `
[calendar.icloud]
endpoint   = "https://caldav.icloud.com"
username   = "user@icloud.com"
secret_ref = "icloud-calendar-pw"
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Calendar.ICloud.Endpoint != "https://caldav.icloud.com" {
		t.Errorf("got endpoint %q", cfg.Calendar.ICloud.Endpoint)
	}
}

func TestLoad_MinimalGoogle(t *testing.T) {
	p := writeTOML(t, `
[calendar.google]
client_id_ref     = "goog-client-id"
client_secret_ref = "goog-client-secret"
token_ref         = "goog-token"
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Calendar.Google == nil {
		t.Fatal("expected google calendar config")
	}
}

func TestLoad_MinimalICloudMail(t *testing.T) {
	p := writeTOML(t, `
[mail.icloud]
host       = "imap.mail.me.com"
port       = 993
username   = "user@icloud.com"
secret_ref = "icloud-mail-pw"
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mail.ICloud.Port != 993 {
		t.Errorf("got port %d", cfg.Mail.ICloud.Port)
	}
}

func TestLoad_FilesDefaults(t *testing.T) {
	p := writeTOML(t, `
[files]
rules_file = "/tmp/access.rules"
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Files.MaxReadBytes != 5*1024*1024 {
		t.Errorf("expected 5MiB default, got %d", cfg.Files.MaxReadBytes)
	}
}

func TestLoad_AllProviders(t *testing.T) {
	p := writeTOML(t, `
[calendar.icloud]
endpoint   = "https://caldav.icloud.com"
username   = "user@icloud.com"
secret_ref = "ic-cal-pw"

[calendar.google]
client_id_ref     = "g-cid"
client_secret_ref = "g-cs"
token_ref         = "g-tok"

[mail.icloud]
host       = "imap.mail.me.com"
port       = 993
username   = "user@icloud.com"
secret_ref = "ic-mail-pw"

[mail.google]
client_id_ref     = "g-cid"
client_secret_ref = "g-cs"
token_ref         = "g-tok"

[files]
rules_file     = "/tmp/access.rules"
max_read_bytes = 1048576
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Calendar.ICloud == nil || cfg.Calendar.Google == nil {
		t.Fatal("expected both calendar providers")
	}
	if cfg.Mail.ICloud == nil || cfg.Mail.Google == nil {
		t.Fatal("expected both mail providers")
	}
	if cfg.Files == nil {
		t.Fatal("expected files")
	}
	if cfg.Files.MaxReadBytes != 1048576 {
		t.Errorf("expected 1MiB, got %d", cfg.Files.MaxReadBytes)
	}
}

func TestLoad_NoProviders(t *testing.T) {
	p := writeTOML(t, `
[server]
log_file = "/tmp/freestone.log"
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected error for no providers")
	}
	if !strings.Contains(err.Error(), "at least one provider") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestLoad_ICloudCalendarMissingEndpoint(t *testing.T) {
	p := writeTOML(t, `
[calendar.icloud]
username   = "user@icloud.com"
secret_ref = "pw"
`)
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "endpoint") {
		t.Errorf("expected endpoint error, got: %v", err)
	}
}

func TestLoad_ICloudCalendarMissingUsername(t *testing.T) {
	p := writeTOML(t, `
[calendar.icloud]
endpoint   = "https://caldav.icloud.com"
secret_ref = "pw"
`)
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "username") {
		t.Errorf("expected username error, got: %v", err)
	}
}

func TestLoad_ICloudCalendarMissingSecretRef(t *testing.T) {
	p := writeTOML(t, `
[calendar.icloud]
endpoint = "https://caldav.icloud.com"
username = "user@icloud.com"
`)
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "secret_ref") {
		t.Errorf("expected secret_ref error, got: %v", err)
	}
}

func TestLoad_ICloudMailMissingHost(t *testing.T) {
	p := writeTOML(t, `
[mail.icloud]
port       = 993
username   = "user@icloud.com"
secret_ref = "pw"
`)
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "host") {
		t.Errorf("expected host error, got: %v", err)
	}
}

func TestLoad_ICloudMailMissingPort(t *testing.T) {
	p := writeTOML(t, `
[mail.icloud]
host       = "imap.mail.me.com"
username   = "user@icloud.com"
secret_ref = "pw"
`)
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "port") {
		t.Errorf("expected port error, got: %v", err)
	}
}

func TestLoad_GoogleMissingClientID(t *testing.T) {
	p := writeTOML(t, `
[calendar.google]
client_secret_ref = "cs"
token_ref         = "tok"
`)
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "client_id_ref") {
		t.Errorf("expected client_id_ref error, got: %v", err)
	}
}

func TestLoad_GoogleMissingClientSecret(t *testing.T) {
	p := writeTOML(t, `
[calendar.google]
client_id_ref = "cid"
token_ref     = "tok"
`)
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "client_secret_ref") {
		t.Errorf("expected client_secret_ref error, got: %v", err)
	}
}

func TestLoad_GoogleMissingTokenRef(t *testing.T) {
	p := writeTOML(t, `
[calendar.google]
client_id_ref     = "cid"
client_secret_ref = "cs"
`)
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "token_ref") {
		t.Errorf("expected token_ref error, got: %v", err)
	}
}

func TestLoad_FilesRelativePath(t *testing.T) {
	p := writeTOML(t, `
[files]
rules_file = "relative/path"
`)
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "absolute path") {
		t.Errorf("expected absolute path error, got: %v", err)
	}
}

func TestLoad_FilesNegativeMaxRead(t *testing.T) {
	p := writeTOML(t, `
[files]
rules_file     = "/tmp/access.rules"
max_read_bytes = -1
`)
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "non-negative") {
		t.Errorf("expected non-negative error, got: %v", err)
	}
}

func TestLoad_FileNotFound(t *testing.T) {
	_, err := Load("/nonexistent/config.toml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoad_InvalidTOML(t *testing.T) {
	p := writeTOML(t, `[broken`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected parse error")
	}
}

func TestLoad_DraftsMailboxDefault(t *testing.T) {
	p := writeTOML(t, `
[mail.icloud]
host       = "imap.mail.me.com"
port       = 993
username   = "user@icloud.com"
secret_ref = "pw"
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	// DraftsMailbox is empty when not set; the driver defaults it.
	if cfg.Mail.ICloud.DraftsMailbox != "" {
		t.Errorf("expected empty drafts_mailbox, got %q", cfg.Mail.ICloud.DraftsMailbox)
	}
}
