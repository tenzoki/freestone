package icloud

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"

	"github.com/kai/freestone/internal/calendar"
)

func TestDecodeVEvent_Simple(t *testing.T) {
	vevent := ical.NewComponent(ical.CompEvent)
	vevent.Props.SetText(ical.PropUID, "test-uid-1")
	vevent.Props.SetText(ical.PropSummary, "Team Meeting")
	vevent.Props.SetText(ical.PropDescription, "Weekly standup")
	vevent.Props.SetText(ical.PropLocation, "Room 42")
	vevent.Props.SetText(ical.PropStatus, "CONFIRMED")

	startProp := ical.NewProp(ical.PropDateTimeStart)
	startProp.Value = "20260408T100000Z"
	vevent.Props.Set(startProp)

	endProp := ical.NewProp(ical.PropDateTimeEnd)
	endProp.Value = "20260408T110000Z"
	vevent.Props.Set(endProp)

	e, err := decodeVEvent(vevent, "etag-123", "/cal/personal/")
	if err != nil {
		t.Fatal(err)
	}
	if e.UID != "test-uid-1" {
		t.Errorf("got UID %q", e.UID)
	}
	if e.Summary != "Team Meeting" {
		t.Errorf("got Summary %q", e.Summary)
	}
	if e.Location != "Room 42" {
		t.Errorf("got Location %q", e.Location)
	}
	if e.Provider != "icloud" {
		t.Errorf("got Provider %q", e.Provider)
	}
	if e.ETag != "etag-123" {
		t.Errorf("got ETag %q", e.ETag)
	}
	expected := time.Date(2026, 4, 8, 10, 0, 0, 0, time.UTC)
	if !e.StartUTC.Equal(expected) {
		t.Errorf("StartUTC = %v, want %v", e.StartUTC, expected)
	}
}

func TestDecodeVEvent_AllDay(t *testing.T) {
	vevent := ical.NewComponent(ical.CompEvent)
	vevent.Props.SetText(ical.PropUID, "allday-1")
	vevent.Props.SetText(ical.PropSummary, "Holiday")

	startProp := ical.NewProp(ical.PropDateTimeStart)
	startProp.Value = "20260408"
	startProp.Params.Set("VALUE", "DATE")
	vevent.Props.Set(startProp)

	e, err := decodeVEvent(vevent, "", "/cal/")
	if err != nil {
		t.Fatal(err)
	}
	if !e.AllDay {
		t.Error("expected AllDay=true")
	}
}

func TestDecodeVEvent_WithTZID(t *testing.T) {
	vevent := ical.NewComponent(ical.CompEvent)
	vevent.Props.SetText(ical.PropUID, "tz-1")
	vevent.Props.SetText(ical.PropSummary, "Berlin Meeting")

	startProp := ical.NewProp(ical.PropDateTimeStart)
	startProp.Value = "20260408T120000"
	startProp.Params.Set("TZID", "Europe/Berlin")
	vevent.Props.Set(startProp)

	endProp := ical.NewProp(ical.PropDateTimeEnd)
	endProp.Value = "20260408T130000"
	endProp.Params.Set("TZID", "Europe/Berlin")
	vevent.Props.Set(endProp)

	e, err := decodeVEvent(vevent, "", "/cal/")
	if err != nil {
		t.Fatal(err)
	}
	if e.TZID != "Europe/Berlin" {
		t.Errorf("got TZID %q", e.TZID)
	}
	// Berlin is UTC+2 in April
	expectedStart := time.Date(2026, 4, 8, 10, 0, 0, 0, time.UTC)
	if !e.StartUTC.Equal(expectedStart) {
		t.Errorf("StartUTC = %v, want %v", e.StartUTC, expectedStart)
	}
	if e.StartLocal == "" {
		t.Error("expected non-empty StartLocal")
	}
}

