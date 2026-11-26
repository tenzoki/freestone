package icloud

import (
	"context"
	"testing"
	"time"

	"github.com/emersion/go-ical"

	"github.com/kai/freestone/internal/calendar"
)

// allDayEventICS is a one-day all-day event as Calendar.app writes it.
const allDayEventICS = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//harness//EN
BEGIN:VEVENT
UID:allday-1
DTSTAMP:20261101T080000Z
DTSTART;VALUE=DATE:20261126
DTEND;VALUE=DATE:20261127
SUMMARY:Holiday
END:VEVENT
END:VCALENDAR
`

// assertDateProp checks that the property is a DATE (VALUE=DATE, no TZID)
// with the given value.
func assertDateProp(t *testing.T, vevent *ical.Component, name, want string) {
	t.Helper()
	p := vevent.Props.Get(name)
	if p == nil {
		t.Errorf("%s missing", name)
		return
	}
	if got := p.Params.Get(ical.ParamValue); got != string(ical.ValueDate) {
		t.Errorf("%s VALUE = %q, want DATE (value %q)", name, got, p.Value)
	}
	if tz := p.Params.Get(ical.ParamTimezoneID); tz != "" {
		t.Errorf("%s TZID = %q, want none on a DATE", name, tz)
	}
	if p.Value != want {
		t.Errorf("%s = %q, want %q", name, p.Value, want)
	}
}

func TestCreateEvent_AllDayWritesDates(t *testing.T) {
	h := newCalDAVHarness(t)
	cal := h.addCalendar("personal", "Personal")
	d := h.driver()

	created, err := d.CreateEvent(context.Background(), cal, calendar.Event{
		Summary:  "Holiday",
		StartUTC: time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC),
		EndUTC:   time.Date(2026, 11, 27, 0, 0, 0, 0, time.UTC),
		AllDay:   true,
	})
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}

	stored, ok := h.object(eventObjectPath(cal, created.UID))
	if !ok {
		t.Fatalf("created object not stored; stored: %v", h.hrefs())
	}
	vevent := harnessComponent(t, stored.Data.Component, ical.CompEvent, 0)
	if vevent == nil {
		t.FailNow()
	}
	assertDateProp(t, vevent, ical.PropDateTimeStart, "20261126")
	assertDateProp(t, vevent, ical.PropDateTimeEnd, "20261127")
}

func TestCreateEvent_TimedStaysUTCDateTime(t *testing.T) {
	h := newCalDAVHarness(t)
	cal := h.addCalendar("personal", "Personal")
	d := h.driver()

	created, err := d.CreateEvent(context.Background(), cal, calendar.Event{
		Summary:  "Call",
		StartUTC: time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC),
		EndUTC:   time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}
	stored, _ := h.object(eventObjectPath(cal, created.UID))
	vevent := harnessComponent(t, stored.Data.Component, ical.CompEvent, 0)
	if vevent == nil {
		t.FailNow()
	}
	if got := propValue(t, vevent, ical.PropDateTimeStart); got != "20261126T090000Z" {
		t.Errorf("DTSTART = %q, want 20261126T090000Z", got)
	}
	if p := vevent.Props.Get(ical.PropDateTimeStart); p != nil && p.Params.Get(ical.ParamValue) != "" {
		t.Errorf("DTSTART VALUE = %q, want none on a timed event", p.Params.Get(ical.ParamValue))
	}
}

func TestUpdateEvent_AllDayTimesStayDates(t *testing.T) {
	h := newCalDAVHarness(t)
	cal := h.addCalendar("personal", "Personal")
	href := cal + "allday-1.ics"
	h.seedObject(href, allDayEventICS)
	d := h.driver()

	// Midnight on the caller's wall clock is still the previous day in UTC;
	// the date written must be the caller's.
	plusOne := time.FixedZone("+01:00", 3600)
	start := time.Date(2026, 11, 27, 0, 0, 0, 0, plusOne)
	end := time.Date(2026, 11, 29, 0, 0, 0, 0, plusOne)
	updated, err := d.UpdateEvent(context.Background(), calendar.EventPatch{
		CalendarID: cal, UID: "allday-1", Start: &start, End: &end,
	})
	if err != nil {
		t.Fatalf("UpdateEvent: %v", err)
	}

	stored, _ := h.object(href)
	vevent := harnessComponent(t, stored.Data.Component, ical.CompEvent, 0)
	if vevent == nil {
		t.FailNow()
	}
	assertDateProp(t, vevent, ical.PropDateTimeStart, "20261127")
	assertDateProp(t, vevent, ical.PropDateTimeEnd, "20261129")
	if !updated.AllDay {
		t.Errorf("returned event AllDay = false, want true")
	}
}

func TestUpdateEvent_AllDayEndNotAfterStartIsError(t *testing.T) {
	h := newCalDAVHarness(t)
	cal := h.addCalendar("personal", "Personal")
	href := cal + "allday-1.ics"
	h.seedObject(href, allDayEventICS)
	d := h.driver()

	// Distinct instants on the same date: as dates, end equals start.
	start := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	end := time.Date(2026, 11, 27, 17, 0, 0, 0, time.UTC)
	if _, err := d.UpdateEvent(context.Background(), calendar.EventPatch{
		CalendarID: cal, UID: "allday-1", Start: &start, End: &end,
	}); err == nil {
		t.Fatal("UpdateEvent: want an error for an all-day end on the start date")
	}
	stored, _ := h.object(href)
	vevent := harnessComponent(t, stored.Data.Component, ical.CompEvent, 0)
	if vevent != nil {
		assertDateProp(t, vevent, ical.PropDateTimeStart, "20261126")
	}
}
