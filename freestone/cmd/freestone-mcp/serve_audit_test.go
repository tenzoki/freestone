package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kai/freestone/internal/audit"
)

// TestMain lets a test re-execute this binary as `freestone-mcp serve`, so
// the audit tests drive the real runServe wiring over stdio.
func TestMain(m *testing.M) {
	if os.Getenv("FREESTONE_TEST_RUN_SERVE") == "1" {
		if err := runServe(os.Args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "freestone-mcp: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// serveFixture writes a files-only config whose sandbox is a fresh directory
// and returns the config path and that directory. auditLine is placed in
// [server] verbatim.
func serveFixture(t *testing.T, auditLine string) (configPath, sandbox string) {
	t.Helper()
	dir := t.TempDir()
	sandbox, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rules := filepath.Join(dir, "access.rules")
	if err := os.WriteFile(rules, []byte("+ "+sandbox+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath = filepath.Join(dir, "config.toml")
	cfg := fmt.Sprintf("[server]\n%s\n\n[files]\nrules_file = %q\n", auditLine, rules)
	if err := os.WriteFile(configPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath, sandbox
}

func serveCommand(t *testing.T, configPath, home string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-config", configPath)
	cmd.Env = append(os.Environ(), "FREESTONE_TEST_RUN_SERVE=1", "FREESTONE_SECRETS=env", "HOME="+home)
	return cmd
}

// writeThroughServe starts serve, writes one file through files_write_file
// and shuts serve down by closing its stdin.
func writeThroughServe(t *testing.T, configPath, home, target string) {
	t.Helper()
	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: serveCommand(t, configPath, home)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "files_write_file",
		Arguments: map[string]any{"path": target, "content": "hello"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("files_write_file failed: %+v", res.Content)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("serve did not shut down cleanly: %v", err)
	}
}

func readAudit(t *testing.T, path string) []audit.Record {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("audit log: %v", err)
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

func assertOneWriteRecord(t *testing.T, recs []audit.Record, target string) {
	t.Helper()
	if len(recs) != 1 {
		t.Fatalf("got %d audit records, want 1: %+v", len(recs), recs)
	}
	if r := recs[0]; r.Tool != "files_write_file" || r.Outcome != "ok" || r.Inputs["path"] != target {
		t.Errorf("audit record = %+v, want an ok files_write_file of %s", r, target)
	}
}

func TestServe_AuditsWritesToConfiguredFile(t *testing.T) {
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	configPath, sandbox := serveFixture(t, fmt.Sprintf("audit_file = %q", auditPath))
	target := filepath.Join(sandbox, "note.txt")

	writeThroughServe(t, configPath, t.TempDir(), target)

	assertOneWriteRecord(t, readAudit(t, auditPath), target)
}

func TestServe_AuditsWritesToDefaultFile(t *testing.T) {
	home := t.TempDir()
	configPath, sandbox := serveFixture(t, "")
	target := filepath.Join(sandbox, "note.txt")

	writeThroughServe(t, configPath, home, target)

	assertOneWriteRecord(t, readAudit(t, filepath.Join(home, ".config", "freestone", "audit.jsonl")), target)
}

func TestServe_FailsWhenAuditFileCannotBeOpened(t *testing.T) {
	auditPath := filepath.Join(t.TempDir(), "missing-dir", "audit.jsonl")
	configPath, _ := serveFixture(t, fmt.Sprintf("audit_file = %q", auditPath))

	cmd := serveCommand(t, configPath, t.TempDir())
	cmd.Stdin = strings.NewReader("")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("serve started without its audit log; output:\n%s", out)
	}
	if !strings.Contains(string(out), "audit: open "+auditPath) {
		t.Errorf("startup error does not name the audit log:\n%s", out)
	}
}