func TestExpandRRule_Weekly(t *testing.T) {
	vevent := ical.NewComponent(ical.CompEvent)
	vevent.Props.SetText(ical.PropUID, "recur-1")
	vevent.Props.SetText(ical.PropSummary, "Weekly Sync")

	startProp := ical.NewProp(ical.PropDateTimeStart)
	startProp.Value = "20260401T090000Z"
	vevent.Props.Set(startProp)

	endProp := ical.NewProp(ical.PropDateTimeEnd)
	endProp.Value = "20260401T100000Z"
	vevent.Props.Set(endProp)

	rruleProp := ical.NewProp("RRULE")
	rruleProp.Value = "FREQ=WEEKLY;COUNT=4"
	vevent.Props.Set(rruleProp)

	base := calendar.Event{
		UID:      "recur-1",
		Summary:  "Weekly Sync",
		StartUTC: time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC),
		EndUTC:   time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
	}

	events, err := expandSeries(vevent, base, noOverrides(), time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 {
		t.Fatalf("expected 4 instances, got %d", len(events))
	}

	// Check the instances are 7 days apart.
	for i := 1; i < len(events); i++ {
		diff := events[i].StartUTC.Sub(events[i-1].StartUTC)
		if diff != 7*24*time.Hour {
			t.Errorf("instance %d: gap = %v, want 7 days", i, diff)
		}
	}

	// Each instance should have RecurringParentUID set.
	for _, e := range events {
		if e.RecurringParentUID != "recur-1" {
			t.Errorf("got RecurringParentUID %q", e.RecurringParentUID)
		}
	}
}

func TestExpandRRule_WithEXDATE(t *testing.T) {
	vevent := ical.NewComponent(ical.CompEvent)
	vevent.Props.SetText(ical.PropUID, "recur-ex")
	vevent.Props.SetText(ical.PropSummary, "With Exclusion")

	startProp := ical.NewProp(ical.PropDateTimeStart)
	startProp.Value = "20260401T090000Z"
	vevent.Props.Set(startProp)

	endProp := ical.NewProp(ical.PropDateTimeEnd)
	endProp.Value = "20260401T100000Z"
	vevent.Props.Set(endProp)

	rruleProp := ical.NewProp("RRULE")
	rruleProp.Value = "FREQ=WEEKLY;COUNT=4"
	vevent.Props.Set(rruleProp)

	// Exclude the second instance (April 8).
	exProp := ical.NewProp("EXDATE")
	exProp.Value = "20260408T090000Z"
	vevent.Props.Set(exProp)

	base := calendar.Event{
		UID:      "recur-ex",
		StartUTC: time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC),
		EndUTC:   time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
	}

	events, err := expandSeries(vevent, base, noOverrides(), time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 instances (1 excluded), got %d", len(events))
	}

	// Verify the excluded date is absent.
	excluded := time.Date(2026, 4, 8, 9, 0, 0, 0, time.UTC)
	for _, e := range events {
		if e.StartUTC.Equal(excluded) {
			t.Error("excluded date should not appear")
		}
	}
}

func TestExpandRRule_WithUNTIL(t *testing.T) {
	vevent := ical.NewComponent(ical.CompEvent)
	vevent.Props.SetText(ical.PropUID, "recur-until")

	startProp := ical.NewProp(ical.PropDateTimeStart)
	startProp.Value = "20260401T090000Z"
	vevent.Props.Set(startProp)

	endProp := ical.NewProp(ical.PropDateTimeEnd)
	endProp.Value = "20260401T100000Z"
	vevent.Props.Set(endProp)

	rruleProp := ical.NewProp("RRULE")
	rruleProp.Value = "FREQ=DAILY;UNTIL=20260405T090000Z"
	vevent.Props.Set(rruleProp)

	base := calendar.Event{
		UID:      "recur-until",
		StartUTC: time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC),
		EndUTC:   time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
	}

	events, err := expandSeries(vevent, base, noOverrides(), time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 5 {
		t.Fatalf("expected 5 instances (Apr 1-5), got %d", len(events))
	}
}

