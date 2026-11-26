package google

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"google.golang.org/api/calendar/v3"

	domain "github.com/kai/freestone/internal/calendar"
)

// storedAllDay is a one-day all-day event as Google stores it.
var storedAllDay = &calendar.Event{
	Id:      "evt-1",
	Summary: "Holiday",
	Status:  "confirmed",
	Start:   &calendar.EventDateTime{Date: "2026-11-26"},
	End:     &calendar.EventDateTime{Date: "2026-11-27"},
}

// Patching the times of an all-day event keeps it all-day: the driver reads
// the stored event, then sends start.date/end.date from each value's own
// wall clock (midnight +01:00 is the previous day in UTC) and no dateTime.
func TestUpdateEvent_AllDayTimesSendDates(t *testing.T) {
	h := newCalendarAPIHarness(t)
	h.respond(http.MethodGet, patchPath, http.StatusOK, storedAllDay)
	h.respond(http.MethodPatch, patchPath, http.StatusOK, &calendar.Event{
		Id: "evt-1", Summary: "Holiday", Status: "confirmed",
		Start: &calendar.EventDateTime{Date: "2026-11-28"},
		End:   &calendar.EventDateTime{Date: "2026-11-29"},
	})

	cet := time.FixedZone("CET", 3600)
	start := time.Date(2026, 11, 28, 0, 0, 0, 0, cet)
	end := time.Date(2026, 11, 29, 0, 0, 0, 0, cet)
	updated, err := h.driver().UpdateEvent(context.Background(), domain.EventPatch{
		CalendarID: "primary", UID: "evt-1", Start: &start, End: &end,
	})
	if err != nil {
		t.Fatalf("UpdateEvent: %v", err)
	}

	reqs := h.recorded()
	if len(reqs) != 2 || reqs[0].Method != http.MethodGet || reqs[1].Method != http.MethodPatch {
		t.Fatalf("requests = %+v, want GET then PATCH", reqs)
	}
	body := reqs[1].JSON(t)
	for field, want := range map[string]string{"start": "2026-11-28", "end": "2026-11-29"} {
		if got := body[field]; !reflect.DeepEqual(got, map[string]any{"date": want}) {
			t.Errorf("%s = %#v, want only date %s", field, got, want)
		}
	}
	if !updated.AllDay {
		t.Errorf("returned event AllDay = false, want true")
	}
}

// An all-day end on the start's own date is an empty range; it is refused
// before anything is written.
func TestUpdateEvent_AllDayEndNotAfterStartIsError(t *testing.T) {
	h := newCalendarAPIHarness(t)
	h.respond(http.MethodGet, patchPath, http.StatusOK, storedAllDay)

	cet := time.FixedZone("CET", 3600)
	start := time.Date(2026, 11, 28, 9, 0, 0, 0, cet)
	end := time.Date(2026, 11, 28, 17, 0, 0, 0, cet)
	_, err := h.driver().UpdateEvent(context.Background(), domain.EventPatch{
		CalendarID: "primary", UID: "evt-1", Start: &start, End: &end,
	})
	if err == nil || !strings.Contains(err.Error(), "all-day end date 2026-11-28 must be after start date 2026-11-28") {
		t.Fatalf("err = %v, want the all-day end-date error", err)
	}
	if got := h.only(); got.Method != http.MethodGet {
		t.Errorf("request = %s %s, want only the GET and no PATCH", got.Method, got.Path)
	}
}
