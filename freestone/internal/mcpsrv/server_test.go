package mcpsrv_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kai/freestone/internal/calendar"
	"github.com/kai/freestone/internal/files"
	"github.com/kai/freestone/internal/mail"
	"github.com/kai/freestone/internal/mcpsrv"
)

func buildTestServer(t *testing.T) (*mcp.ClientSession, context.Context) {
	t.Helper()
	ctx := context.Background()

	calFake := &calendar.Fake{
		ProviderName: "icloud",
		Calendars: []calendar.Calendar{
			{ID: "/cal/personal/", Name: "Personal"},
		},
		Events: map[string][]calendar.Event{
			"/cal/personal/": {
				{
					UID:      "evt-1",
					Summary:  "Test Event",
					StartUTC: time.Date(2026, 4, 8, 10, 0, 0, 0, time.UTC),
					EndUTC:   time.Date(2026, 4, 8, 11, 0, 0, 0, time.UTC),
				},
			},
		},
	}
	calReg := calendar.NewFakeRegistry(calFake)

	mailFake := &mail.Fake{
		ProviderName: "icloud",
		Mailboxes: []mail.Mailbox{
			{ID: "INBOX", Name: "Inbox", Messages: 5},
		},
		Messages: map[string][]mail.MessageSummary{
			"INBOX": {
				{ID: "1", Subject: "Hello", From: "alice@test.com", Date: time.Now()},
			},
		},
		FullMessages: map[string]*mail.Message{
			"1": {
				MessageSummary: mail.MessageSummary{ID: "1", Subject: "Hello"},
				Text:           "Hello world",
			},
		},
	}
	mailReg := mail.NewFakeRegistry(mailFake)

	fakeFS := files.NewFakeFS()
	fakeFS.Dirs["/tmp/test"] = true
	fakeFS.Files["/tmp/test/readme.txt"] = []byte("test content")

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	srv := mcpsrv.Build(mcpsrv.Deps{
		Calendar: calReg,
		Mail:     mailReg,
		Files:    fakeFS,
		Log:      log,
	})

	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	// Start server in background.
	go func() {
		srv.Run(ctx, serverTransport)
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })

	return session, ctx
}

func TestListTools(t *testing.T) {
	session, ctx := buildTestServer(t)

	result, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}

	expectedTools := []string{
		"calendar_list_calendars",
		"calendar_list_events",
		"calendar_get_event",
		"mail_list_mailboxes",
		"mail_list_messages",
		"mail_get_message",
		"files_list_dir",
		"files_read_file",
		"files_stat",
		"files_glob",
	}

	toolNames := make(map[string]bool)
	for _, tool := range result.Tools {
		toolNames[tool.Name] = true
	}

	for _, name := range expectedTools {
		if !toolNames[name] {
			t.Errorf("missing tool: %s", name)
		}
	}
}

func TestCalendarListCalendars(t *testing.T) {
	session, ctx := buildTestServer(t)

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "calendar_list_calendars",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatal("expected success")
	}
	text := extractText(result)
	if !strings.Contains(text, "Personal") {
		t.Errorf("expected 'Personal' in result, got: %s", text)
	}
}

func TestCalendarListCalendarsWithProvider(t *testing.T) {
	session, ctx := buildTestServer(t)

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "calendar_list_calendars",
		Arguments: map[string]any{"provider": "icloud"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatal("expected success")
	}
	text := extractText(result)
	if !strings.Contains(text, "Personal") {
		t.Errorf("expected 'Personal', got: %s", text)
	}
}

func TestCalendarListEvents(t *testing.T) {
	session, ctx := buildTestServer(t)

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "calendar_list_events",
		Arguments: map[string]any{
			"provider":    "icloud",
			"calendar_id": "/cal/personal/",
			"start":       "2026-04-08T00:00:00Z",
			"end":         "2026-04-09T00:00:00Z",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("expected success, got: %s", extractText(result))
	}
	text := extractText(result)
	if !strings.Contains(text, "Test Event") {
		t.Errorf("expected 'Test Event', got: %s", text)
	}
}

func TestCalendarGetEvent(t *testing.T) {
	session, ctx := buildTestServer(t)

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "calendar_get_event",
		Arguments: map[string]any{
			"provider":    "icloud",
			"calendar_id": "/cal/personal/",
			"uid":         "evt-1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("expected success, got: %s", extractText(result))
	}
}

func TestMailListMailboxes(t *testing.T) {
	session, ctx := buildTestServer(t)

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "mail_list_mailboxes",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatal("expected success")
	}
	text := extractText(result)
	if !strings.Contains(text, "INBOX") {
		t.Errorf("expected 'INBOX', got: %s", text)
	}
}

