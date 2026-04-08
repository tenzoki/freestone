package gauth

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sync"

	"golang.org/x/oauth2"

	"github.com/kai/freestone/internal/secrets"
)

// LoadToken reads a JSON-serialized oauth2.Token from Keychain via the resolver.
func LoadToken(ref string, resolver secrets.Resolver) (*oauth2.Token, error) {
	raw, err := resolver.Resolve(ref)
	if err != nil {
		return nil, fmt.Errorf("gauth: load token %q: %w", ref, err)
	}
	var tok oauth2.Token
	if err := json.Unmarshal([]byte(raw), &tok); err != nil {
		return nil, fmt.Errorf("gauth: decode token %q: %w", ref, err)
	}
	return &tok, nil
}

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
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("gauth: save token to keychain %q: %s: %w", ref, string(out), err)
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
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("gauth: delete token from keychain %q: %s: %w", ref, string(out), err)
	}
	return nil
}

// TokenSource returns an oauth2.TokenSource that loads the token from Keychain,
// auto-refreshes it, and writes back rotated refresh tokens.
func TokenSource(ctx context.Context, cfg *oauth2.Config, ref string, resolver secrets.Resolver) (oauth2.TokenSource, error) {
	tok, err := LoadToken(ref, resolver)
	if err != nil {
		return nil, err
	}
	return &persistingTokenSource{
		src: cfg.TokenSource(ctx, tok),
		ref: ref,
	}, nil
}

// persistingTokenSource wraps an oauth2.TokenSource and writes back
// any new refresh token to Keychain when token rotation occurs.
type persistingTokenSource struct {
	mu  sync.Mutex
	src oauth2.TokenSource
	ref string
	// lastRefresh tracks the last refresh token we persisted, to avoid
	// redundant writes.
	lastRefresh string
}

func (p *persistingTokenSource) Token() (*oauth2.Token, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	tok, err := p.src.Token()
	if err != nil {
		return nil, err
	}

	// If the refresh token rotated, persist it.
	if tok.RefreshToken != "" && tok.RefreshToken != p.lastRefresh {
		if err := SaveToken(p.ref, tok); err != nil {
			return nil, fmt.Errorf("gauth: persist rotated token: %w", err)
		}
		p.lastRefresh = tok.RefreshToken
	}

	return tok, nil
}
