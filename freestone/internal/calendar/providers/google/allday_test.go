package google

import (
	"context"
	"net/http"
	"testing"
	"time"

	"google.golang.org/api/calendar/v3"

	domain "github.com/kai/freestone/internal/calendar"
)

// TestCreateEvent_AllDaySendsDates pins the convention the tool relies on
// (D9): an all-day domain event carries its date as UTC midnight, and the
// driver sends that date as start.date/end.date with no dateTime.
func TestCreateEvent_AllDaySendsDates(t *testing.T) {
	h := newCalendarAPIHarness(t)
	h.respond(http.MethodPost, "/calendars/primary/events", http.StatusOK, &calendar.Event{
		Id:      "evt-allday",
		Summary: "Holiday",
		Start:   &calendar.EventDateTime{Date: "2026-11-26"},
		End:     &calendar.EventDateTime{Date: "2026-11-27"},
	})

	created, err := h.driver().CreateEvent(context.Background(), "primary", domain.Event{
		Summary:  "Holiday",
		StartUTC: time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC),
		EndUTC:   time.Date(2026, 11, 27, 0, 0, 0, 0, time.UTC),
		AllDay:   true,
	})
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}

	body := h.only().JSON(t)
	for _, field := range []string{"start", "end"} {
		dt, _ := body[field].(map[string]any)
		want := map[string]string{"start": "2026-11-26", "end": "2026-11-27"}[field]
		if dt["date"] != want {
			t.Errorf("%s.date = %v, want %s (body %v)", field, dt["date"], want, body)
		}
		if _, ok := dt["dateTime"]; ok {
			t.Errorf("%s.dateTime = %v, want none on an all-day event", field, dt["dateTime"])
		}
	}
	if !created.AllDay {
		t.Errorf("returned event AllDay = false, want true")
	}
}