func TestMailListMessages(t *testing.T) {
	session, ctx := buildTestServer(t)

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "mail_list_messages",
		Arguments: map[string]any{
			"provider":   "icloud",
			"mailbox_id": "INBOX",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("expected success, got: %s", extractText(result))
	}
	text := extractText(result)
	if !strings.Contains(text, "Hello") {
		t.Errorf("expected 'Hello', got: %s", text)
	}
}

func TestMailGetMessage(t *testing.T) {
	session, ctx := buildTestServer(t)

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "mail_get_message",
		Arguments: map[string]any{
			"provider":   "icloud",
			"mailbox_id": "INBOX",
			"id":         "1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("expected success, got: %s", extractText(result))
	}
	text := extractText(result)
	if !strings.Contains(text, "Hello world") {
		t.Errorf("expected 'Hello world', got: %s", text)
	}
}

func TestFilesListDir(t *testing.T) {
	session, ctx := buildTestServer(t)

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "files_list_dir",
		Arguments: map[string]any{
			"path": "/tmp/test",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("expected success, got: %s", extractText(result))
	}
	text := extractText(result)
	if !strings.Contains(text, "readme.txt") {
		t.Errorf("expected 'readme.txt', got: %s", text)
	}
}

func TestFilesReadFile(t *testing.T) {
	session, ctx := buildTestServer(t)

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "files_read_file",
		Arguments: map[string]any{
			"path": "/tmp/test/readme.txt",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("expected success, got: %s", extractText(result))
	}
	text := extractText(result)
	if text != "test content" {
		t.Errorf("expected 'test content', got: %q", text)
	}
}

func TestFilesStat(t *testing.T) {
	session, ctx := buildTestServer(t)

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "files_stat",
		Arguments: map[string]any{
			"path": "/tmp/test/readme.txt",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("expected success, got: %s", extractText(result))
	}
}

func TestFilesGlob(t *testing.T) {
	session, ctx := buildTestServer(t)

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "files_glob",
		Arguments: map[string]any{
			"pattern": "/tmp/test/*.txt",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("expected success, got: %s", extractText(result))
	}
}

// -- Phase 1B multi-provider tests --

func buildMultiProviderServer(t *testing.T) (*mcp.ClientSession, context.Context) {
	t.Helper()
	ctx := context.Background()

	icloudCal := &calendar.Fake{
		ProviderName: "icloud",
		Calendars: []calendar.Calendar{
			{ID: "/cal/personal/", Name: "Personal"},
		},
		Events: map[string][]calendar.Event{
			"/cal/personal/": {
				{
					UID:      "ic-evt-1",
					Summary:  "iCloud Event",
					StartUTC: time.Date(2026, 4, 8, 10, 0, 0, 0, time.UTC),
					EndUTC:   time.Date(2026, 4, 8, 11, 0, 0, 0, time.UTC),
				},
			},
		},
	}
	googleCal := &calendar.Fake{
		ProviderName: "google",
		Calendars: []calendar.Calendar{
			{ID: "primary", Name: "Work"},
		},
		Events: map[string][]calendar.Event{
			"primary": {
				{
					UID:      "g-evt-1",
					Summary:  "Google Event",
					StartUTC: time.Date(2026, 4, 8, 14, 0, 0, 0, time.UTC),
					EndUTC:   time.Date(2026, 4, 8, 15, 0, 0, 0, time.UTC),
				},
			},
		},
	}
	calReg := calendar.NewFakeRegistry(icloudCal, googleCal)

	icloudMail := &mail.Fake{
		ProviderName: "icloud",
		Mailboxes: []mail.Mailbox{
			{ID: "INBOX", Name: "Inbox", Messages: 5},
		},
		Messages: map[string][]mail.MessageSummary{
			"INBOX": {
				{ID: "ic-1", Subject: "iCloud Mail", From: "alice@icloud.com", Date: time.Now()},
			},
		},
		FullMessages: map[string]*mail.Message{
			"ic-1": {
				MessageSummary: mail.MessageSummary{ID: "ic-1", Subject: "iCloud Mail"},
				Text:           "From iCloud",
			},
		},
	}
	googleMail := &mail.Fake{
		ProviderName: "google",
		Mailboxes: []mail.Mailbox{
			{ID: "INBOX", Name: "Inbox", Messages: 10},
		},
		Messages: map[string][]mail.MessageSummary{
			"INBOX": {
				{ID: "g-1", Subject: "Gmail Message", From: "bob@gmail.com", Date: time.Now()},
			},
		},
		FullMessages: map[string]*mail.Message{
			"g-1": {
				MessageSummary: mail.MessageSummary{ID: "g-1", Subject: "Gmail Message"},
				Text:           "From Gmail",
			},
		},
	}
	mailReg := mail.NewFakeRegistry(icloudMail, googleMail)

	fakeFS := files.NewFakeFS()
	fakeFS.Dirs["/tmp/test"] = true
	fakeFS.Files["/tmp/test/readme.txt"] = []byte("test content")

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	srv := mcpsrv.Build(mcpsrv.Deps{
		Calendar: calReg,
		Mail:     mailReg,
		Files:    fakeFS,
		Log:      log,
	})

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	go func() { srv.Run(ctx, serverTransport) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session, ctx
}

func TestMultiProvider_ListCalendarsAll(t *testing.T) {
	session, ctx := buildMultiProviderServer(t)

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "calendar_list_calendars",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("expected success, got: %s", extractText(result))
	}
	text := extractText(result)
	if !strings.Contains(text, "Personal") {
		t.Errorf("expected 'Personal' (icloud), got: %s", text)
	}
	if !strings.Contains(text, "Work") {
		t.Errorf("expected 'Work' (google), got: %s", text)
	}
}