// zonedWeeklyEvent builds a VEVENT whose DTSTART/DTEND carry a TZID, the
// shape iCloud writes for a recurring meeting created in a local zone.
func zonedWeeklyEvent(uid, tzid, dtstart, dtend, rrule string) *ical.Component {
	vevent := ical.NewComponent(ical.CompEvent)
	vevent.Props.SetText(ical.PropUID, uid)

	startProp := ical.NewProp(ical.PropDateTimeStart)
	startProp.Value = dtstart
	startProp.Params.Set("TZID", tzid)
	vevent.Props.Set(startProp)

	endProp := ical.NewProp(ical.PropDateTimeEnd)
	endProp.Value = dtend
	endProp.Params.Set("TZID", tzid)
	vevent.Props.Set(endProp)

	rruleProp := ical.NewProp("RRULE")
	rruleProp.Value = rrule
	vevent.Props.Set(rruleProp)
	return vevent
}

func expandDecoded(t *testing.T, vevent *ical.Component, windowStart, windowEnd time.Time) []calendar.Event {
	t.Helper()
	base, err := decodeVEvent(vevent, "", "/cal/")
	if err != nil {
		t.Fatal(err)
	}
	events, err := expandSeries(vevent, base, noOverrides(), windowStart, windowEnd)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

// D3: a weekly 09:00 Europe/Berlin series started in winter (CET, UTC+1)
// must still be at 09:00 local in summer (CEST, UTC+2), i.e. 07:00Z, not
// pinned to the winter UTC instant 08:00Z.
func TestExpandRRule_KeepsLocalWallClockAcrossDST(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	vevent := zonedWeeklyEvent("dst-1", "Europe/Berlin", "20260105T090000", "20260105T100000", "FREQ=WEEKLY")

	events := expandDecoded(t, vevent, time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	if len(events) == 0 {
		t.Fatal("expected July instances, got none")
	}

	want := time.Date(2026, 7, 6, 7, 0, 0, 0, time.UTC)
	if !events[0].StartUTC.Equal(want) {
		t.Errorf("first July instance StartUTC = %v, want %v (09:00 CEST)", events[0].StartUTC, want)
	}
	for _, e := range events {
		local := e.StartUTC.In(berlin)
		if local.Hour() != 9 || local.Minute() != 0 {
			t.Errorf("instance %v is %s local, want 09:00", e.StartUTC, local.Format("15:04"))
		}
		if e.EndUTC.Sub(e.StartUTC) != time.Hour {
			t.Errorf("instance %v duration = %v, want 1h", e.StartUTC, e.EndUTC.Sub(e.StartUTC))
		}
		if e.StartLocal != local.Format(time.RFC3339) {
			t.Errorf("StartLocal = %q, want %q", e.StartLocal, local.Format(time.RFC3339))
		}
	}
}

// D3: BYDAY is evaluated on the event's local date. A Monday 00:30 Berlin
// series is Sunday 23:30Z in winter; expanded in UTC it lands on Tuesdays.
func TestExpandRRule_BYDAYUsesLocalDateAcrossDST(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	vevent := zonedWeeklyEvent("dst-2", "Europe/Berlin", "20260302T003000", "20260302T013000", "FREQ=WEEKLY;BYDAY=MO;COUNT=6")

	events := expandDecoded(t, vevent, time.Time{}, time.Time{})
	if len(events) != 6 {
		t.Fatalf("expected 6 instances, got %d", len(events))
	}
	for _, e := range events {
		local := e.StartUTC.In(berlin)
		if local.Weekday() != time.Monday || local.Hour() != 0 || local.Minute() != 30 {
			t.Errorf("instance %v is %s local, want Monday 00:30", e.StartUTC, local.Format("Mon 15:04"))
		}
	}
	// The series crosses the 2026-03-29 switch: the last instance is in CEST.
	if _, offset := events[len(events)-1].StartUTC.In(berlin).Zone(); offset != 2*3600 {
		t.Errorf("last instance offset = %d, want CEST (+7200)", offset)
	}
}

func noOverrides() *overrideSet {
	s, err := decodeOverrides(nil, "", "")
	if err != nil {
		panic(err)
	}
	return s
}

// d5Calendar parses one VCALENDAR from VEVENT bodies written with LF line
// ends; each body is wrapped in BEGIN/END:VEVENT.
func d5Calendar(t *testing.T, vevents ...string) *ical.Calendar {
	t.Helper()
	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\nVERSION:2.0\nPRODID:-//test//EN\n")
	for _, v := range vevents {
		b.WriteString("BEGIN:VEVENT\nDTSTAMP:20261101T080000Z\n" + strings.TrimSpace(v) + "\nEND:VEVENT\n")
	}
	b.WriteString("END:VCALENDAR\n")
	cal, err := ical.NewDecoder(strings.NewReader(strings.ReplaceAll(b.String(), "\n", "\r\n"))).Decode()
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	return cal
}

func d5Starts(events []calendar.Event) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.StartUTC.Format("2006-01-02T15:04Z")
	}
	return out
}

