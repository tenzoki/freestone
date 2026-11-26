package google

import (
	"context"
	"net/http"
	"reflect"
	"sort"
	"testing"
	"time"

	"google.golang.org/api/calendar/v3"

	domain "github.com/kai/freestone/internal/calendar"
)

const patchPath = "/calendars/primary/events/evt-1"

// patchedEvent is what Google answers to a patch: the whole stored event,
// fields the patch did not name included.
var patchedEvent = &calendar.Event{
	Id:          "evt-1",
	Summary:     "Standup v2",
	Description: "Daily sync",
	Location:    "Room A",
	Status:      "confirmed",
	Recurrence:  []string{"RRULE:FREQ=WEEKLY;BYDAY=TH"},
	Start:       &calendar.EventDateTime{DateTime: "2026-11-26T09:00:00+01:00", TimeZone: "Europe/Berlin"},
	End:         &calendar.EventDateTime{DateTime: "2026-11-26T10:00:00+01:00", TimeZone: "Europe/Berlin"},
}

// updateSummary is a title-only update, the call D1 broke.
func updateSummary(ctx context.Context, d *Driver, calendarID, uid, summary string) (*domain.Event, error) {
	return d.UpdateEvent(ctx, domain.EventPatch{CalendarID: calendarID, UID: uid, Summary: &summary})
}

func bodyKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestUpdateEvent_TitleOnlySendsPatchWithSummaryOnly(t *testing.T) {
	h := newCalendarAPIHarness(t)
	h.respond(http.MethodPatch, patchPath, http.StatusOK, patchedEvent)

	updated, err := updateSummary(context.Background(), h.driver(), "primary", "evt-1", "Standup v2")
	if err != nil {
		t.Fatalf("UpdateEvent: %v", err)
	}

	req := h.only()
	if req.Method != http.MethodPatch {
		t.Errorf("method = %s, want PATCH", req.Method)
	}
	body := req.JSON(t)
	if got := bodyKeys(body); !reflect.DeepEqual(got, []string{"summary"}) {
		t.Errorf("body fields = %v, want [summary] (body %s)", got, req.Body)
	}
	if body["summary"] != "Standup v2" {
		t.Errorf("summary = %v", body["summary"])
	}
	if updated.Description != "Daily sync" || updated.CalendarID != "primary" {
		t.Errorf("returned event = %+v, want the stored description and calendar primary", updated)
	}
}

func TestUpdateEvent_EmptyStringClearsAndTimesAreSent(t *testing.T) {
	h := newCalendarAPIHarness(t)
	h.respond(http.MethodPatch, patchPath, http.StatusOK, patchedEvent)

	empty := ""
	berlin := time.FixedZone("CET", 3600)
	start := time.Date(2026, 11, 26, 14, 30, 0, 0, berlin)
	end := start.Add(30 * time.Minute)
	_, err := h.driver().UpdateEvent(context.Background(), domain.EventPatch{
		CalendarID: "primary", UID: "evt-1", Description: &empty, Start: &start, End: &end,
	})
	if err != nil {
		t.Fatalf("UpdateEvent: %v", err)
	}

	body := h.only().JSON(t)
	if got := bodyKeys(body); !reflect.DeepEqual(got, []string{"description", "end", "start"}) {
		t.Errorf("body fields = %v, want [description end start]", got)
	}
	if v, ok := body["description"]; !ok || v != "" {
		t.Errorf("description = %#v, want an explicit empty string", v)
	}
	want := map[string]any{"dateTime": "2026-11-26T14:30:00+01:00"}
	if !reflect.DeepEqual(body["start"], want) {
		t.Errorf("start = %#v, want %#v", body["start"], want)
	}
}
