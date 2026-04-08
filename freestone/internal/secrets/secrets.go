// Package secrets resolves secret references to their values.
// The default resolver uses the macOS Keychain. An environment-variable
// resolver is available for tests and CI.
package secrets

import (
	"fmt"
	"os"
	"runtime"
)

// Resolver resolves a named secret reference to its plaintext value.
type Resolver interface {
	Resolve(ref string) (string, error)
}

// New returns the appropriate resolver for the current platform.
// If FREESTONE_SECRETS=env is set, it returns an EnvResolver (for tests).
func New() (Resolver, error) {
	if os.Getenv("FREESTONE_SECRETS") == "env" {
		return &EnvResolver{}, nil
	}
	if runtime.GOOS != "darwin" {
		return nil, fmt.Errorf("secrets: keychain resolver requires macOS; set FREESTONE_SECRETS=env for other platforms")
	}
	return &KeychainResolver{}, nil
}
