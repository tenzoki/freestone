//go:build darwin

package gauth

import (
	"encoding/json"
	"fmt"
	"os/exec"

	"golang.org/x/oauth2"
)

// SaveToken writes a JSON-serialized oauth2.Token to macOS Keychain.
// It uses -U (update) so it replaces any existing entry.
func SaveToken(ref string, tok *oauth2.Token) error {
	data, err := json.Marshal(tok)
	if err != nil {
		return fmt.Errorf("gauth: encode token: %w", err)
	}
	cmd := exec.Command(
		"/usr/bin/security", "add-generic-password",
		"-U",            // update if exists
		"-s", ref,       // service name
		"-a", "freestone", // account
		"-w", string(data), // password (the JSON blob)
	)
	cmd.Stderr = nil // suppress stderr to avoid leaking keychain metadata
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gauth: save token to keychain %q: %w", ref, err)
	}
	return nil
}

// DeleteToken removes a token entry from macOS Keychain.
func DeleteToken(ref string) error {
	cmd := exec.Command(
		"/usr/bin/security", "delete-generic-password",
		"-s", ref,
		"-a", "freestone",
	)
	cmd.Stderr = nil // suppress stderr to avoid leaking keychain metadata
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gauth: delete token from keychain %q: %w", ref, err)
	}
	return nil
}