func d5Decode(t *testing.T, cal *ical.Calendar) []calendar.Event {
	t.Helper()
	events, err := decodeEvents(cal, "etag", "/cal/", harnessTime("2026-12-01T00:00:00Z"), harnessTime("2027-01-01T00:00:00Z"))
	if err != nil {
		t.Fatalf("decodeEvents: %v", err)
	}
	return events
}

// Weekly Tuesday 09:00Z series from 2026-12-01, four instances.
const d5Master = `UID:series-1
SUMMARY:Weekly
DTSTART:20261201T090000Z
DTEND:20261201T100000Z
RRULE:FREQ=WEEKLY;COUNT=4`

// D5: an override moving the Dec 8 Tuesday to Wednesday Dec 9 replaces that
// instance; the Tuesday slot is gone and the Wednesday shows once.
func TestDecodeEvents_OverrideReplacesInstance(t *testing.T) {
	cal := d5Calendar(t, d5Master, `UID:series-1
RECURRENCE-ID:20261208T090000Z
SUMMARY:Moved
DTSTART:20261209T090000Z
DTEND:20261209T100000Z`)

	events := d5Decode(t, cal)
	got := d5Starts(events)
	want := []string{"2026-12-01T09:00Z", "2026-12-09T09:00Z", "2026-12-15T09:00Z", "2026-12-22T09:00Z"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("starts = %v, want %v", got, want)
	}
	moved := events[1]
	if moved.Summary != "Moved" || moved.RecurringParentUID != "series-1" || !moved.InstanceStartUTC.Equal(harnessTime("2026-12-08T09:00:00Z")) {
		t.Errorf("override = %+v, want summary Moved, parent series-1, instance 2026-12-08T09:00Z", moved)
	}
}

// D5: a multi-value EXDATE excludes every listed instance.
func TestDecodeEvents_MultiValueEXDATE(t *testing.T) {
	cal := d5Calendar(t, d5Master+"\nEXDATE:20261208T090000Z,20261215T090000Z")
	got := d5Starts(d5Decode(t, cal))
	want := []string{"2026-12-01T09:00Z", "2026-12-22T09:00Z"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("starts = %v, want %v", got, want)
	}
}

// D5: an RDATE adds an instance to the series.
func TestDecodeEvents_RDATEAddsInstance(t *testing.T) {
	cal := d5Calendar(t, d5Master+"\nRDATE:20261203T140000Z")
	got := d5Starts(d5Decode(t, cal))
	want := []string{"2026-12-01T09:00Z", "2026-12-03T14:00Z", "2026-12-08T09:00Z", "2026-12-15T09:00Z", "2026-12-22T09:00Z"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("starts = %v, want %v", got, want)
	}
}

// D5: EXDATE;VALUE=DATE excludes days of an all-day series, also as a list.
func TestDecodeEvents_AllDayEXDATE(t *testing.T) {
	cal := d5Calendar(t, `UID:allday-series
SUMMARY:Daily
DTSTART;VALUE=DATE:20261201
DTEND;VALUE=DATE:20261202
RRULE:FREQ=DAILY;COUNT=5
EXDATE;VALUE=DATE:20261203,20261204`)
	got := d5Starts(d5Decode(t, cal))
	want := []string{"2026-12-01T00:00Z", "2026-12-02T00:00Z", "2026-12-05T00:00Z"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("starts = %v, want %v", got, want)
	}
}

// D5: a malformed EXDATE fails the object instead of being ignored.
func TestDecodeEvents_MalformedEXDATEIsError(t *testing.T) {
	cal := d5Calendar(t, d5Master+"\nEXDATE:20261208T090000Z,not-a-date")
	if _, err := decodeEvents(cal, "", "/cal/", time.Time{}, time.Time{}); err == nil || !strings.Contains(err.Error(), "EXDATE") {
		t.Fatalf("err = %v, want an EXDATE parse error", err)
	}
}

