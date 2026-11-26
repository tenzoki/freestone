package icloud

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/emersion/go-ical"

	"github.com/kai/freestone/internal/calendar"
)

// richEventICS is an object a real calendar client writes: a VTIMEZONE, a
// zoned recurring master with an alarm, an accepted attendee and a vendor
// property, and one moved instance. None of it but SUMMARY, DESCRIPTION,
// LOCATION, DTSTART and DTEND is modelled by calendar.Event.
const richEventICS = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//harness//EN
BEGIN:VTIMEZONE
TZID:Europe/Berlin
BEGIN:STANDARD
DTSTART:19701025T030000
TZOFFSETFROM:+0200
TZOFFSETTO:+0100
RRULE:FREQ=YEARLY;BYMONTH=10;BYDAY=-1SU
END:STANDARD
BEGIN:DAYLIGHT
DTSTART:19700329T020000
TZOFFSETFROM:+0100
TZOFFSETTO:+0200
RRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=-1SU
END:DAYLIGHT
END:VTIMEZONE
BEGIN:VEVENT
UID:uid-1
DTSTAMP:20261101T080000Z
DTSTART;TZID=Europe/Berlin:20261126T090000
DTEND;TZID=Europe/Berlin:20261126T100000
SEQUENCE:2
SUMMARY:Standup
DESCRIPTION:Daily sync
LOCATION:Room 42
RRULE:FREQ=WEEKLY;BYDAY=TH
ATTENDEE;PARTSTAT=ACCEPTED;CN=Alice:mailto:alice@example.com
X-APPLE-TRAVEL-ADVISORY-BEHAVIOR:AUTOMATIC
BEGIN:VALARM
ACTION:DISPLAY
DESCRIPTION:Reminder
TRIGGER:-PT15M
END:VALARM
END:VEVENT
BEGIN:VEVENT
UID:uid-1
RECURRENCE-ID;TZID=Europe/Berlin:20261203T090000
DTSTAMP:20261101T080000Z
DTSTART;TZID=Europe/Berlin:20261203T110000
DTEND;TZID=Europe/Berlin:20261203T120000
SUMMARY:Standup (moved)
END:VEVENT
END:VCALENDAR
`

// updateSummary is a title-only update of a whole series, the call D1
// broke.
func updateSummary(ctx context.Context, d *Driver, calendarID, uid, summary string) (*calendar.Event, error) {
	return d.UpdateEvent(ctx, calendar.EventPatch{CalendarID: calendarID, UID: uid, Summary: &summary, Scope: calendar.ScopeSeries})
}

// harnessComponent returns the index-th child called name, or reports it
// missing and returns nil, so one run lists every lost component.
func harnessComponent(t *testing.T, parent *ical.Component, name string, index int) *ical.Component {
	t.Helper()
	n := 0
	for _, c := range parent.Children {
		if c.Name == name {
			if n == index {
				return c
			}
			n++
		}
	}
	t.Errorf("%s #%d not found under %s", name, index, parent.Name)
	return nil
}

func propValue(t *testing.T, c *ical.Component, name string) string {
	t.Helper()
	p := c.Props.Get(name)
	if p == nil {
		t.Errorf("%s: property %s missing", c.Name, name)
		return ""
	}
	return p.Value
}

func TestUpdateEvent_TitleOnlyKeepsEverythingElse(t *testing.T) {
	h := newCalDAVHarness(t)
	cal := h.addCalendar("personal", "Personal")
	href := cal + "uid-1.ics"
	etag := h.seedObject(href, richEventICS)
	d := h.driver()

	updated, err := updateSummary(context.Background(), d, cal, "uid-1", "Standup v2")
	if err != nil {
		t.Fatalf("UpdateEvent: %v", err)
	}

	stored, ok := h.object(href)
	if !ok {
		t.Fatalf("object %s gone after update; stored: %v", href, h.hrefs())
	}
	if len(h.hrefs()) != 1 {
		t.Errorf("stored objects = %v, want only %s", h.hrefs(), href)
	}
	root := stored.Data.Component
	harnessComponent(t, root, ical.CompTimezone, 0)
	master := harnessComponent(t, root, ical.CompEvent, 0)
	if master == nil {
		t.FailNow()
	}

	if got := propValue(t, master, ical.PropSummary); got != "Standup v2" {
		t.Errorf("SUMMARY = %q, want Standup v2", got)
	}
	for name, want := range map[string]string{
		ical.PropDescription:               "Daily sync",
		ical.PropLocation:                  "Room 42",
		ical.PropDateTimeStart:             "20261126T090000",
		ical.PropDateTimeEnd:               "20261126T100000",
		ical.PropRecurrenceRule:            "FREQ=WEEKLY;BYDAY=TH",
		ical.PropAttendee:                  "mailto:alice@example.com",
		"X-APPLE-TRAVEL-ADVISORY-BEHAVIOR": "AUTOMATIC",
		ical.PropSequence:                  "3",
	} {
		if got := propValue(t, master, name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if p := master.Props.Get(ical.PropDateTimeStart); p != nil && p.Params.Get(ical.PropTimezoneID) != "Europe/Berlin" {
		t.Errorf("DTSTART TZID = %q, want Europe/Berlin", p.Params.Get(ical.PropTimezoneID))
	}
	if p := master.Props.Get(ical.PropAttendee); p != nil && p.Params.Get("PARTSTAT") != "ACCEPTED" {
		t.Errorf("ATTENDEE PARTSTAT = %q, want ACCEPTED", p.Params.Get("PARTSTAT"))
	}
	harnessComponent(t, master, ical.CompAlarm, 0)

	if override := harnessComponent(t, root, ical.CompEvent, 1); override != nil {
		if got := propValue(t, override, ical.PropSummary); got != "Standup (moved)" {
			t.Errorf("override SUMMARY = %q, want it untouched", got)
		}
	}

	puts := h.recordedWith(http.MethodPut)
	if len(puts) != 1 {
		t.Fatalf("PUT requests = %+v, want 1", puts)
	}
	if puts[0].Path != href {
		t.Errorf("PUT path = %s, want %s", puts[0].Path, href)
	}
	if want := strconv.Quote(etag); puts[0].IfMatch != want {
		t.Errorf("PUT If-Match = %q, want %q", puts[0].IfMatch, want)
	}

	if updated.ETag != stored.ETag {
		t.Errorf("returned ETag = %q, want the new stored %q", updated.ETag, stored.ETag)
	}
	if updated.StartUTC.IsZero() || updated.EndUTC.IsZero() {
		t.Errorf("returned start/end zeroed: %v / %v", updated.StartUTC, updated.EndUTC)
	}
}

func TestUpdateEvent_EmptyStringClearsAndTimesKeepZone(t *testing.T) {
	h := newCalDAVHarness(t)
	cal := h.addCalendar("personal", "Personal")
	href := cal + "uid-1.ics"
	h.seedObject(href, richEventICS)
	d := h.driver()

	empty := ""
	start := time.Date(2026, 11, 26, 13, 30, 0, 0, time.UTC) // 14:30 in Berlin
	end := start.Add(30 * time.Minute)
	_, err := d.UpdateEvent(context.Background(), calendar.EventPatch{
		CalendarID: cal, UID: "uid-1", Location: &empty, Start: &start, End: &end, Scope: calendar.ScopeSeries,
	})
	if err != nil {
		t.Fatalf("UpdateEvent: %v", err)
	}

	stored, _ := h.object(href)
	master := harnessComponent(t, stored.Data.Component, ical.CompEvent, 0)
	if master == nil {
		t.FailNow()
	}
	if p := master.Props.Get(ical.PropLocation); p != nil {
		t.Errorf("LOCATION = %q, want removed", p.Value)
	}
	if got := propValue(t, master, ical.PropDescription); got != "Daily sync" {
		t.Errorf("DESCRIPTION = %q, want kept", got)
	}
	for name, want := range map[string]string{
		ical.PropDateTimeStart: "20261126T143000",
		ical.PropDateTimeEnd:   "20261126T150000",
	} {
		p := master.Props.Get(name)
		if p == nil {
			t.Fatalf("%s missing", name)
		}
		if p.Value != want || p.Params.Get(ical.PropTimezoneID) != "Europe/Berlin" {
			t.Errorf("%s = %q TZID %q, want %q in Europe/Berlin", name, p.Value, p.Params.Get(ical.PropTimezoneID), want)
		}
	}
}

func TestGetAndDeleteEvent_GoThroughLoadObject(t *testing.T) {
	h := newCalDAVHarness(t)
	cal := h.addCalendar("personal", "Personal")
	href := cal + "uid-1.ics"
	etag := h.seedObject(href, richEventICS)
	d := h.driver()
	ctx := context.Background()

	got, err := d.GetEvent(ctx, cal, "uid-1")
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if got.UID != "uid-1" || got.ETag != etag {
		t.Errorf("GetEvent = uid %q etag %q, want uid-1 %q", got.UID, got.ETag, etag)
	}

	if err := d.DeleteEvent(ctx, calendar.EventDeletion{CalendarID: cal, UID: "uid-1", Scope: calendar.ScopeSeries}); err != nil {
		t.Fatalf("DeleteEvent: %v", err)
	}
	if _, ok := h.object(href); ok {
		t.Errorf("object %s still stored after delete", href)
	}
	if err := d.DeleteEvent(ctx, calendar.EventDeletion{CalendarID: cal, UID: "uid-1", Scope: calendar.ScopeSeries}); err == nil {
		t.Errorf("second DeleteEvent: want not-found error")
	}
}
