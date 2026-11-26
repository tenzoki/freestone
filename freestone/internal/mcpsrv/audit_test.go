package mcpsrv_test

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kai/freestone/internal/audit"
	"github.com/kai/freestone/internal/calendar"
	"github.com/kai/freestone/internal/files"
	"github.com/kai/freestone/internal/mail"
	"github.com/kai/freestone/internal/mcpsrv"
)

// readAuditRecords returns every JSONL record in path.
func readAuditRecords(t *testing.T, path string) []audit.Record {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var recs []audit.Record
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r audit.Record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("audit line %q: %v", sc.Text(), err)
		}
		recs = append(recs, r)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return recs
}

// Every write tool, called through the MCP transport, leaves exactly one
// record in the audit log, with its outcome.
func TestAudit_EveryWriteToolIsRecorded(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	al, err := audit.New(path)
	if err != nil {
		t.Fatal(err)
	}

	calFake := &calendar.Fake{
		ProviderName: "icloud",
		Calendars:    []calendar.Calendar{{ID: "/cal/personal/", Name: "Personal"}},
		Events: map[string][]calendar.Event{
			"/cal/personal/": {
				{UID: "evt-1", Summary: "One", StartUTC: time.Date(2026, 4, 8, 10, 0, 0, 0, time.UTC), EndUTC: time.Date(2026, 4, 8, 11, 0, 0, 0, time.UTC)},
				{UID: "evt-2", Summary: "Two", StartUTC: time.Date(2026, 4, 9, 10, 0, 0, 0, time.UTC), EndUTC: time.Date(2026, 4, 9, 11, 0, 0, 0, time.UTC)},
			},
		},
	}
	mailFake := &mail.Fake{
		ProviderName: "icloud",
		Mailboxes:    []mail.Mailbox{{ID: "INBOX", Name: "Inbox"}},
		Messages:     map[string][]mail.MessageSummary{"INBOX": {{ID: "1", Subject: "Hello"}}},
	}
	fakeFS := files.NewFakeFS()
	fakeFS.Dirs["/tmp/test"] = true
	fakeFS.Files["/tmp/test/readme.txt"] = []byte("existing")

	srv := mcpsrv.Build(mcpsrv.Deps{
		Calendar: calendar.NewFakeRegistry(calFake),
		Mail:     mail.NewFakeRegistry(mailFake),
		Files:    fakeFS,
		Audit:    al,
		Log:      slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	})
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	go srv.Run(ctx, serverTransport) //nolint:errcheck
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v1"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	calls := []struct {
		tool    string
		args    map[string]any
		outcome string
	}{
		{"calendar_create_event", map[string]any{"provider": "icloud", "calendar_id": "/cal/personal/", "summary": "New", "start": "2026-04-10T09:00:00Z", "end": "2026-04-10T10:00:00Z"}, "ok"},
		{"calendar_update_event", map[string]any{"provider": "icloud", "calendar_id": "/cal/personal/", "uid": "evt-1", "summary": "Renamed"}, "ok"},
		{"calendar_delete_event", map[string]any{"provider": "icloud", "calendar_id": "/cal/personal/", "uid": "evt-2"}, "ok"},
		{"mail_save_draft_reply", map[string]any{"provider": "icloud", "subject": "Re: Hello", "body_text": "Thanks.", "to": []any{"alice@test.com"}}, "ok"},
		{"mail_mark_read", map[string]any{"provider": "icloud", "mailbox_id": "INBOX", "id": "1"}, "ok"},
		{"files_write_file", map[string]any{"path": "/tmp/test/new.txt", "content": "hello"}, "ok"},
		{"files_write_file", map[string]any{"path": "/tmp/test/readme.txt", "content": "no overwrite flag"}, "error"},
		{"files_make_dir", map[string]any{"path": "/tmp/test/sub"}, "ok"},
	}
	for _, c := range calls {
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: c.tool, Arguments: c.args})
		if err != nil {
			t.Fatalf("%s: %v", c.tool, err)
		}
		if res.IsError != (c.outcome == "error") {
			t.Fatalf("%s: IsError = %v, want outcome %s: %s", c.tool, res.IsError, c.outcome, extractText(res))
		}
	}
	if err := al.Close(); err != nil {
		t.Fatal(err)
	}

	recs := readAuditRecords(t, path)
	if len(recs) != len(calls) {
		t.Fatalf("got %d audit records, want %d: %+v", len(recs), len(calls), recs)
	}
	for i, c := range calls {
		r := recs[i]
		if r.Tool != c.tool || r.Outcome != c.outcome || r.Time.IsZero() {
			t.Errorf("record %d = %+v, want tool %s outcome %s", i, r, c.tool, c.outcome)
		}
		if c.outcome == "error" && r.Error == "" {
			t.Errorf("record %d: error outcome without error text", i)
		}
	}
}