// D5: RANGE=THISANDFUTURE is not applied, so it is a decode error naming
// the parameter rather than a series shown with the wrong instances.
func TestDecodeEvents_RangeThisAndFutureIsError(t *testing.T) {
	cal := d5Calendar(t, d5Master, `UID:series-1
RECURRENCE-ID;RANGE=THISANDFUTURE:20261208T090000Z
SUMMARY:From here on
DTSTART:20261208T110000Z
DTEND:20261208T120000Z`)
	if _, err := decodeEvents(cal, "", "/cal/", time.Time{}, time.Time{}); err == nil || !strings.Contains(err.Error(), "THISANDFUTURE") {
		t.Fatalf("err = %v, want an error naming RANGE=THISANDFUTURE", err)
	}
}

func d5Seed(h *calDAVHarness, href string, vevents ...string) {
	h.t.Helper()
	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\nVERSION:2.0\nPRODID:-//test//EN\n")
	for _, v := range vevents {
		b.WriteString("BEGIN:VEVENT\nDTSTAMP:20261101T080000Z\n" + strings.TrimSpace(v) + "\nEND:VEVENT\n")
	}
	b.WriteString("END:VCALENDAR\n")
	h.seedObject(href, b.String())
}

// D5: the window filter applies to overrides. An instance moved out of the
// window disappears from it, and neither its old nor its new slot is shown.
func TestListEvents_OverrideMovedOutOfWindowIsExcluded(t *testing.T) {
	h := newCalDAVHarness(t)
	cal := h.addCalendar("personal", "Personal")
	d5Seed(h, cal+"series.ics", d5Master, `UID:series-1
RECURRENCE-ID:20261208T090000Z
SUMMARY:Moved to January
DTSTART:20270105T090000Z
DTEND:20270105T100000Z`)

	list, err := h.driver().ListEvents(context.Background(), calendar.EventQuery{
		CalendarID: cal,
		Start:      harnessTime("2026-12-01T00:00:00Z"),
		End:        harnessTime("2026-12-31T00:00:00Z"),
	})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	got := d5Starts(list.Events)
	want := []string{"2026-12-01T09:00Z", "2026-12-15T09:00Z", "2026-12-22T09:00Z"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("starts = %v, want %v", got, want)
	}
	if len(list.Warnings) != 0 {
		t.Errorf("warnings = %v, want none", list.Warnings)
	}
}

// D5: an object that cannot be expanded is skipped and named in Warnings;
// the good object beside it is still listed.
func TestListEvents_UndecodableObjectIsWarning(t *testing.T) {
	h := newCalDAVHarness(t)
	cal := h.addCalendar("personal", "Personal")
	d5Seed(h, cal+"good.ics", `UID:good-1
SUMMARY:Good
DTSTART:20261202T090000Z
DTEND:20261202T100000Z`)
	// The server's own time-range filter cannot parse a multi-value
	// EXDATE, so the undecodable object here is a THISANDFUTURE override,
	// which the server ignores and the driver cannot apply.
	d5Seed(h, cal+"bad.ics", d5Master, `UID:series-1
RECURRENCE-ID;RANGE=THISANDFUTURE:20261208T090000Z
SUMMARY:From here on
DTSTART:20261208T110000Z
DTEND:20261208T120000Z`)

	list, err := h.driver().ListEvents(context.Background(), calendar.EventQuery{
		CalendarID: cal,
		Start:      harnessTime("2026-12-01T00:00:00Z"),
		End:        harnessTime("2026-12-31T00:00:00Z"),
	})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(list.Events) != 1 || list.Events[0].UID != "good-1" {
		t.Errorf("events = %+v, want only good-1", list.Events)
	}
	if len(list.Warnings) != 1 || !strings.Contains(list.Warnings[0], cal+"bad.ics") || !strings.Contains(list.Warnings[0], "THISANDFUTURE") {
		t.Errorf("warnings = %q, want one naming %sbad.ics and the RANGE error", list.Warnings, cal)
	}
}
