//go:build darwin

package secrets

import (
	"fmt"
	"os/exec"
	"strings"
)

// KeychainResolver resolves secrets from the macOS Keychain using
// /usr/bin/security.
type KeychainResolver struct{}

// Resolve looks up a generic password by service name (ref).
func (k *KeychainResolver) Resolve(ref string) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("secrets: empty reference")
	}

	cmd := exec.Command("/usr/bin/security", "find-generic-password", "-s", ref, "-w")
	// Suppress stderr to prevent leaking account names or diagnostics from
	// /usr/bin/security into logs or MCP responses.
	cmd.Stderr = nil
	out, err := cmd.Output()
	if err != nil {
		// Do not wrap the exec.ExitError (which contains stderr) — return a
		// sanitized error with only the ref name.
		return "", fmt.Errorf("secrets: keychain lookup for %q failed", ref)
	}

	val := strings.TrimSpace(string(out))
	if val == "" {
		return "", fmt.Errorf("secrets: keychain returned empty value for %q", ref)
	}
	return val, nil
}
