//go:build !darwin

package gauth

import (
	"fmt"
	"runtime"

	"golang.org/x/oauth2"
)

// SaveToken is not supported on non-macOS platforms.
func SaveToken(ref string, _ *oauth2.Token) error {
	return fmt.Errorf("gauth: SaveToken requires macOS Keychain (current OS: %s)", runtime.GOOS)
}

// DeleteToken is not supported on non-macOS platforms.
func DeleteToken(ref string) error {
	return fmt.Errorf("gauth: DeleteToken requires macOS Keychain (current OS: %s)", runtime.GOOS)
}
