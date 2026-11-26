package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/kai/freestone/internal/audit"
)

// openAuditLog opens the audit log that serve hands to every write tool.
// configured is server.audit_file; when it is empty the log goes to
// defaultAuditPath, whose directory is created if missing. A log that cannot
// be opened is an error: serving writes without their audit trail is not an
// option the caller gets silently.
func openAuditLog(configured string) (*audit.Logger, string, error) {
	path := configured
	if path == "" {
		def, err := defaultAuditPath()
		if err != nil {
			return nil, "", err
		}
		if err := os.MkdirAll(filepath.Dir(def), 0o700); err != nil {
			return nil, "", fmt.Errorf("audit log: create directory for %s: %w", def, err)
		}
		path = def
	}
	al, err := audit.New(path)
	if err != nil {
		return nil, "", err
	}
	return al, path, nil
}

// defaultAuditPath is ~/.config/freestone/audit.jsonl, beside the default
// config file.
func defaultAuditPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("audit log: server.audit_file is empty and the home directory is unknown: %w", err)
	}
	return filepath.Join(home, ".config", "freestone", "audit.jsonl"), nil
}
