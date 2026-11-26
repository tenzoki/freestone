package mcpsrv_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kai/freestone/internal/calendar"
	"github.com/kai/freestone/internal/mcpsrv"
)

// conflictProvider refuses every conditional write as a provider does for a
// stale etag, and records the etag it was given.
type conflictProvider struct {
	*calendar.Fake
	gotETags []string
}

func (p *conflictProvider) UpdateEvent(_ context.Context, patch calendar.EventPatch) (*calendar.Event, error) {
	p.gotETags = append(p.gotETags, patch.ETag)
	return nil, fmt.Errorf("stub: update event %s: %w", patch.UID, calendar.ErrConflict)
}

func (p *conflictProvider) DeleteEvent(_ context.Context, del calendar.EventDeletion) error {
	p.gotETags = append(p.gotETags, del.ETag)
	return fmt.Errorf("stub: delete event %s: %w", del.UID, calendar.ErrConflict)
}

// A stale etag reaches the caller as an error result, not a success and not
// a transport error, and its text says to re-fetch the event.
func TestCalendarWriteTools_ConflictIsErrorResultSayingReFetch(t *testing.T) {
	ctx := context.Background()
	stub := &conflictProvider{Fake: &calendar.Fake{ProviderName: "google", Events: map[string][]calendar.Event{}}}
	reg, err := calendar.NewRegistry(stub)
	if err != nil {
		t.Fatal(err)
	}
	srv := mcpsrv.Build(mcpsrv.Deps{Calendar: reg, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	go func() { srv.Run(ctx, serverTransport) }()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v1"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })

	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"calendar_update_event", map[string]any{"provider": "google", "calendar_id": "primary", "uid": "evt-1", "summary": "x", "etag": "stale"}},
		{"calendar_delete_event", map[string]any{"provider": "google", "calendar_id": "primary", "uid": "evt-1", "etag": "stale"}},
	} {
		result := callTool(t, ctx, session, tc.tool, tc.args)
		if !result.IsError {
			t.Errorf("%s: IsError = false, want an error result (%s)", tc.tool, extractText(result))
		}
		if text := extractText(result); !strings.Contains(text, "re-fetch") {
			t.Errorf("%s: text = %q, want it to tell the caller to re-fetch", tc.tool, text)
		}
	}
	if len(stub.gotETags) != 2 || stub.gotETags[0] != "stale" || stub.gotETags[1] != "stale" {
		t.Errorf("etags passed to the provider = %v, want [stale stale]", stub.gotETags)
	}
}
