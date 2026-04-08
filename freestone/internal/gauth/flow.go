package gauth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os/exec"

	"golang.org/x/oauth2"
)

// RunLocalFlow runs an OAuth2 authorization code flow using a local
// loopback HTTP server to capture the redirect. It opens the user's
// default browser to the consent page and blocks until the flow completes
// or the context is cancelled.
func RunLocalFlow(ctx context.Context, cfg *oauth2.Config) (*oauth2.Token, error) {
	// Bind to a random port on loopback.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("gauth: listen: %w", err)
	}
	defer ln.Close()

	addr := ln.Addr().String()
	cfg.RedirectURL = "http://" + addr

	state, err := randomState()
	if err != nil {
		return nil, err
	}

	type result struct {
		code string
		err  error
	}
	ch := make(chan result, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("state") != state {
			ch <- result{err: fmt.Errorf("gauth: state mismatch")}
			http.Error(w, "state mismatch", http.StatusBadRequest)
			return
		}
		if errMsg := r.URL.Query().Get("error"); errMsg != "" {
			ch <- result{err: fmt.Errorf("gauth: oauth error: %s", errMsg)}
			fmt.Fprintf(w, "Authorization failed: %s. You can close this window.", errMsg)
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			ch <- result{err: fmt.Errorf("gauth: no code in callback")}
			http.Error(w, "missing code", http.StatusBadRequest)
			return
		}
		ch <- result{code: code}
		fmt.Fprint(w, "Authorization successful! You can close this window.")
	})

	srv := &http.Server{Handler: mux}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			ch <- result{err: fmt.Errorf("gauth: serve: %w", err)}
		}
	}()
	defer srv.Shutdown(context.Background()) //nolint:errcheck

	authURL := cfg.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.ApprovalForce)
	fmt.Printf("Opening browser for Google authorization...\n")
	fmt.Printf("If the browser does not open, visit:\n%s\n", authURL)
	_ = exec.Command("open", authURL).Start()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-ch:
		if res.err != nil {
			return nil, res.err
		}
		tok, err := cfg.Exchange(ctx, res.code)
		if err != nil {
			return nil, fmt.Errorf("gauth: exchange: %w", err)
		}
		return tok, nil
	}
}

func randomState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("gauth: random state: %w", err)
	}
	return hex.EncodeToString(b), nil
}
