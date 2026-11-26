package mcpsrv_test

import (
	"encoding/json"
	"strings"
	"testing"
)

// createAllDay creates an all-day event and returns the stored event, or
// the error text when the tool rejected the call.
func createAllDay(t *testing.T, start, end string) (storedEvent, string) {
	t.Helper()
	session, ctx := buildTestServer(t)
	result := callTool(t, ctx, session, "calendar_create_event", map[string]any{
		"provider":    "icloud",
		"calendar_id": "/cal/personal/",
		"summary":     "Holiday",
		"start":       start,
		"end":         end,
		"all_day":     true,
	})
	if result.IsError {
		return storedEvent{}, extractText(result)
	}
	var created struct {
		UID string `json:"uid"`
	}
	if err := json.Unmarshal([]byte(extractText(result)), &created); err != nil || created.UID == "" {
		t.Fatalf("create result %q: uid missing (%v)", extractText(result), err)
	}
	return getEvent(t, ctx, session, created.UID), ""
}

func TestCalendarCreateEvent_AllDayDateFromCallerWallClock(t *testing.T) {
	e, errText := createAllDay(t, "2026-11-26T00:00:00+01:00", "2026-11-27T00:00:00+01:00")
	if errText != "" {
		t.Fatalf("create: %s", errText)
	}
	if e.StartUTC != "2026-11-26T00:00:00Z" {
		t.Errorf("start_utc = %s, want 2026-11-26T00:00:00Z (the caller's date, not the 25th)", e.StartUTC)
	}
	if e.EndUTC != "2026-11-27T00:00:00Z" {
		t.Errorf("end_utc = %s, want 2026-11-27T00:00:00Z", e.EndUTC)
	}
}

func TestCalendarCreateEvent_AllDayAcceptsPlainDate(t *testing.T) {
	e, errText := createAllDay(t, "2026-11-26", "2026-11-27")
	if errText != "" {
		t.Fatalf("create: %s", errText)
	}
	if e.StartUTC != "2026-11-26T00:00:00Z" || e.EndUTC != "2026-11-27T00:00:00Z" {
		t.Errorf("start/end = %s / %s, want 2026-11-26 / 2026-11-27 at UTC midnight", e.StartUTC, e.EndUTC)
	}
}

func TestCalendarCreateEvent_AllDayEndNotAfterStartIsError(t *testing.T) {
	_, errText := createAllDay(t, "2026-11-26", "2026-11-26")
	if errText == "" {
		t.Fatal("create: want an error for an all-day end equal to start (end is exclusive)")
	}
	if !strings.Contains(errText, "after start") {
		t.Errorf("error %q does not say end must be after start", errText)
	}
}

func TestCalendarCreateEvent_TimedPlainDateIsError(t *testing.T) {
	session, ctx := buildTestServer(t)
	result := callTool(t, ctx, session, "calendar_create_event", map[string]any{
		"provider": "icloud", "calendar_id": "/cal/personal/", "summary": "Call",
		"start": "2026-11-26", "end": "2026-11-27",
	})
	if !result.IsError {
		t.Fatalf("create: want an error for a plain date on a timed event, got %s", extractText(result))
	}
}
