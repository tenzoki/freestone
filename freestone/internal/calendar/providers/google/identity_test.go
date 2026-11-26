package google

import (
	"context"
	"net/http"
	"testing"
	"time"

	"google.golang.org/api/calendar/v3"

	domain "github.com/kai/freestone/internal/calendar"
)

// D13: events read from Google carry the calendar they were read from, and
// the organizer as an address, not a display name.
var d13Event = &calendar.Event{
	Id:        "evt-org",
	Summary:   "Review",
	Status:    "confirmed",
	Organizer: &calendar.EventOrganizer{Email: "alice@example.com", DisplayName: "Alice"},
	Start:     &calendar.EventDateTime{DateTime: "2026-11-26T09:00:00Z"},
	End:       &calendar.EventDateTime{DateTime: "2026-11-26T10:00:00Z"},
}

func assertD13Identity(t *testing.T, ev domain.Event, calendarID string) {
	t.Helper()
	if ev.CalendarID != calendarID {
		t.Errorf("calendar_id = %q, want %q", ev.CalendarID, calendarID)
	}
	if ev.Organizer != "alice@example.com" {
		t.Errorf("organizer = %q, want the address alice@example.com", ev.Organizer)
	}
}

func TestListEvents_CarriesCalendarIDAndOrganizerAddress(t *testing.T) {
	h := newCalendarAPIHarness(t)
	h.respond(http.MethodGet, "/calendars/work@example.com/events", http.StatusOK, &calendar.Events{
		Items: []*calendar.Event{d13Event},
	})

	list, err := h.driver().ListEvents(context.Background(), domain.EventQuery{
		CalendarID: "work@example.com",
		Start:      time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC),
		End:        time.Date(2026, 11, 27, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(list.Events) != 1 {
		t.Fatalf("events = %+v, want one", list.Events)
	}
	assertD13Identity(t, list.Events[0], "work@example.com")
}

func TestGetEvent_CarriesCalendarIDAndOrganizerAddress(t *testing.T) {
	h := newCalendarAPIHarness(t)
	h.respond(http.MethodGet, "/calendars/work@example.com/events/evt-org", http.StatusOK, d13Event)

	ev, err := h.driver().GetEvent(context.Background(), "work@example.com", "evt-org")
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	assertD13Identity(t, *ev, "work@example.com")
}
