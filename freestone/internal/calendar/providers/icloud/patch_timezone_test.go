package icloud

import (
	"context"
	"testing"
	"time"

	"github.com/emersion/go-ical"

	"github.com/kai/freestone/internal/calendar"
)

// windowsZonedICS is an event as Outlook/Exchange writes it: a Windows zone
// name in TZID, defined by a VTIMEZONE of that same name.
const windowsZonedICS = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Microsoft Corporation//Outlook 16.0 MIMEDIR//EN
BEGIN:VTIMEZONE
TZID:W. Europe Standard Time
BEGIN:STANDARD
DTSTART:16011028T030000
RRULE:FREQ=YEARLY;BYDAY=-1SU;BYMONTH=10
TZOFFSETFROM:+0200
TZOFFSETTO:+0100
END:STANDARD
BEGIN:DAYLIGHT
DTSTART:16010325T020000
RRULE:FREQ=YEARLY;BYDAY=-1SU;BYMONTH=3
TZOFFSETFROM:+0100
TZOFFSETTO:+0200
END:DAYLIGHT
END:VTIMEZONE
BEGIN:VEVENT
UID:win-1
DTSTAMP:20261101T080000Z
DTSTART;TZID=W. Europe Standard Time:20270112T090000
DTEND;TZID=W. Europe Standard Time:20270112T100000
SUMMARY:Outlook meeting
END:VEVENT
END:VCALENDAR
`

// updateTimes moves uid to [start, end) through the driver.
func updateTimes(t *testing.T, d *Driver, calendarID, uid string, start, end time.Time) *calendar.Event {
	t.Helper()
	updated, err := d.UpdateEvent(context.Background(), calendar.EventPatch{
		CalendarID: calendarID, UID: uid, Start: &start, End: &end,
	})
	if err != nil {
		t.Fatalf("UpdateEvent: %v", err)
	}
	return updated
}

// storedMaster returns the first VEVENT stored at href.
func storedMaster(t *testing.T, h *calDAVHarness, href string) *ical.Component {
	t.Helper()
	stored, ok := h.object(href)
	if !ok {
		t.Fatalf("object %s gone after update; stored: %v", href, h.hrefs())
	}
	master := harnessComponent(t, stored.Data.Component, ical.CompEvent, 0)
	if master == nil {
		t.FailNow()
	}
	return master
}

// wantDateTime checks a stored DATE-TIME property's value and TZID.
func wantDateTime(t *testing.T, vevent *ical.Component, name, value, tzid string) {
	t.Helper()
	p := vevent.Props.Get(name)
	if p == nil {
		t.Errorf("%s missing", name)
		return
	}
	if p.Value != value || p.Params.Get(ical.ParamTimezoneID) != tzid {
		t.Errorf("%s = %q TZID %q, want %q TZID %q", name, p.Value, p.Params.Get(ical.ParamTimezoneID), value, tzid)
	}
}

// An event with a Windows TZID lists, so it must also update. The new
// times keep the exact TZID string the producer wrote, which is the one
// the object's VTIMEZONE defines, and land at the instant the caller gave.
func TestUpdateEvent_WindowsTZIDKeepsTZIDString(t *testing.T) {
	h := newCalDAVHarness(t)
	cal := h.addCalendar("personal", "Personal")
	href := cal + "win-1.ics"
	h.seedObject(href, windowsZonedICS)
	d := h.driver()

	start := harnessTime("2027-01-12T13:30:00Z") // 14:30 in Berlin, CET
	end := start.Add(30 * time.Minute)
	updated := updateTimes(t, d, cal, "win-1", start, end)

	master := storedMaster(t, h, href)
	wantDateTime(t, master, ical.PropDateTimeStart, "20270112T143000", "W. Europe Standard Time")
	wantDateTime(t, master, ical.PropDateTimeEnd, "20270112T150000", "W. Europe Standard Time")

	stored, _ := h.object(href)
	if tz := harnessComponent(t, stored.Data.Component, ical.CompTimezone, 0); tz != nil {
		if got := propValue(t, tz, ical.PropTimezoneID); got != "W. Europe Standard Time" {
			t.Errorf("VTIMEZONE TZID = %q, want it kept", got)
		}
	}
	if !updated.StartUTC.Equal(start) || !updated.EndUTC.Equal(end) {
		t.Errorf("returned %v..%v, want %v..%v", updated.StartUTC, updated.EndUTC, start, end)
	}
	if updated.TZID != "W. Europe Standard Time" {
		t.Errorf("returned TZID = %q, want W. Europe Standard Time", updated.TZID)
	}
}

// A floating event stays floating: the new times are the caller's instants
// on the driver's floating wall clock, written without TZID and without Z.
func TestUpdateEvent_FloatingStaysFloating(t *testing.T) {
	h := newCalDAVHarness(t)
	cal := h.addCalendar("personal", "Personal")
	href := cal + "float-1.ics"
	d5Seed(h, href, `UID:float-1
SUMMARY:Floating
DTSTART:20270112T090000
DTEND:20270112T100000`)
	d := tzDriver(t, h, tzBerlin(t))

	start := harnessTime("2027-01-12T13:30:00Z") // 14:30 in Berlin, CET
	end := start.Add(30 * time.Minute)
	updated := updateTimes(t, d, cal, "float-1", start, end)

	master := storedMaster(t, h, href)
	wantDateTime(t, master, ical.PropDateTimeStart, "20270112T143000", "")
	wantDateTime(t, master, ical.PropDateTimeEnd, "20270112T150000", "")
	if !updated.StartUTC.Equal(start) || !updated.EndUTC.Equal(end) {
		t.Errorf("returned %v..%v, want %v..%v", updated.StartUTC, updated.EndUTC, start, end)
	}
}

// A UTC event stays UTC.
func TestUpdateEvent_UTCStaysUTC(t *testing.T) {
	h := newCalDAVHarness(t)
	cal := h.addCalendar("personal", "Personal")
	href := cal + "utc-1.ics"
	d5Seed(h, href, `UID:utc-1
SUMMARY:UTC
DTSTART:20270112T090000Z
DTEND:20270112T100000Z`)
	d := tzDriver(t, h, tzBerlin(t))

	start := harnessTime("2027-01-12T13:30:00Z")
	updateTimes(t, d, cal, "utc-1", start, start.Add(30*time.Minute))

	master := storedMaster(t, h, href)
	wantDateTime(t, master, ical.PropDateTimeStart, "20270112T133000Z", "")
	wantDateTime(t, master, ical.PropDateTimeEnd, "20270112T140000Z", "")
}
