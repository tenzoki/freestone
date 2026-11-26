package icloud

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"

	"github.com/kai/freestone/internal/calendar"
)

const partialRetrievalObject = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//harness//EN
BEGIN:VEVENT
UID:uid-1
DTSTAMP:20261101T080000Z
DTSTART:20261126T090000Z
DTEND:20261126T100000Z
SUMMARY:Standup
BEGIN:VALARM
ACTION:DISPLAY
TRIGGER:-PT15M
END:VALARM
END:VEVENT
END:VCALENDAR
`

// TestCalDAVHarness_ICloudStripsNestedAllProp pins the iCloud-fidelity
// mode: the partial-retrieval request ListEvents used to send comes back
// with a VEVENT that has its VALARM and no properties at all.
func TestCalDAVHarness_ICloudStripsNestedAllProp(t *testing.T) {
	h := newCalDAVHarness(t).withICloudPartialRetrieval()
	cal := h.addCalendar("personal", "Personal")
	h.seedObject(cal+"uid-1.ics", partialRetrievalObject)
	d := h.driver()

	objs, err := d.client.QueryCalendar(context.Background(), cal, &caldav.CalendarQuery{
		CompRequest: caldav.CalendarCompRequest{
			Name: "VCALENDAR", AllProps: true,
			Comps: []caldav.CalendarCompRequest{{Name: "VEVENT", AllProps: true}},
		},
		CompFilter: caldav.CompFilter{Name: "VCALENDAR", Comps: []caldav.CompFilter{{Name: "VEVENT"}}},
	})
	if err != nil {
		t.Fatalf("QueryCalendar: %v", err)
	}
	if len(objs) != 1 || len(objs[0].Data.Children) != 1 {
		t.Fatalf("QueryCalendar = %+v, want one object with one VEVENT", objs)
	}
	vevent := objs[0].Data.Children[0]
	if len(vevent.Props) != 0 {
		t.Errorf("VEVENT props = %v, want none", vevent.Props)
	}
	if len(vevent.Children) != 1 || vevent.Children[0].Name != ical.CompAlarm {
		t.Errorf("VEVENT sub-components = %+v, want [VALARM]", vevent.Children)
	}
}

// TestDecodeEvents_RejectsStrippedVEvent: the object iCloud returned live
// decodes to an error, which ListEvents turns into a warning, not to an
// event with no UID, title or times.
func TestDecodeEvents_RejectsStrippedVEvent(t *testing.T) {
	h := newCalDAVHarness(t).withICloudPartialRetrieval()
	cal := h.addCalendar("personal", "Personal")
	h.seedObject(cal+"uid-1.ics", partialRetrievalObject)
	d := h.driver()

	objs, err := d.client.QueryCalendar(context.Background(), cal, &caldav.CalendarQuery{
		CompRequest: caldav.CalendarCompRequest{
			Name: "VCALENDAR", AllProps: true,
			Comps: []caldav.CalendarCompRequest{{Name: "VEVENT", AllProps: true}},
		},
		CompFilter: caldav.CompFilter{Name: "VCALENDAR", Comps: []caldav.CompFilter{{Name: "VEVENT"}}},
	})
	if err != nil || len(objs) != 1 {
		t.Fatalf("QueryCalendar = %d objects, %v; want 1", len(objs), err)
	}
	events, err := decodeEvents(objs[0].Data, objs[0].ETag, cal, time.Time{}, time.Time{}, time.UTC)
	if err == nil || !strings.Contains(err.Error(), "no UID") {
		t.Fatalf("decodeEvents = %+v, %v; want an error naming the missing UID", events, err)
	}
}

// TestListEvents_ICloudPartialRetrieval is the regression for iCloud
// listing events with no UID, title or times: against a server that strips
// VEVENT properties from a partial-retrieval answer, ListEvents must still
// return the whole event, so it must not send such a request.
func TestListEvents_ICloudPartialRetrieval(t *testing.T) {
	h := newCalDAVHarness(t).withICloudPartialRetrieval()
	cal := h.addCalendar("personal", "Personal")
	h.seedObject(cal+"uid-1.ics", partialRetrievalObject)
	d := h.driver()

	list, err := d.ListEvents(context.Background(), calendar.EventQuery{
		CalendarID: cal,
		Start:      harnessTime("2026-11-26T00:00:00Z"),
		End:        harnessTime("2026-11-27T00:00:00Z"),
	})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(list.Warnings) != 0 {
		t.Errorf("Warnings = %v, want none", list.Warnings)
	}
	if len(list.Events) != 1 {
		t.Fatalf("ListEvents returned %d events, want 1: %+v", len(list.Events), list.Events)
	}
	e := list.Events[0]
	if e.UID != "uid-1" || e.Summary != "Standup" || !e.StartUTC.Equal(harnessTime("2026-11-26T09:00:00Z")) || !e.EndUTC.Equal(harnessTime("2026-11-26T10:00:00Z")) {
		t.Errorf("event = %+v, want uid-1 Standup 09:00-10:00Z", e)
	}
}

const (
	veventNoUID = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//harness//EN
BEGIN:VEVENT
DTSTAMP:20261101T080000Z
DTSTART:20261126T110000Z
SUMMARY:No UID
END:VEVENT
END:VCALENDAR
`
	veventNoDTStart = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//harness//EN
