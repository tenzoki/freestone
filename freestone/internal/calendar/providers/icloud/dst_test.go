package icloud

import (
	"strings"
	"testing"
	"time"
)

// Issue 261126-1212 (ambiguous local time): RFC 5545 §3.3.5 reads a local
// time that a fall-back repeats as its first occurrence, and one that a
// spring-forward skips with the offset before the gap.
func TestLocalInstant(t *testing.T) {
	newYork := mustLoadLocation("America/New_York")
	lordHowe := mustLoadLocation("Australia/Lord_Howe") // 30-minute shift
	wall := func(y int, m time.Month, d, h, mi int) time.Time { return time.Date(y, m, d, h, mi, 0, 0, time.UTC) }
	cases := []struct {
		name string
		wall time.Time
		loc  *time.Location
		want string
	}{
		{"berlin before fall-back", wall(2027, 10, 31, 1, 59), berlin, "2027-10-30T23:59:00Z"},
		{"berlin repeated hour start", wall(2027, 10, 31, 2, 0), berlin, "2027-10-31T00:00:00Z"},
		{"berlin repeated hour", wall(2027, 10, 31, 2, 30), berlin, "2027-10-31T00:30:00Z"},
		{"berlin after fall-back", wall(2027, 10, 31, 3, 0), berlin, "2027-10-31T02:00:00Z"},
		{"berlin before gap", wall(2027, 3, 28, 1, 59), berlin, "2027-03-28T00:59:00Z"},
		{"berlin gap start", wall(2027, 3, 28, 2, 0), berlin, "2027-03-28T01:00:00Z"},
		{"berlin gap", wall(2027, 3, 28, 2, 30), berlin, "2027-03-28T01:30:00Z"},
		{"berlin after gap", wall(2027, 3, 28, 3, 30), berlin, "2027-03-28T01:30:00Z"},
		{"new york repeated hour", wall(2027, 11, 7, 1, 30), newYork, "2027-11-07T05:30:00Z"},
		{"new york gap", wall(2027, 3, 14, 2, 30), newYork, "2027-03-14T07:30:00Z"},
		{"lord howe repeated half hour", wall(2027, 4, 4, 1, 45), lordHowe, "2027-04-03T14:45:00Z"},
		{"utc", wall(2027, 10, 31, 2, 30), time.UTC, "2027-10-31T02:30:00Z"},
		{"normalised fields", time.Date(2027, 10, 30, 26, 30, 0, 0, time.UTC), berlin, "2027-10-31T00:30:00Z"},
	}
	for _, c := range cases {
		got := localInstant(c.wall, c.loc)
		if !got.Equal(harnessTime(c.want)) {
			t.Errorf("%s: localInstant(%s) = %s, want %s", c.name, c.wall.Format("2006-01-02T15:04"), got.UTC().Format(time.RFC3339), c.want)
		}
		if got.Location() != c.loc {
			t.Errorf("%s: location %v, want %v", c.name, got.Location(), c.loc)
		}
	}
}

func TestDecode_RepeatedHourIsFirstOccurrence(t *testing.T) {
	cal := d5Calendar(t, `UID:fold-1
SUMMARY:Fold
DTSTART;TZID=Europe/Berlin:20271031T023000
DTEND;TZID=Europe/Berlin:20271031T033000`)

	events, err := tzDecode(cal, time.UTC)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %+v, want one", events)
	}
	if got, want := events[0].StartUTC, harnessTime("2027-10-31T00:30:00Z"); !got.Equal(want) {
		t.Errorf("StartUTC = %v, want %v", got, want)
	}
	if got, want := events[0].EndUTC, harnessTime("2027-10-31T02:30:00Z"); !got.Equal(want) {
		t.Errorf("EndUTC = %v, want %v (two hours: the repeated hour and the next)", got, want)
	}
}

