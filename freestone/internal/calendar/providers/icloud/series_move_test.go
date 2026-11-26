package icloud

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"

	"github.com/kai/freestone/internal/calendar"
)

// berlinSeriesICS is a one-hour Europe/Berlin series uid-1 starting
// Thursday 2026-11-26 09:00, with the given recurrence lines in its master
// and the given VEVENTs (overrides) after it.
func berlinSeriesICS(recurrence, overrides string) string {
	return `BEGIN:VCALENDAR
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
SUMMARY:Series
` + recurrence + `END:VEVENT
` + overrides + `END:VCALENDAR
`
}

// plainSeriesICS is a series uid-1 whose master has the given DTSTART and
// DTEND lines, used for the UTC, floating and all-day forms.
func plainSeriesICS(times, recurrence, overrides string) string {
	return "BEGIN:VCALENDAR\nVERSION:2.0\nPRODID:-//harness//EN\nBEGIN:VEVENT\nUID:uid-1\nDTSTAMP:20261101T080000Z\n" +
		times + "SUMMARY:Series\n" + recurrence + "END:VEVENT\n" + overrides + "END:VCALENDAR\n"
}

// moveSeries moves uid-1 with scope "series" to start, keeping duration.
func moveSeries(d *Driver, cal string, start time.Time, duration time.Duration) error {
	end := start.Add(duration)
	_, err := d.UpdateEvent(context.Background(), calendar.EventPatch{
		CalendarID: cal, UID: "uid-1", Start: &start, End: &end, Scope: calendar.ScopeSeries,
	})
	return err
}

// listedStarts lists uid-1's occurrences in [from, to) as summary@start.
func listedStarts(t *testing.T, d *Driver, cal string, from, to time.Time) []string {
	t.Helper()
	var out []string
	for _, e := range listRange(t, d, cal, from, to) {
		out = append(out, e.Summary+"@"+e.StartUTC.Format(time.RFC3339))
	}
	return out
}

func assertListed(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("occurrences = %v, want %v", got, want)
	}
}

// storedProp returns the value and TZID of name on the VEVENT #index of
// the stored object.
func storedProp(t *testing.T, h *calDAVHarness, href string, index int, name string) (string, string) {
	t.Helper()
	stored, _ := h.object(href)
	c := harnessComponent(t, stored.Data.Component, ical.CompEvent, index)
	if c == nil {
		t.FailNow()
	}
	p := c.Props.Get(name)
	if p == nil {
		t.Fatalf("VEVENT #%d: %s missing", index, name)
	}
	return p.Value, p.Params.Get(ical.ParamTimezoneID)
}

// Issue 261126-1156, probe 1: the rule pins Thursday, so a move to Friday
// cannot be carried by DTSTART alone. Without an override or EXDATE to
// trip over, the move used to write one stray Friday followed by
// Thursdays; it is refused, naming the rule part, and nothing is written.
func TestUpdateEvent_SeriesDayMoveAgainstBYDAYIsRefused(t *testing.T) {
	h, d, cal, href, etag := seededSeries(t, berlinSeriesICS("RRULE:FREQ=WEEKLY;BYDAY=TH\n", ""))

	err := moveSeries(d, cal, time.Date(2026, 11, 27, 9, 0, 0, 0, berlin), time.Hour)
	if err == nil {
		t.Fatal("UpdateEvent moving a BYDAY=TH series to a Friday: want a refusal")
	}
	for _, want := range []string{"cannot be carried over exactly", "nothing was changed", "BYDAY=TH", "2026-12-03T09:00:00+01:00", "2026-12-04T09:00:00+01:00"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to contain %q", err, want)
		}
	}
	assertNothingWritten(t, h, href, etag)
}

// Issue 261126-1156, probe 2: UNTIL moves with the series, in its UTC
// form, so the last occurrence is kept.
func TestUpdateEvent_SeriesMoveShiftsUntil(t *testing.T) {
	h, d, cal, href, _ := seededSeries(t, berlinSeriesICS("RRULE:FREQ=WEEKLY;UNTIL=20261210T080000Z\n", ""))

	if err := moveSeries(d, cal, time.Date(2026, 11, 26, 11, 0, 0, 0, berlin), time.Hour); err != nil {
		t.Fatalf("UpdateEvent: %v", err)
	}
	assertListed(t, listedStarts(t, d, cal, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)),
		"Series@2026-11-26T10:00:00Z", "Series@2026-12-03T10:00:00Z", "Series@2026-12-10T10:00:00Z")
	if rule, _ := storedProp(t, h, href, 0, ical.PropRecurrenceRule); rule != "FREQ=WEEKLY;UNTIL=20261210T100000Z" {
		t.Errorf("RRULE = %q, want UNTIL moved to 20261210T100000Z", rule)
	}
}

