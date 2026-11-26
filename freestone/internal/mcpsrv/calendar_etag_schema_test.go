package mcpsrv_test

import (
	"encoding/json"
	"strings"
	"testing"
)

// The etag input is honoured by both providers, so its description must not
// limit it to iCloud, and it must tell the caller to pass the value back
// unchanged.
func TestCalendarWriteTools_ETagDescription(t *testing.T) {
	session, ctx := buildTestServer(t)
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, tool := range tools.Tools {
		if tool.Name != "calendar_update_event" && tool.Name != "calendar_delete_event" {
			continue
		}
		seen[tool.Name] = true
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema struct {
			Properties map[string]struct {
				Desc string `json:"description"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		desc := schema.Properties["etag"].Desc
		if strings.Contains(desc, "iCloud only") {
			t.Errorf("%s etag description = %q, still limited to iCloud", tool.Name, desc)
		}
		if !strings.Contains(desc, "exactly as received") || !strings.Contains(desc, "re-fetched") {
			t.Errorf("%s etag description = %q, want it to say to pass the value back as received and to re-fetch on conflict", tool.Name, desc)
		}
	}
	if !seen["calendar_update_event"] || !seen["calendar_delete_event"] {
		t.Fatalf("tools seen = %v, want both calendar_update_event and calendar_delete_event", seen)
	}
}