func TestDecode_GapIsReadWithOffsetBeforeIt(t *testing.T) {
	cal := d5Calendar(t, `UID:gap-1
SUMMARY:Gap
DTSTART;TZID=Europe/Berlin:20270328T023000
DTEND;TZID=Europe/Berlin:20270328T040000`)

	events, err := tzDecode(cal, time.UTC)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got, want := events[0].StartUTC, harnessTime("2027-03-28T01:30:00Z"); !got.Equal(want) {
		t.Errorf("StartUTC = %v, want %v (03:30 CEST)", got, want)
	}
}

// decodeStarts decodes the VEVENTs and returns the instance starts.
func decodeStarts(t *testing.T, vevents ...string) []string {
	t.Helper()
	events, err := decodeEvents(d5Calendar(t, vevents...), "", "/cal/", harnessTime("2027-01-01T00:00:00Z"), harnessTime("2028-01-01T00:00:00Z"), time.UTC)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return d5Starts(events)
}

func assertStarts(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("instances = %v, want %v", got, want)
	}
}

// The acceptance listing: a daily 02:30 series lists 2027-10-31 at 00:30Z.
func TestExpand_RepeatedHourIsFirstOccurrence(t *testing.T) {
	got := decodeStarts(t, `UID:fold-series
SUMMARY:Daily
DTSTART;TZID=Europe/Berlin:20271030T023000
DTEND;TZID=Europe/Berlin:20271030T030000
RRULE:FREQ=DAILY;COUNT=3`)
	assertStarts(t, got, "2027-10-30T00:30Z", "2027-10-31T00:30Z", "2027-11-01T01:30Z")
}

// A UTC UNTIL between the two occurrences of the repeated reading admits
// the first, which is the occurrence.
func TestExpand_UTCUntilInRepeatedHour(t *testing.T) {
	got := decodeStarts(t, `UID:fold-until
SUMMARY:Daily
DTSTART;TZID=Europe/Berlin:20271030T023000
DTEND;TZID=Europe/Berlin:20271030T030000
RRULE:FREQ=DAILY;UNTIL=20271031T010000Z`)
	assertStarts(t, got, "2027-10-30T00:30Z", "2027-10-31T00:30Z")
}

// EXDATE and RECURRENCE-ID in the repeated hour name the occurrence the
// expansion generates.
func TestExpand_ExdateAndOverrideInRepeatedHour(t *testing.T) {
	got := decodeStarts(t, `UID:fold-ex
SUMMARY:Daily
DTSTART;TZID=Europe/Berlin:20271030T023000
DTEND;TZID=Europe/Berlin:20271030T030000
RRULE:FREQ=DAILY;COUNT=3
EXDATE;TZID=Europe/Berlin:20271031T023000`)
	assertStarts(t, got, "2027-10-30T00:30Z", "2027-11-01T01:30Z")

	events, err := decodeEvents(d5Calendar(t, `UID:fold-ov
SUMMARY:Daily
DTSTART;TZID=Europe/Berlin:20271030T023000
DTEND;TZID=Europe/Berlin:20271030T030000
RRULE:FREQ=DAILY;COUNT=3`, `UID:fold-ov
SUMMARY:Moved
RECURRENCE-ID;TZID=Europe/Berlin:20271031T023000
DTSTART;TZID=Europe/Berlin:20271031T050000
DTEND;TZID=Europe/Berlin:20271031T053000`), "", "/cal/", harnessTime("2027-01-01T00:00:00Z"), harnessTime("2028-01-01T00:00:00Z"), time.UTC)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	var got2 []string
	for _, e := range events {
		got2 = append(got2, e.Summary+"@"+e.StartUTC.Format("2006-01-02T15:04Z"))
	}
	assertStarts(t, got2, "Daily@2027-10-30T00:30Z", "Moved@2027-10-31T04:00Z", "Daily@2027-11-01T01:30Z")
}

// A daily 02:30 series meets the spring-forward gap: the gap day's
// occurrence is read with the offset before the gap (03:30 CEST), and the
// days after keep 02:30.
func TestExpand_GapDay(t *testing.T) {
	got := decodeStarts(t, `UID:gap-series
SUMMARY:Daily
DTSTART;TZID=Europe/Berlin:20270327T023000
DTEND;TZID=Europe/Berlin:20270327T030000
RRULE:FREQ=DAILY;COUNT=3`)
	assertStarts(t, got, "2027-03-27T01:30Z", "2027-03-28T01:30Z", "2027-03-29T00:30Z")
}

