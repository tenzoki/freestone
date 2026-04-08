package secrets

import (
	"fmt"
	"os"
	"strings"
)

// EnvResolver resolves secrets from environment variables.
// The ref is uppercased and hyphens are replaced with underscores.
// For example, ref "icloud-calendar-pw" looks up ICLOUD_CALENDAR_PW.
type EnvResolver struct{}

// Resolve looks up an environment variable derived from ref.
func (e *EnvResolver) Resolve(ref string) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("secrets: empty reference")
	}

	envKey := strings.ToUpper(strings.ReplaceAll(ref, "-", "_"))
	val := os.Getenv(envKey)
	if val == "" {
		return "", fmt.Errorf("secrets: env var %s (from ref %q) is empty or not set", envKey, ref)
	}
	return val, nil
}