func TestMultiProvider_ListEventsPerProvider(t *testing.T) {
	session, ctx := buildMultiProviderServer(t)

	// iCloud events
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "calendar_list_events",
		Arguments: map[string]any{
			"provider":    "icloud",
			"calendar_id": "/cal/personal/",
			"start":       "2026-04-08T00:00:00Z",
			"end":         "2026-04-09T00:00:00Z",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("icloud: expected success, got: %s", extractText(result))
	}
	text := extractText(result)
	if !strings.Contains(text, "iCloud Event") {
		t.Errorf("expected 'iCloud Event', got: %s", text)
	}

	// Google events
	result, err = session.CallTool(ctx, &mcp.CallToolParams{
		Name: "calendar_list_events",
		Arguments: map[string]any{
			"provider":    "google",
			"calendar_id": "primary",
			"start":       "2026-04-08T00:00:00Z",
			"end":         "2026-04-09T00:00:00Z",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("google: expected success, got: %s", extractText(result))
	}
	text = extractText(result)
	if !strings.Contains(text, "Google Event") {
		t.Errorf("expected 'Google Event', got: %s", text)
	}
}

func TestMultiProvider_ListMailboxesAll(t *testing.T) {
	session, ctx := buildMultiProviderServer(t)

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "mail_list_mailboxes",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("expected success, got: %s", extractText(result))
	}
	text := extractText(result)
	// Both providers have INBOX; verify we get at least 2 entries.
	if strings.Count(text, "INBOX") < 2 {
		t.Errorf("expected at least 2 INBOX entries (one per provider), got: %s", text)
	}
}

func TestMultiProvider_ListMessagesPerProvider(t *testing.T) {
	session, ctx := buildMultiProviderServer(t)

	// iCloud messages
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "mail_list_messages",
		Arguments: map[string]any{
			"provider":   "icloud",
			"mailbox_id": "INBOX",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("icloud: expected success, got: %s", extractText(result))
	}
	text := extractText(result)
	if !strings.Contains(text, "iCloud Mail") {
		t.Errorf("expected 'iCloud Mail', got: %s", text)
	}

	// Google messages
	result, err = session.CallTool(ctx, &mcp.CallToolParams{
		Name: "mail_list_messages",
		Arguments: map[string]any{
			"provider":   "google",
			"mailbox_id": "INBOX",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("google: expected success, got: %s", extractText(result))
	}
	text = extractText(result)
	if !strings.Contains(text, "Gmail Message") {
		t.Errorf("expected 'Gmail Message', got: %s", text)
	}
}

func TestMultiProvider_GetMessagePerProvider(t *testing.T) {
	session, ctx := buildMultiProviderServer(t)

	// iCloud
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "mail_get_message",
		Arguments: map[string]any{
			"provider":   "icloud",
			"mailbox_id": "INBOX",
			"id":         "ic-1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("icloud: expected success, got: %s", extractText(result))
	}
	if !strings.Contains(extractText(result), "From iCloud") {
		t.Errorf("expected 'From iCloud'")
	}

	// Google
	result, err = session.CallTool(ctx, &mcp.CallToolParams{
		Name: "mail_get_message",
		Arguments: map[string]any{
			"provider":   "google",
			"mailbox_id": "INBOX",
			"id":         "g-1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("google: expected success, got: %s", extractText(result))
	}
	if !strings.Contains(extractText(result), "From Gmail") {
		t.Errorf("expected 'From Gmail'")
	}
}

func TestUnknownProvider(t *testing.T) {
	session, ctx := buildTestServer(t)

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "calendar_list_calendars",
		Arguments: map[string]any{
			"provider": "nonexistent",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("expected error for unknown provider")
	}
}

func extractText(result *mcp.CallToolResult) string {
	for _, c := range result.Content {
		data, _ := json.Marshal(c)
		var m map[string]any
		json.Unmarshal(data, &m)
		if text, ok := m["text"].(string); ok {
			return text
		}
	}
	return ""
}
