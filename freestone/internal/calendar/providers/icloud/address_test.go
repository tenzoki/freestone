package icloud

import (
	"context"
	"reflect"
	"testing"

	"github.com/kai/freestone/internal/calendar"
)

// D13: ORGANIZER and ATTENDEE are CAL-ADDRESS URIs; the domain carries the
// bare address, as Google does. The scheme match is case-insensitive.
const d13Event = `UID:addr-1
SUMMARY:Review
DTSTART:20261202T090000Z
DTEND:20261202T100000Z
ORGANIZER;CN=Anna:mailto:a@b.de
ATTENDEE;PARTSTAT=ACCEPTED:MAILTO:c@d.de
ATTENDEE:urn:uuid:7f1c0e2a`

func assertBareAddresses(t *testing.T, e calendar.Event) {
	t.Helper()
	if e.Organizer != "a@b.de" {
		t.Errorf("organizer = %q, want a@b.de", e.Organizer)
	}
	// A non-mailto URI is not an address to strip and is kept as is.
	want := []string{"c@d.de", "urn:uuid:7f1c0e2a"}
	if !reflect.DeepEqual(e.Attendees, want) {
		t.Errorf("attendees = %q, want %q", e.Attendees, want)
	}
}

func TestDecodeVEvent_StripsMailtoFromAddresses(t *testing.T) {
	events := d5Decode(t, d5Calendar(t, d13Event))
	if len(events) != 1 {
		t.Fatalf("events = %+v, want one", events)
	}
	assertBareAddresses(t, events[0])
}

func TestCalDAV_ListedAddressesAreBare(t *testing.T) {
	h := newCalDAVHarness(t)
	cal := h.addCalendar("personal", "Personal")
	d5Seed(h, cal+"addr.ics", d13Event)

	list, err := h.driver().ListEvents(context.Background(), calendar.EventQuery{
		CalendarID: cal,
		Start:      harnessTime("2026-12-01T00:00:00Z"),
		End:        harnessTime("2026-12-31T00:00:00Z"),
	})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(list.Events) != 1 {
		t.Fatalf("events = %+v, want one", list.Events)
	}
	assertBareAddresses(t, list.Events[0])
}