BEGIN:VEVENT
UID:uid-nostart
DTSTAMP:20261101T080000Z
SUMMARY:No start
END:VEVENT
END:VCALENDAR
`
)

func TestDecodeVEvent_RejectsMissingUIDOrDTStart(t *testing.T) {
	for _, tc := range []struct {
		name, ics, want string
	}{
		{"no UID", veventNoUID, "no UID"},
		{"no DTSTART", veventNoDTStart, "no DTSTART"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := strings.ReplaceAll(tc.ics, "\n", "\r\n")
			cal, err := ical.NewDecoder(strings.NewReader(text)).Decode()
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if _, err := decodeVEvent(cal.Children[0], "", "/cal/", time.UTC); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("decodeVEvent err = %v, want %q", err, tc.want)
			}
		})
	}
}

// TestListEvents_SkipsVEventWithoutDTStart: an object whose VEVENT has no
// DTSTART is skipped and named in Warnings; the good object is still
// listed. The no-UID case cannot be served: go-webdav's handler encodes
// with go-ical, which refuses a VEVENT without UID, so it is covered on the
// decoder (TestDecodeEvents_RejectsStrippedVEvent and above).
func TestListEvents_SkipsVEventWithoutDTStart(t *testing.T) {
	h := newCalDAVHarness(t)
	cal := h.addCalendar("personal", "Personal")
	h.seedObject(cal+"uid-1.ics", partialRetrievalObject)
	h.seedObject(cal+"no-start.ics", veventNoDTStart)
	d := h.driver()

	// No window: the server's time-range test cannot place a VEVENT without
	// DTSTART, and the point here is what the decoder does with it.
	list, err := d.ListEvents(context.Background(), calendar.EventQuery{CalendarID: cal})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(list.Events) != 1 || list.Events[0].UID != "uid-1" {
		t.Errorf("Events = %+v, want only uid-1", list.Events)
	}
	if len(list.Warnings) != 1 || !strings.Contains(list.Warnings[0], cal+"no-start.ics") || !strings.Contains(list.Warnings[0], "no DTSTART") {
		t.Errorf("Warnings = %q, want one naming %sno-start.ics and the missing DTSTART", list.Warnings, cal)
	}
}

func TestGetEvent_RejectsVEventWithoutDTStart(t *testing.T) {
	h := newCalDAVHarness(t)
	cal := h.addCalendar("personal", "Personal")
	h.seedObject(cal+"uid-nostart.ics", veventNoDTStart)
	d := h.driver()

	e, err := d.GetEvent(context.Background(), cal, "uid-nostart")
	if err == nil || !strings.Contains(err.Error(), "no DTSTART") {
		t.Fatalf("GetEvent = %+v, %v; want an error naming the missing DTSTART", e, err)
	}
	if e != nil {
		t.Errorf("GetEvent returned an event alongside the error: %+v", e)
	}
}