// Issue 261126-1156, probe 3: an RDATE moves with the series in its own
// form, so the extra Saturday occurrence moves to 14:00 too.
func TestUpdateEvent_SeriesMoveShiftsRdate(t *testing.T) {
	h, d, cal, href, _ := seededSeries(t, berlinSeriesICS("RRULE:FREQ=WEEKLY;BYDAY=TH;COUNT=3\nRDATE;TZID=Europe/Berlin:20261205T090000\n", ""))

	if err := moveSeries(d, cal, time.Date(2026, 11, 26, 14, 0, 0, 0, berlin), time.Hour); err != nil {
		t.Fatalf("UpdateEvent: %v", err)
	}
	assertListed(t, listedStarts(t, d, cal, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)),
		"Series@2026-11-26T13:00:00Z", "Series@2026-12-03T13:00:00Z", "Series@2026-12-05T13:00:00Z", "Series@2026-12-10T13:00:00Z")
	if v, tz := storedProp(t, h, href, 0, ical.PropRecurrenceDates); v != "20261205T140000" || tz != "Europe/Berlin" {
		t.Errorf("RDATE = %q TZID %q, want 20261205T140000 in Europe/Berlin", v, tz)
	}
}

// An endless daily series moved by a day and an hour is accepted, and each
// listed occurrence is the old one a day and an hour later, written by one
// PUT. The comparison runs into the scan bound, which an endless rule may.
func TestUpdateEvent_SeriesMoveOfEndlessDailySeriesMovesEachListedDay(t *testing.T) {
	h, d, cal, href, _ := seededSeries(t, berlinSeriesICS("RRULE:FREQ=DAILY\n", ""))

	if err := moveSeries(d, cal, time.Date(2026, 11, 27, 10, 0, 0, 0, berlin), time.Hour); err != nil {
		t.Fatalf("UpdateEvent: %v", err)
	}
	assertListed(t, listedStarts(t, d, cal, time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC)),
		"Series@2026-11-27T09:00:00Z", "Series@2026-11-28T09:00:00Z", "Series@2026-11-29T09:00:00Z")
	if v, _ := storedProp(t, h, href, 0, ical.PropDateTimeStart); v != "20261127T100000" {
		t.Errorf("DTSTART = %q, want 20261127T100000", v)
	}
	if puts := h.recordedWith(http.MethodPut); len(puts) != 1 {
		t.Errorf("PUT requests = %+v, want one", puts)
	}
}

// An override whose RECURRENCE-ID names no occurrence even before the move
// cannot be carried; the refusal says it was off the series already.
func TestUpdateEvent_SeriesMoveWithOrphanedOverrideNamesIt(t *testing.T) {
	orphan := "BEGIN:VEVENT\nUID:uid-1\nRECURRENCE-ID;TZID=Europe/Berlin:20261202T090000\nDTSTAMP:20261101T080000Z\n" +
		"DTSTART;TZID=Europe/Berlin:20261202T110000\nDTEND;TZID=Europe/Berlin:20261202T120000\nSUMMARY:Orphan\nEND:VEVENT\n"
	h, d, cal, href, etag := seededSeries(t, berlinSeriesICS("RRULE:FREQ=WEEKLY;BYDAY=TH\n", orphan))

	err := moveSeries(d, cal, time.Date(2026, 11, 26, 10, 0, 0, 0, berlin), time.Hour)
	if err == nil || !strings.Contains(err.Error(), "RECURRENCE-ID at 2026-12-02T09:00:00+01:00 already names no occurrence") {
		t.Fatalf("err = %v, want the orphaned RECURRENCE-ID named as already off the series", err)
	}
	assertNothingWritten(t, h, href, etag)
}

