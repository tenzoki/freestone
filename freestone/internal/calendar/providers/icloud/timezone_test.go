package icloud

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"

	"github.com/kai/freestone/internal/calendar"
)

// tzDecode decodes cal with no window, reading floating times in floating.
func tzDecode(cal *ical.Calendar, floating *time.Location) ([]calendar.Event, error) {
	return decodeEvents(cal, "", "/cal/", time.Time{}, time.Time{}, floating)
}

// tzDriver builds a Driver over the harness with the given floating
// location, through the production constructor.
func tzDriver(t *testing.T, h *calDAVHarness, floating *time.Location) *Driver {
	t.Helper()
	d, err := New(context.Background(), h.srv.URL, "user", "secret", floating, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return d
}

func tzBerlin(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// D16: a Windows zone name (Exchange, Outlook) resolves through the CLDR
// table; W. Europe Standard Time is Europe/Berlin, so 09:00 in January is
// 08:00Z.
func TestDecode_WindowsTZID(t *testing.T) {
	cal := d5Calendar(t, `UID:win-1
SUMMARY:Outlook meeting
DTSTART;TZID=W. Europe Standard Time:20270112T090000
DTEND;TZID=W. Europe Standard Time:20270112T100000`)

	events, err := tzDecode(cal, time.UTC)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %+v, want one", events)
	}
	e := events[0]
	if got, want := e.StartUTC, harnessTime("2027-01-12T08:00:00Z"); !got.Equal(want) {
		t.Errorf("StartUTC = %v, want %v", got, want)
	}
	if got, want := e.EndUTC, harnessTime("2027-01-12T09:00:00Z"); !got.Equal(want) {
		t.Errorf("EndUTC = %v, want %v", got, want)
	}
	if e.TZID != "W. Europe Standard Time" {
		t.Errorf("TZID = %q, want the producer's name", e.TZID)
	}
	if e.StartLocal != "2027-01-12T09:00:00+01:00" {
		t.Errorf("StartLocal = %q, want 2027-01-12T09:00:00+01:00", e.StartLocal)
	}
}

// D16: a floating date-time (no Z, no TZID) is the user's wall clock, read
// in the injected location, not UTC.
func TestDecode_FloatingUsesInjectedLocation(t *testing.T) {
	cal := d5Calendar(t, `UID:float-1
SUMMARY:Floating
DTSTART:20270112T090000
DTEND:20270112T100000`)

	events, err := tzDecode(cal, tzBerlin(t))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %+v, want one", events)
	}
	if got, want := events[0].StartUTC, harnessTime("2027-01-12T08:00:00Z"); !got.Equal(want) {
		t.Errorf("StartUTC = %v, want %v", got, want)
	}
	if got, want := events[0].EndUTC, harnessTime("2027-01-12T09:00:00Z"); !got.Equal(want) {
		t.Errorf("EndUTC = %v, want %v", got, want)
	}
}

// D16: a TZID neither IANA nor the Windows table knows is a decode error
// naming it, never a silent UTC reading.
func TestDecode_UnknownTZIDIsError(t *testing.T) {
	cal := d5Calendar(t, `UID:mars-1
SUMMARY:Olympus
DTSTART;TZID=Mars/Olympus:20270112T090000
DTEND;TZID=Mars/Olympus:20270112T100000`)

	events, err := tzDecode(cal, time.UTC)
	if err == nil || !strings.Contains(err.Error(), `unknown TZID "Mars/Olympus"`) {
		t.Fatalf("decode = %+v, %v; want an unknown TZID error naming Mars/Olympus", events, err)
	}
}

// D16: an EXDATE written in a Windows zone excludes the instance at the
// same instant as the DTSTART series it belongs to.
func TestExpand_WindowsZoneEXDATEExcludesInstance(t *testing.T) {
	cal := d5Calendar(t, `UID:win-series
SUMMARY:Weekly Outlook
DTSTART;TZID=W. Europe Standard Time:20270105T090000
DTEND;TZID=W. Europe Standard Time:20270105T100000
RRULE:FREQ=WEEKLY;COUNT=3
EXDATE;TZID=W. Europe Standard Time:20270112T090000`)

	events, err := tzDecode(cal, time.UTC)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := d5Starts(events)
	want := []string{"2027-01-05T08:00Z", "2027-01-19T08:00Z"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("instances = %v, want %v", got, want)
	}
}

// D16: through the driver, the unknown-TZID object is skipped and named in
// the list warnings, a floating event is read in the driver's injected
// location, and an Outlook-zoned event is listed at its real instant.
func TestListEvents_ZoneResolution(t *testing.T) {
	h := newCalDAVHarness(t)
	cal := h.addCalendar("personal", "Personal")
	d5Seed(h, cal+"mars.ics", `UID:mars-1
SUMMARY:Olympus
DTSTART;TZID=Mars/Olympus:20270112T090000
DTEND;TZID=Mars/Olympus:20270112T100000`)
	d5Seed(h, cal+"float.ics", `UID:float-1
SUMMARY:Floating
DTSTART:20270112T090000
DTEND:20270112T100000`)
	d5Seed(h, cal+"win.ics", `UID:win-1
SUMMARY:Outlook meeting
DTSTART;TZID=W. Europe Standard Time:20270112T090000
DTEND;TZID=W. Europe Standard Time:20270112T100000`)

	// No time range: the harness server's own range filter is not under
	// test, and it cannot read the zones the driver now resolves.
	list, err := tzDriver(t, h, tzBerlin(t)).ListEvents(context.Background(), calendar.EventQuery{CalendarID: cal})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	starts := map[string]time.Time{}
	for _, e := range list.Events {
		starts[e.UID] = e.StartUTC
	}
	want := harnessTime("2027-01-12T08:00:00Z")
	for _, uid := range []string{"float-1", "win-1"} {
		if got, ok := starts[uid]; !ok || !got.Equal(want) {
			t.Errorf("%s start = %v (listed %v), want %v", uid, got, ok, want)
		}
	}
	if _, ok := starts["mars-1"]; ok {
		t.Errorf("mars-1 listed, want it skipped")
	}
	if len(list.Warnings) != 1 || !strings.Contains(list.Warnings[0], cal+"mars.ics") || !strings.Contains(list.Warnings[0], "Mars/Olympus") {
		t.Errorf("warnings = %q, want one naming %smars.ics and Mars/Olympus", list.Warnings, cal)
	}
}

func TestNew_RequiresFloatingLocation(t *testing.T) {
	h := newCalDAVHarness(t)
	if _, err := New(context.Background(), h.srv.URL, "user", "secret", nil, slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
		t.Fatal("New with nil floating location succeeded, want an error")
	}
}

// The embedded table yields territory-001 mappings that all load.
func TestWindowsZones_TableLoads(t *testing.T) {
	zones, err := windowsZones()
	if err != nil {
		t.Fatalf("windowsZones: %v", err)
	}
	if got := zones["W. Europe Standard Time"]; got != "Europe/Berlin" {
		t.Errorf("W. Europe Standard Time = %q, want Europe/Berlin", got)
	}
	for win, iana := range zones {
		if _, err := time.LoadLocation(iana); err != nil {
			t.Errorf("%s -> %s: %v", win, iana, err)
		}
	}
}
