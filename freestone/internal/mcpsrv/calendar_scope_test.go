package mcpsrv_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kai/freestone/internal/calendar"
	"github.com/kai/freestone/internal/mcpsrv"
)

// The update and delete tools declare scope, delete also recurrence_id,
// and each description says what the input means per provider.
func TestCalendarWriteTools_ScopeInputs(t *testing.T) {
	session, ctx := buildTestServer(t)
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"calendar_update_event": {"scope"},
		"calendar_delete_event": {"scope", "recurrence_id"},
	}
	seen := 0
	for _, tool := range tools.Tools {
		inputs, ok := want[tool.Name]
		if !ok {
			continue
		}
		seen++
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema struct {
			Properties map[string]struct {
				Type json.RawMessage `json:"type"`
				Desc string          `json:"description"`
			} `json:"properties"`
			Required []string `json:"required"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		for _, in := range inputs {
			p, ok := schema.Properties[in]
			if !ok {
				t.Errorf("%s: input %s missing from schema", tool.Name, in)
				continue
			}
			if string(p.Type) != `"string"` {
				t.Errorf("%s.%s type = %s, want string", tool.Name, in, p.Type)
			}
			for _, word := range []string{"iCloud", "google"} {
				if !strings.Contains(p.Desc, word) {
					t.Errorf("%s.%s description = %q, want it to mention %s", tool.Name, in, p.Desc, word)
				}
			}
			for _, r := range schema.Required {
				if r == in {
					t.Errorf("%s.%s is required, want optional", tool.Name, in)
				}
			}
		}
		for _, word := range []string{"iCloud", "Google", `"series"`} {
			if !strings.Contains(tool.Description, word) {
				t.Errorf("%s description = %q, want it to mention %s", tool.Name, tool.Description, word)
			}
		}
	}
	if seen != len(want) {
		t.Fatalf("saw %d of the tools %v", seen, want)
	}
}

// scopeRecorder records the scope and recurrence ID each write receives.
type scopeRecorder struct {
	*calendar.Fake
	patches   []calendar.EventPatch
	deletions []calendar.EventDeletion
}

func (p *scopeRecorder) UpdateEvent(_ context.Context, patch calendar.EventPatch) (*calendar.Event, error) {
	p.patches = append(p.patches, patch)
	return &calendar.Event{UID: patch.UID}, nil
}

func (p *scopeRecorder) DeleteEvent(_ context.Context, del calendar.EventDeletion) error {
	p.deletions = append(p.deletions, del)
	return nil
}

func TestCalendarWriteTools_ScopeReachesProvider(t *testing.T) {
	ctx := context.Background()
	rec := &scopeRecorder{Fake: &calendar.Fake{ProviderName: "icloud", Events: map[string][]calendar.Event{}}}
	reg, err := calendar.NewRegistry(rec)
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

	base := map[string]any{"provider": "icloud", "calendar_id": "/cal/", "uid": "uid-1"}
	with := func(extra map[string]any) map[string]any {
		m := map[string]any{}
		for k, v := range base {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}

	if r := callTool(t, ctx, session, "calendar_update_event", with(map[string]any{"summary": "x", "scope": "series"})); r.IsError {
		t.Fatalf("update: %s", extractText(r))
	}
	if r := callTool(t, ctx, session, "calendar_delete_event", with(map[string]any{"scope": "instance", "recurrence_id": "2026-12-03T09:00:00+01:00"})); r.IsError {
		t.Fatalf("delete: %s", extractText(r))
	}
	if len(rec.patches) != 1 || rec.patches[0].Scope != calendar.ScopeSeries {
		t.Errorf("patches = %+v, want one with scope series", rec.patches)
	}
	wantRID := time.Date(2026, 12, 3, 8, 0, 0, 0, time.UTC)
	if len(rec.deletions) != 1 || rec.deletions[0].Scope != calendar.ScopeInstance || !rec.deletions[0].RecurrenceID.Equal(wantRID) {
		t.Errorf("deletions = %+v, want one with scope instance at %v", rec.deletions, wantRID)
	}

	for _, tc := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"calendar_update_event", with(map[string]any{"summary": "x", "scope": "all"}), "unknown scope"},
		{"calendar_delete_event", with(map[string]any{"scope": "both"}), "unknown scope"},
		{"calendar_delete_event", with(map[string]any{"scope": "instance", "recurrence_id": "2026-12-03"}), "invalid recurrence_id"},
	} {
		r := callTool(t, ctx, session, tc.tool, tc.args)
		if !r.IsError || !strings.Contains(extractText(r), tc.want) {
			t.Errorf("%s %v: result %q (error %v), want an error containing %q", tc.tool, tc.args, extractText(r), r.IsError, tc.want)
		}
	}
	if len(rec.patches) != 1 || len(rec.deletions) != 1 {
		t.Errorf("invalid inputs reached the provider: patches %d, deletions %d", len(rec.patches), len(rec.deletions))
	}
}