// Issue 261126-1156 (F2): a move from November (CET) to 14:30 carries an
// April (CEST) override whose RECURRENCE-ID is in UTC form, and an April
// EXDATE in the series' zone, each to 14:30 local in its own form.
func TestUpdateEvent_SeriesMoveAcrossDSTKeepsEachForm(t *testing.T) {
	override := "BEGIN:VEVENT\nUID:uid-1\nRECURRENCE-ID:20270415T070000Z\nDTSTAMP:20261101T080000Z\n" +
		"DTSTART:20270415T100000Z\nDTEND:20270415T110000Z\nSUMMARY:April moved\nEND:VEVENT\n"
	h, d, cal, href, _ := seededSeries(t, berlinSeriesICS("RRULE:FREQ=WEEKLY\nEXDATE;TZID=Europe/Berlin:20270408T090000\n", override))

	if err := moveSeries(d, cal, time.Date(2026, 11, 26, 14, 30, 0, 0, berlin), time.Hour); err != nil {
		t.Fatalf("UpdateEvent: %v", err)
	}
	if v, tz := storedProp(t, h, href, 1, ical.PropRecurrenceID); v != "20270415T123000Z" || tz != "" {
		t.Errorf("RECURRENCE-ID = %q TZID %q, want 20270415T123000Z (14:30 CEST) in UTC form", v, tz)
	}
	if v, tz := storedProp(t, h, href, 0, ical.PropExceptionDates); v != "20270408T143000" || tz != "Europe/Berlin" {
		t.Errorf("EXDATE = %q TZID %q, want 20270408T143000 in Europe/Berlin", v, tz)
	}
	assertListed(t, listedStarts(t, d, cal, time.Date(2027, 4, 1, 0, 0, 0, 0, time.UTC), time.Date(2027, 4, 23, 0, 0, 0, 0, time.UTC)),
		"Series@2027-04-01T12:30:00Z", "April moved@2027-04-15T10:00:00Z", "Series@2027-04-22T12:30:00Z")
}

// An all-day weekly series moved one day moves its DATE override and DATE
// EXDATE one day as well, keeping VALUE=DATE.
func TestUpdateEvent_SeriesMoveOfAllDaySeries(t *testing.T) {
	override := "BEGIN:VEVENT\nUID:uid-1\nRECURRENCE-ID;VALUE=DATE:20261203\nDTSTAMP:20261101T080000Z\n" +
		"DTSTART;VALUE=DATE:20261203\nDTEND;VALUE=DATE:20261204\nSUMMARY:Renamed\nEND:VEVENT\n"
	h, d, cal, href, _ := seededSeries(t, plainSeriesICS("DTSTART;VALUE=DATE:20261126\nDTEND;VALUE=DATE:20261127\n",
		"RRULE:FREQ=WEEKLY\nEXDATE;VALUE=DATE:20261210\n", override))

	start := time.Date(2026, 11, 27, 0, 0, 0, 0, time.UTC)
	if err := moveSeries(d, cal, start, 24*time.Hour); err != nil {
		t.Fatalf("UpdateEvent: %v", err)
	}
	for _, c := range []struct {
		index int
		name  string
		want  string
	}{
		{1, ical.PropRecurrenceID, "20261204"},
		{0, ical.PropExceptionDates, "20261211"},
		{0, ical.PropDateTimeStart, "20261127"},
	} {
		stored, _ := h.object(href)
		p := harnessComponent(t, stored.Data.Component, ical.CompEvent, c.index).Props.Get(c.name)
		if p == nil || p.Value != c.want || p.Params.Get(ical.ParamValue) != string(ical.ValueDate) {
			t.Errorf("%s = %+v, want %s with VALUE=DATE", c.name, p, c.want)
		}
	}
	assertListed(t, listedStarts(t, d, cal, time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC), time.Date(2026, 12, 19, 0, 0, 0, 0, time.UTC)),
		"Series@2026-11-27T00:00:00Z", "Renamed@2026-12-03T00:00:00Z", "Series@2026-12-18T00:00:00Z")
}

