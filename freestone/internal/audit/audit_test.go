package audit

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLogger_WritesJSONL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")

	l, err := New(path)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().Truncate(time.Millisecond)
	rec := Record{
		Time:     now,
		Tool:     "calendar_create_event",
		Provider: "google",
		Inputs:   map[string]string{"calendar_id": "primary", "summary": "Test"},
		Outcome:  "ok",
	}
	if err := l.Log(rec); err != nil {
		t.Fatal(err)
	}

	errRec := Record{
		Time:    now.Add(time.Second),
		Tool:    "files_write_file",
		Outcome: "error",
		Error:   "access denied",
	}
	if err := l.Log(errRec); err != nil {
		t.Fatal(err)
	}

	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	// Verify file contents.
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	var records []Record
	for scanner.Scan() {
		var r Record
		if err := json.Unmarshal(scanner.Bytes(), &r); err != nil {
			t.Fatalf("unmarshal line: %v", err)
		}
		records = append(records, r)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}

	if len(records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(records))
	}

	if records[0].Tool != "calendar_create_event" {
		t.Errorf("record[0].Tool = %q, want calendar_create_event", records[0].Tool)
	}
	if records[0].Provider != "google" {
		t.Errorf("record[0].Provider = %q, want google", records[0].Provider)
	}
	if records[0].Outcome != "ok" {
		t.Errorf("record[0].Outcome = %q, want ok", records[0].Outcome)
	}

	if records[1].Tool != "files_write_file" {
		t.Errorf("record[1].Tool = %q, want files_write_file", records[1].Tool)
	}
	if records[1].Outcome != "error" {
		t.Errorf("record[1].Outcome = %q, want error", records[1].Outcome)
	}
	if records[1].Error != "access denied" {
		t.Errorf("record[1].Error = %q, want access denied", records[1].Error)
	}
}

func TestLogger_FilePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")

	l, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	l.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	perm := info.Mode().Perm()
	if perm != 0o600 {
		t.Errorf("file mode = %o, want 0600", perm)
	}
}
