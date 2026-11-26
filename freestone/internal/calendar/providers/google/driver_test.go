package google

import (
	"context"
	"net/http"
	"testing"
	"time"

	"google.golang.org/api/calendar/v3"

	domain "github.com/kai/freestone/internal/calendar"
)

func TestEventFromGoogle_TimedEvent(t *testing.T) {
	item := &calendar.Event{
		Id:          "evt-123",
		Summary:     "Team Standup",
		Description: "Daily sync",
		Location:    "Room A",
		Status:      "confirmed",
		Organizer: &calendar.EventOrganizer{
			Email:       "alice@example.com",
			DisplayName: "Alice",
		},
		Attendees: []*calendar.EventAttendee{
			{Email: "bob@example.com"},
			{Email: "carol@example.com"},
		},
		Start: &calendar.EventDateTime{
			DateTime: "2026-04-08T10:00:00Z",
			TimeZone: "Europe/Berlin",
		},
		End: &calendar.EventDateTime{
			DateTime: "2026-04-08T10:30:00Z",
			TimeZone: "Europe/Berlin",
		},
	}

	ev, err := eventFromGoogle(item)
	if err != nil {
		t.Fatal(err)
	}

	if ev.Provider != "google" {
		t.Errorf("provider = %q, want google", ev.Provider)
	}
	if ev.UID != "evt-123" {
		t.Errorf("uid = %q, want evt-123", ev.UID)
	}
	if ev.Summary != "Team Standup" {
		t.Errorf("summary = %q", ev.Summary)
	}
	if ev.Location != "Room A" {
		t.Errorf("location = %q", ev.Location)
	}
	if ev.Status != "CONFIRMED" {
		t.Errorf("status = %q, want CONFIRMED", ev.Status)
	}
	if ev.Organizer != "Alice" {
		t.Errorf("organizer = %q, want Alice", ev.Organizer)
	}
	if len(ev.Attendees) != 2 {
		t.Errorf("attendees len = %d", len(ev.Attendees))
	}
	if ev.AllDay {
		t.Error("expected timed event, got all-day")
	}

	expectedStart := time.Date(2026, 4, 8, 10, 0, 0, 0, time.UTC)
	if !ev.StartUTC.Equal(expectedStart) {
		t.Errorf("startUTC = %v, want %v", ev.StartUTC, expectedStart)
	}
	if ev.TZID != "Europe/Berlin" {
		t.Errorf("tzid = %q, want Europe/Berlin", ev.TZID)
	}
	if ev.StartLocal == "" {
		t.Error("expected non-empty StartLocal")
	}
}

func TestEventFromGoogle_AllDayEvent(t *testing.T) {
	item := &calendar.Event{
		Id:      "allday-1",
		Summary: "Company Holiday",
		Status:  "confirmed",
		Start: &calendar.EventDateTime{
			Date: "2026-12-25",
		},
		End: &calendar.EventDateTime{
			Date: "2026-12-26",
		},
	}

	ev, err := eventFromGoogle(item)
	if err != nil {
		t.Fatal(err)
	}

	if !ev.AllDay {
		t.Error("expected all-day event")
	}
	if ev.StartLocal != "2026-12-25" {
		t.Errorf("startLocal = %q, want 2026-12-25", ev.StartLocal)
	}
	expectedStart := time.Date(2026, 12, 25, 0, 0, 0, 0, time.UTC)
	if !ev.StartUTC.Equal(expectedStart) {
		t.Errorf("startUTC = %v", ev.StartUTC)
	}
}

func TestEventFromGoogle_RecurringInstance(t *testing.T) {
	item := &calendar.Event{
		Id:               "evt-123_20260408T100000Z",
		Summary:          "Weekly Sync",
		Status:           "confirmed",
		RecurringEventId: "evt-123",
		Start: &calendar.EventDateTime{
			DateTime: "2026-04-08T10:00:00Z",
		},
		End: &calendar.EventDateTime{
			DateTime: "2026-04-08T11:00:00Z",
		},
		OriginalStartTime: &calendar.EventDateTime{
			DateTime: "2026-04-08T10:00:00Z",
		},
	}

	ev, err := eventFromGoogle(item)
	if err != nil {
		t.Fatal(err)
	}

	if ev.RecurringParentUID != "evt-123" {
		t.Errorf("recurringParentUID = %q, want evt-123", ev.RecurringParentUID)
	}
	expectedInstance := time.Date(2026, 4, 8, 10, 0, 0, 0, time.UTC)
	if !ev.InstanceStartUTC.Equal(expectedInstance) {
		t.Errorf("instanceStartUTC = %v, want %v", ev.InstanceStartUTC, expectedInstance)
	}
}

func TestEventFromGoogle_OrganizerFallsBackToEmail(t *testing.T) {
	item := &calendar.Event{
		Id:      "evt-org",
		Summary: "Meeting",
		Status:  "confirmed",
		Organizer: &calendar.EventOrganizer{
			Email: "noreply@example.com",
		},
		Start: &calendar.EventDateTime{
			DateTime: "2026-04-08T14:00:00Z",
		},
		End: &calendar.EventDateTime{
			DateTime: "2026-04-08T15:00:00Z",
		},
	}

	ev, err := eventFromGoogle(item)
	if err != nil {
		t.Fatal(err)
	}

	if ev.Organizer != "noreply@example.com" {
		t.Errorf("organizer = %q, want email fallback", ev.Organizer)
	}
}

func TestParseDateTimeField(t *testing.T) {
	utc, local, tzid, err := parseDateTimeField("2026-04-08T12:00:00+02:00", "Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	expectedUTC := time.Date(2026, 4, 8, 10, 0, 0, 0, time.UTC)
	if !utc.Equal(expectedUTC) {
		t.Errorf("utc = %v, want %v", utc, expectedUTC)
	}
	if tzid != "Europe/Berlin" {
		t.Errorf("tzid = %q", tzid)
	}
	if local == "" {
		t.Error("expected non-empty local")
	}
}

func TestParseDateTimeField_NoTimezone(t *testing.T) {
	utc, local, tzid, err := parseDateTimeField("2026-04-08T10:00:00Z", "")
	if err != nil {
		t.Fatal(err)
	}
	if !utc.Equal(time.Date(2026, 4, 8, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("utc = %v", utc)
	}
	if local != "2026-04-08T10:00:00Z" {
		t.Errorf("local = %q, want original string", local)
	}
	if tzid != "" {
		t.Errorf("tzid = %q, want empty", tzid)
	}
}

// D11: a window given with an offset is sent as its UTC instant, not as
// its wall clock relabelled "Z".
func TestListEvents_WindowWithOffsetSentAsUTC(t *testing.T) {
	h := newCalendarAPIHarness(t)
	h.respond(http.MethodGet, "/calendars/primary/events", http.StatusOK, &calendar.Events{})

	plusOne := time.FixedZone("+01:00", 60*60)
	_, err := h.driver().ListEvents(context.Background(), domain.EventQuery{
		CalendarID: "primary",
		Start:      time.Date(2026, 11, 26, 0, 0, 0, 0, plusOne),
		End:        time.Date(2026, 11, 27, 0, 0, 0, 0, plusOne),
	})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}

	req := h.only()
	if got := req.Query.Get("timeMin"); got != "2026-11-25T23:00:00Z" {
		t.Errorf("timeMin = %q, want 2026-11-25T23:00:00Z", got)
	}
	if got := req.Query.Get("timeMax"); got != "2026-11-26T23:00:00Z" {
		t.Errorf("timeMax = %q, want 2026-11-26T23:00:00Z", got)
	}
}