// A floating series stays floating: every moved value is written without
// TZID and without Z.
func TestUpdateEvent_SeriesMoveOfFloatingSeries(t *testing.T) {
	override := "BEGIN:VEVENT\nUID:uid-1\nRECURRENCE-ID:20261203T090000\nDTSTAMP:20261101T080000Z\n" +
		"DTSTART:20261203T110000\nDTEND:20261203T120000\nSUMMARY:Moved\nEND:VEVENT\n"
	h, d, cal, href, _ := seededSeries(t, plainSeriesICS("DTSTART:20261126T090000\nDTEND:20261126T100000\n",
		"RRULE:FREQ=WEEKLY;UNTIL=20261217T090000\nEXDATE:20261210T090000\n", override))

	// The harness driver reads floating times in UTC.
	if err := moveSeries(d, cal, time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC), time.Hour); err != nil {
		t.Fatalf("UpdateEvent: %v", err)
	}
	for _, c := range []struct {
		index int
		name  string
		want  string
	}{
		{1, ical.PropRecurrenceID, "20261203T100000"},
		{0, ical.PropExceptionDates, "20261210T100000"},
		{0, ical.PropDateTimeStart, "20261126T100000"},
		{0, ical.PropRecurrenceRule, "FREQ=WEEKLY;UNTIL=20261217T100000"},
	} {
		if v, tz := storedProp(t, h, href, c.index, c.name); v != c.want || tz != "" {
			t.Errorf("%s = %q TZID %q, want floating %q", c.name, v, tz, c.want)
		}
	}
	assertListed(t, listedStarts(t, d, cal, time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC), time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)),
		"Series@2026-11-26T10:00:00Z", "Moved@2026-12-03T11:00:00Z", "Series@2026-12-17T10:00:00Z")
}

// A UTC series keeps its UTC form, UNTIL included.
func TestUpdateEvent_SeriesMoveOfUTCSeries(t *testing.T) {
	h, d, cal, href, _ := seededSeries(t, plainSeriesICS("DTSTART:20261126T080000Z\nDTEND:20261126T090000Z\n",
		"RRULE:FREQ=DAILY;UNTIL=20261128T080000Z\nEXDATE:20261127T080000Z\n", ""))

	if err := moveSeries(d, cal, time.Date(2026, 11, 26, 9, 30, 0, 0, time.UTC), time.Hour); err != nil {
		t.Fatalf("UpdateEvent: %v", err)
	}
	for _, c := range []struct{ name, want string }{
		{ical.PropDateTimeStart, "20261126T093000Z"},
		{ical.PropExceptionDates, "20261127T093000Z"},
		{ical.PropRecurrenceRule, "FREQ=DAILY;UNTIL=20261128T093000Z"},
	} {
		if v, _ := storedProp(t, h, href, 0, c.name); v != c.want {
			t.Errorf("%s = %q, want %q", c.name, v, c.want)
		}
	}
	assertListed(t, listedStarts(t, d, cal, time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC), time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)),
		"Series@2026-11-26T09:30:00Z", "Series@2026-11-28T09:30:00Z")
}

// Issue 261126-1212, probe 1: a DATE UNTIL on a timed series bounds the
// occurrences by their date. Moving 09:00 to 08:00 on the same day changes
// no date, so UNTIL stays and every occurrence is kept.
func TestUpdateEvent_SeriesMoveToEarlierTimeKeepsDateUntil(t *testing.T) {
	h, d, cal, href, _ := seededSeries(t, berlinSeriesICS("RRULE:FREQ=DAILY;UNTIL=20261130\n", ""))

	if err := moveSeries(d, cal, time.Date(2026, 11, 26, 8, 0, 0, 0, berlin), time.Hour); err != nil {
		t.Fatalf("UpdateEvent: %v", err)
	}
	if rule, _ := storedProp(t, h, href, 0, ical.PropRecurrenceRule); rule != "FREQ=DAILY;UNTIL=20261130" {
		t.Errorf("RRULE = %q, want UNTIL kept at 20261130", rule)
	}
	assertListed(t, listedStarts(t, d, cal, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)),
		"Series@2026-11-26T07:00:00Z", "Series@2026-11-27T07:00:00Z", "Series@2026-11-28T07:00:00Z", "Series@2026-11-29T07:00:00Z")
}

// Issue 261126-1212, probe 2: Thursday 09:00 to Friday 08:00 moves every
// occurrence one date later, so the DATE UNTIL moves one day, not by the
// hour as well.
func TestUpdateEvent_SeriesMoveToNextDayEarlierShiftsDateUntilByDays(t *testing.T) {
	h, d, cal, href, _ := seededSeries(t, berlinSeriesICS("RRULE:FREQ=DAILY;UNTIL=20261130\n", ""))

	if err := moveSeries(d, cal, time.Date(2026, 11, 27, 8, 0, 0, 0, berlin), time.Hour); err != nil {
		t.Fatalf("UpdateEvent: %v", err)
	}
	if rule, _ := storedProp(t, h, href, 0, ical.PropRecurrenceRule); rule != "FREQ=DAILY;UNTIL=20261201" {
		t.Errorf("RRULE = %q, want UNTIL moved to 20261201", rule)
	}
	assertListed(t, listedStarts(t, d, cal, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)),
		"Series@2026-11-27T07:00:00Z", "Series@2026-11-28T07:00:00Z", "Series@2026-11-29T07:00:00Z", "Series@2026-11-30T07:00:00Z")
}