// A series whose DTSTART is in the gap recurs at the clock time DTSTART
// states, 02:30, not at the 03:30 it resolves to on the gap day.
func TestExpand_DTSTARTInGapKeepsItsClockTime(t *testing.T) {
	got := decodeStarts(t, `UID:gap-start
SUMMARY:Daily
DTSTART;TZID=Europe/Berlin:20270328T023000
DTEND;TZID=Europe/Berlin:20270328T030000
RRULE:FREQ=DAILY;COUNT=3`)
	assertStarts(t, got, "2027-03-28T01:30Z", "2027-03-29T00:30Z", "2027-03-30T00:30Z")
}

// berlinPlainSeries is a series uid-1 in Europe/Berlin (no VTIMEZONE)
// starting at start, half an hour long.
func berlinPlainSeries(start, end, rule string) string {
	return plainSeriesICS("DTSTART;TZID=Europe/Berlin:"+start+"\nDTEND;TZID=Europe/Berlin:"+end+"\n", rule+"\n", "")
}

// A series move from 01:30 to 02:30 across the fall-back day is exact: the
// 31st's occurrence lands on the first 02:30, as the moved series' own
// reading does, and is listed there.
func TestUpdateEvent_SeriesMoveIntoRepeatedHour(t *testing.T) {
	_, d, cal, _, _ := seededSeries(t, berlinPlainSeries("20271029T013000", "20271029T020000", "RRULE:FREQ=DAILY;COUNT=4"))

	if err := moveSeries(d, cal, harnessTime("2027-10-29T00:30:00Z"), 30*time.Minute); err != nil {
		t.Fatalf("UpdateEvent: %v", err)
	}
	assertListed(t, listedStarts(t, d, cal, harnessTime("2027-10-28T00:00:00Z"), harnessTime("2027-11-03T00:00:00Z")),
		"Series@2027-10-29T00:30:00Z", "Series@2027-10-30T00:30:00Z", "Series@2027-10-31T00:30:00Z", "Series@2027-11-01T01:30:00Z")
}

// A series move from 01:30 to 02:30 across the spring-forward day is
// exact: the gap day's moved occurrence and the moved series' own reading
// both resolve with the offset before the gap.
func TestUpdateEvent_SeriesMoveIntoGap(t *testing.T) {
	_, d, cal, _, _ := seededSeries(t, berlinPlainSeries("20270327T013000", "20270327T020000", "RRULE:FREQ=DAILY;COUNT=3"))

	if err := moveSeries(d, cal, harnessTime("2027-03-27T01:30:00Z"), 30*time.Minute); err != nil {
		t.Fatalf("UpdateEvent: %v", err)
	}
	assertListed(t, listedStarts(t, d, cal, harnessTime("2027-03-26T00:00:00Z"), harnessTime("2027-03-31T00:00:00Z")),
		"Series@2027-03-27T01:30:00Z", "Series@2027-03-28T01:30:00Z", "Series@2027-03-29T00:30:00Z")
}

// The reverse move, 02:30 to 01:30 across the spring-forward day, cannot be
// verified occurrence by occurrence: the gap day's 02:30 is read as 03:30,
// and an hour earlier is 02:30 again, not the 01:30 the moved series
// generates. It is refused and nothing is written.
func TestUpdateEvent_SeriesMoveOutOfGapIsRefused(t *testing.T) {
	h, d, cal, href, etag := seededSeries(t, berlinPlainSeries("20270327T023000", "20270327T030000", "RRULE:FREQ=DAILY;COUNT=3"))

	err := moveSeries(d, cal, harnessTime("2027-03-27T00:30:00Z"), 30*time.Minute)
	if err == nil || !strings.Contains(err.Error(), "cannot be carried over exactly") {
		t.Fatalf("UpdateEvent = %v, want a refusal", err)
	}
	assertNothingWritten(t, h, href, etag)
}
