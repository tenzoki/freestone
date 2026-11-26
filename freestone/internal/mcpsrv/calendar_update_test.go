package mcpsrv_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// callTool calls a tool and returns its result; a transport error fails
// the test.
func callTool(t *testing.T, ctx context.Context, session *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return result
}

// createRichEvent creates an event with every text field set and returns
// its UID.
func createRichEvent(t *testing.T, ctx context.Context, session *mcp.ClientSession) string {
	t.Helper()
	result := callTool(t, ctx, session, "calendar_create_event", map[string]any{
		"provider":    "icloud",
		"calendar_id": "/cal/personal/",
		"summary":     "Planning",
		"description": "Quarterly planning",
		"location":    "Room 42",
		"start":       "2026-04-10T09:00:00Z",
		"end":         "2026-04-10T10:00:00Z",
	})
	if result.IsError {
		t.Fatalf("create: %s", extractText(result))
	}
	var created struct {
		UID string `json:"uid"`
	}
	if err := json.Unmarshal([]byte(extractText(result)), &created); err != nil || created.UID == "" {
		t.Fatalf("create result %q: uid missing (%v)", extractText(result), err)
	}
	return created.UID
}

type storedEvent struct {
	Summary     string `json:"summary"`
	Description string `json:"description"`
	Location    string `json:"location"`
	StartUTC    string `json:"start_utc"`
	EndUTC      string `json:"end_utc"`
}

func getEvent(t *testing.T, ctx context.Context, session *mcp.ClientSession, uid string) storedEvent {
	t.Helper()
	result := callTool(t, ctx, session, "calendar_get_event", map[string]any{
		"provider": "icloud", "calendar_id": "/cal/personal/", "uid": uid,
	})
	if result.IsError {
		t.Fatalf("get: %s", extractText(result))
	}
	var e storedEvent
	if err := json.Unmarshal([]byte(extractText(result)), &e); err != nil {
		t.Fatalf("get result %q: %v", extractText(result), err)
	}
	return e
}

func TestCalendarUpdateEvent_KeepsUnsetFields(t *testing.T) {
	session, ctx := buildTestServer(t)
	uid := createRichEvent(t, ctx, session)

	result := callTool(t, ctx, session, "calendar_update_event", map[string]any{
		"provider": "icloud", "calendar_id": "/cal/personal/", "uid": uid,
		"summary": "Planning v2",
	})
	if result.IsError {
		t.Fatalf("update: %s", extractText(result))
	}

	want := storedEvent{
		Summary:     "Planning v2",
		Description: "Quarterly planning",
		Location:    "Room 42",
		StartUTC:    "2026-04-10T09:00:00Z",
		EndUTC:      "2026-04-10T10:00:00Z",
	}
	if got := getEvent(t, ctx, session, uid); got != want {
		t.Errorf("after title-only update:\n got %+v\nwant %+v", got, want)
	}
}

func TestCalendarUpdateEvent_NullKeepsEmptyStringClears(t *testing.T) {
	session, ctx := buildTestServer(t)
	uid := createRichEvent(t, ctx, session)

	result := callTool(t, ctx, session, "calendar_update_event", map[string]any{
		"provider": "icloud", "calendar_id": "/cal/personal/", "uid": uid,
		"description": nil,
		"location":    "",
	})
	if result.IsError {
		t.Fatalf("update: %s", extractText(result))
	}

	got := getEvent(t, ctx, session, uid)
	if got.Description != "Quarterly planning" {
		t.Errorf("description = %q, want kept on null", got.Description)
	}
	if got.Location != "" {
		t.Errorf("location = %q, want cleared by empty string", got.Location)
	}
	if got.Summary != "Planning" {
		t.Errorf("summary = %q, want kept when omitted", got.Summary)
	}
}

func TestCalendarUpdateEvent_StartAndEndTogether(t *testing.T) {
	session, ctx := buildTestServer(t)
	uid := createRichEvent(t, ctx, session)
	base := map[string]any{"provider": "icloud", "calendar_id": "/cal/personal/", "uid": uid}
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

	for name, args := range map[string]map[string]any{
		"start only":       {"start": "2026-04-10T11:00:00Z"},
		"end only":         {"end": "2026-04-10T12:00:00Z"},
		"end before start": {"start": "2026-04-10T11:00:00Z", "end": "2026-04-10T10:00:00Z"},
	} {
		if result := callTool(t, ctx, session, "calendar_update_event", with(args)); !result.IsError {
			t.Errorf("%s: want error result, got %s", name, extractText(result))
		}
	}

	result := callTool(t, ctx, session, "calendar_update_event", with(map[string]any{
		"start": "2026-04-10T13:00:00+02:00", "end": "2026-04-10T14:00:00+02:00",
	}))
	if result.IsError {
		t.Fatalf("update times: %s", extractText(result))
	}
	got := getEvent(t, ctx, session, uid)
	if got.StartUTC != "2026-04-10T11:00:00Z" || got.EndUTC != "2026-04-10T12:00:00Z" {
		t.Errorf("times = %s / %s, want 11:00Z / 12:00Z", got.StartUTC, got.EndUTC)
	}
	if got.Description != "Quarterly planning" {
		t.Errorf("description = %q, want kept on a time-only update", got.Description)
	}
}

func TestCalendarUpdateEvent_SchemaFieldsNullable(t *testing.T) {
	session, ctx := buildTestServer(t)
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name != "calendar_update_event" {
			continue
		}
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema struct {
			Properties map[string]struct {
				Type  any    `json:"type"`
				Types []any  `json:"types"`
				Desc  string `json:"description"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"summary", "description", "location", "start", "end"} {
			p := schema.Properties[field]
			got := p.Types
			if got == nil {
				if list, ok := p.Type.([]any); ok {
					got = list
				}
			}
			if !reflect.DeepEqual(got, []any{"null", "string"}) {
				t.Errorf("%s schema type = %v / %v, want [null string] (schema %s)", field, p.Type, p.Types, raw)
			}
		}
		return
	}
	t.Fatal("calendar_update_event not listed")
}
