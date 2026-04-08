// Package gauth provides Google OAuth2 helpers for freestone.
// It builds oauth2.Config, runs a local browser-based auth flow,
// and persists tokens to macOS Keychain.
package gauth

import (
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// NewConfig creates an oauth2.Config for Google APIs.
// RedirectURL is set to a placeholder; RunLocalFlow overwrites it
// with the actual loopback address.
func NewConfig(clientID, clientSecret string, scopes []string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Scopes:       scopes,
		Endpoint:     google.Endpoint,
		RedirectURL:  "http://127.0.0.1",
	}
}