// Issue 261126-1212, probe 3: an UNTIL-bounded series with more occurrences
// than the scan reaches cannot be verified to its end, where a moved bound
// goes wrong, so the move is refused and nothing is written.
func TestUpdateEvent_SeriesMovePastScanOfUntilBoundedSeriesIsRefused(t *testing.T) {
	h, d, cal, href, etag := seededSeries(t, plainSeriesICS("DTSTART:20261126T090000Z\nDTEND:20261126T100000Z\n",
		"RRULE:FREQ=HOURLY;UNTIL=20400101\n", ""))

	err := moveSeries(d, cal, time.Date(2026, 11, 26, 8, 30, 0, 0, time.UTC), time.Hour)
	if err == nil {
		t.Fatal("UpdateEvent of a bounded series longer than the scan: want a refusal")
	}
	for _, want := range []string{"nothing was changed", "UNTIL", strconv.Itoa(maxOccurrenceScan)} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to contain %q", err, want)
		}
	}
	assertNothingWritten(t, h, href, etag)
}

// The same holds for a COUNT bound beyond the scan.
func TestUpdateEvent_SeriesMovePastScanOfCountBoundedSeriesIsRefused(t *testing.T) {
	h, d, cal, href, etag := seededSeries(t, plainSeriesICS("DTSTART:20261126T090000Z\nDTEND:20261126T091000Z\n",
		"RRULE:FREQ=MINUTELY;COUNT=150000\n", ""))

	err := moveSeries(d, cal, time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC), 10*time.Minute)
	if err == nil {
		t.Fatal("UpdateEvent of a COUNT series longer than the scan: want a refusal")
	}
	for _, want := range []string{"nothing was changed", "COUNT=150000", strconv.Itoa(maxOccurrenceScan)} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to contain %q", err, want)
		}
	}
	assertNothingWritten(t, h, href, etag)
}

// newYorkSeriesICS is a one-hour America/New_York series uid-1 starting
// Thursday 2026-11-26 19:00 EST, which is 2026-11-27T00:00Z, so a DATE
// value, read as UTC midnight, names one of its occurrences.
func newYorkSeriesICS(recurrence string) string {
	return plainSeriesICS("DTSTART;TZID=America/New_York:20261126T190000\nDTEND;TZID=America/New_York:20261126T200000\n", recurrence, "")
}

// A DATE EXDATE on a timed series moves by the days of the shift. West of
// UTC its UTC midnight falls on the previous local date; moving it on the
// local wall clock left it on its old date, where it excluded a different
// occurrence than before and the excluded one came back.
func TestUpdateEvent_SeriesMoveShiftsDateExdateByDays(t *testing.T) {
	h, d, cal, href, _ := seededSeries(t, newYorkSeriesICS("RRULE:FREQ=DAILY;COUNT=5\nEXDATE;VALUE=DATE:20261129\n"))

	if err := moveSeries(d, cal, time.Date(2026, 11, 27, 19, 0, 0, 0, newYork(t)), time.Hour); err != nil {
		t.Fatalf("UpdateEvent: %v", err)
	}
	if v, _ := storedProp(t, h, href, 0, ical.PropExceptionDates); v != "20261130" {
		t.Errorf("EXDATE = %q, want 20261130", v)
	}
	assertListed(t, listedStarts(t, d, cal, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)),
		"Series@2026-11-28T00:00:00Z", "Series@2026-11-29T00:00:00Z", "Series@2026-12-01T00:00:00Z", "Series@2026-12-02T00:00:00Z")
}

// A DATE RDATE on a timed series moves by the days of the shift, so a move
// by whole days carries its extra occurrence exactly.
func TestUpdateEvent_SeriesMoveShiftsDateRdateByDays(t *testing.T) {
	h, d, cal, href, _ := seededSeries(t, newYorkSeriesICS("RRULE:FREQ=DAILY;COUNT=3\nRDATE;VALUE=DATE:20261205\n"))

	if err := moveSeries(d, cal, time.Date(2026, 11, 27, 19, 0, 0, 0, newYork(t)), time.Hour); err != nil {
		t.Fatalf("UpdateEvent: %v", err)
	}
	if v, _ := storedProp(t, h, href, 0, ical.PropRecurrenceDates); v != "20261206" {
		t.Errorf("RDATE = %q, want 20261206", v)
	}
	assertListed(t, listedStarts(t, d, cal, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)),
		"Series@2026-11-28T00:00:00Z", "Series@2026-11-29T00:00:00Z", "Series@2026-11-30T00:00:00Z", "Series@2026-12-06T00:00:00Z")
}

// A time-of-day move cannot carry a DATE RDATE, which has no time to move,
// so the verification refuses it and nothing is written.
func TestUpdateEvent_SeriesTimeMoveWithDateRdateIsRefused(t *testing.T) {
	h, d, cal, href, etag := seededSeries(t, newYorkSeriesICS("RRULE:FREQ=DAILY;COUNT=3\nRDATE;VALUE=DATE:20261205\n"))

	err := moveSeries(d, cal, time.Date(2026, 11, 26, 20, 0, 0, 0, newYork(t)), time.Hour)
	if err == nil || !strings.Contains(err.Error(), "cannot be carried over exactly") {
		t.Fatalf("err = %v, want a refusal", err)
	}
	assertNothingWritten(t, h, href, etag)
}

func newYork(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// Issue 261126-1156 (F2), the single-occurrence delete's write path: a
// stale caller etag is a conflict found before the PUT, so nothing goes
// out and the object is unchanged.
func TestDeleteEvent_InstanceStaleETagIsConflictWithoutPut(t *testing.T) {
	h, d, cal, href, etag := seededSeries(t, richEventICS)

	err := d.DeleteEvent(context.Background(), calendar.EventDeletion{
		CalendarID: cal, UID: "uid-1", Scope: calendar.ScopeInstance, ETag: "stale",
		RecurrenceID: time.Date(2026, 12, 10, 9, 0, 0, 0, berlin),
	})
	if !errors.Is(err, calendar.ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	assertNothingWritten(t, h, href, etag)
}

// With the caller's current etag the occurrence is removed by one PUT
// conditional on that version, and no DELETE.
func TestDeleteEvent_InstanceCurrentETagWritesConditionally(t *testing.T) {
	h, d, cal, href, etag := seededSeries(t, richEventICS)

	if err := d.DeleteEvent(context.Background(), calendar.EventDeletion{
		CalendarID: cal, UID: "uid-1", Scope: calendar.ScopeInstance, ETag: etag,
		RecurrenceID: time.Date(2026, 12, 10, 9, 0, 0, 0, berlin),
	}); err != nil {
		t.Fatalf("DeleteEvent: %v", err)
	}
	puts := h.recordedWith(http.MethodPut)
	if len(puts) != 1 || puts[0].Path != href || puts[0].IfMatch != strconv.Quote(etag) {
		t.Errorf("PUT requests = %+v, want one on %s with If-Match %s", puts, href, strconv.Quote(etag))
	}
	if dels := h.recordedWith(http.MethodDelete); len(dels) != 0 {
		t.Errorf("DELETE requests = %+v, want none", dels)
	}
	if o, _ := h.object(href); o.ETag == etag {
		t.Errorf("stored ETag %q unchanged after the write", o.ETag)
	}
}

// A change stored between the driver's read and its PUT makes the PUT's
// If-Match fail with 412: ErrConflict, and the other change is kept.
func TestDeleteEvent_InstancePutPreconditionFailedIsConflict(t *testing.T) {
	h, d, cal, href, _ := seededSeries(t, richEventICS)
	var concurrent string
	h.beforePut = func() { concurrent = h.seedObject(href, richEventWithExdateICS) }

	err := d.DeleteEvent(context.Background(), calendar.EventDeletion{
		CalendarID: cal, UID: "uid-1", Scope: calendar.ScopeInstance,
		RecurrenceID: time.Date(2026, 11, 26, 9, 0, 0, 0, berlin),
	})
	if !errors.Is(err, calendar.ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if puts := h.recordedWith(http.MethodPut); len(puts) != 1 {
		t.Errorf("PUT requests = %+v, want the one refused", puts)
	}
	if o, _ := h.object(href); o.ETag != concurrent {
		t.Errorf("stored ETag = %q, want the concurrent change %q kept", o.ETag, concurrent)
	}
}
