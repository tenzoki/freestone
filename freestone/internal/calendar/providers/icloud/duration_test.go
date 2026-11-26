package icloud

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kai/freestone/internal/calendar"
)

// decodeOne decodes a single-VEVENT object with no window and returns its
// events.
func decodeOne(t *testing.T, floating *time.Location, vevent string) []calendar.Event {
	t.Helper()
	events, err := tzDecode(d5Calendar(t, vevent), floating)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return events
}

// wantSpans checks each event's start and end against want, pairs of
// RFC3339 instants.
func wantSpans(t *testing.T, events []calendar.Event, want ...[2]string) {
	t.Helper()
	if len(events) != len(want) {
		t.Fatalf("events = %d, want %d: %+v", len(events), len(want), events)
	}
	for i, w := range want {
		if s := harnessTime(w[0]); !events[i].StartUTC.Equal(s) {
			t.Errorf("event %d start = %v, want %v", i, events[i].StartUTC, s)
		}
		if e := harnessTime(w[1]); !events[i].EndUTC.Equal(e) {
			t.Errorf("event %d end = %v, want %v", i, events[i].EndUTC, e)
		}
	}
}

// DURATION gives end = start + duration, on a single event and on every
// instance of a series.
func TestDecode_DurationGivesEnd(t *testing.T) {
	single := decodeOne(t, time.UTC, `UID:dur-1
DTSTART:20261126T090000Z
DURATION:PT1H30M`)
	wantSpans(t, single, [2]string{"2026-11-26T09:00:00Z", "2026-11-26T10:30:00Z"})

	series := decodeOne(t, time.UTC, `UID:dur-2
DTSTART:20261126T090000Z
DURATION:PT1H
RRULE:FREQ=DAILY;COUNT=2`)
	wantSpans(t, series,
		[2]string{"2026-11-26T09:00:00Z", "2026-11-26T10:00:00Z"},
		[2]string{"2026-11-27T09:00:00Z", "2026-11-27T10:00:00Z"})
}

// DURATION days are nominal (RFC 5545 §3.3.6): a P1D instance that spans
// the end of daylight-saving time ends at the same local time the next
// day, 25 hours later, not 24.
func TestDecode_DurationDaysAreNominal(t *testing.T) {
	events := decodeOne(t, time.UTC, `UID:dur-dst
DTSTART;TZID=Europe/Berlin:20261024T090000
DURATION:P1D
RRULE:FREQ=DAILY;COUNT=2`)
	wantSpans(t, events,
		[2]string{"2026-10-24T07:00:00Z", "2026-10-25T08:00:00Z"},
		[2]string{"2026-10-25T08:00:00Z", "2026-10-26T08:00:00Z"})
}

// Without DTEND and DURATION (RFC 5545 §3.6.1), an all-day event lasts one
// day and a timed one ends when it starts.
func TestDecode_MissingEndFollowsRFC5545(t *testing.T) {
	allDay := decodeOne(t, time.UTC, `UID:allday-1
DTSTART;VALUE=DATE:20261126`)
	wantSpans(t, allDay, [2]string{"2026-11-26T00:00:00Z", "2026-11-27T00:00:00Z"})

	timed := decodeOne(t, time.UTC, `UID:timed-1
DTSTART:20261126T090000Z`)
	wantSpans(t, timed, [2]string{"2026-11-26T09:00:00Z", "2026-11-26T09:00:00Z"})

	allDaySeries := decodeOne(t, time.UTC, `UID:allday-2
DTSTART;VALUE=DATE:20261126
RRULE:FREQ=WEEKLY;COUNT=2`)
	wantSpans(t, allDaySeries,
		[2]string{"2026-11-26T00:00:00Z", "2026-11-27T00:00:00Z"},
		[2]string{"2026-12-03T00:00:00Z", "2026-12-04T00:00:00Z"})
}

// DTEND next to DURATION is non-conformant (RFC 5545 §3.6.1), but the event
// is the user's: it decodes with its DTEND span, on a single event and on
// every instance, rather than being dropped. This is tested at the decoder
// ListEvents calls per object, not through the wire harness: go-ical's
// encoder, which the harness server answers with, refuses to write such a
// VEVENT at all.
func TestDecode_DTENDWinsOverDURATION(t *testing.T) {
	single := decodeOne(t, time.UTC, `UID:both-1
DTSTART:20261126T090000Z
DTEND:20261126T100000Z
DURATION:PT3H`)
	wantSpans(t, single, [2]string{"2026-11-26T09:00:00Z", "2026-11-26T10:00:00Z"})

	series := decodeOne(t, time.UTC, `UID:both-2
DTSTART:20261126T090000Z
DTEND:20261126T100000Z
DURATION:PT3H
RRULE:FREQ=DAILY;COUNT=2`)
	wantSpans(t, series,
		[2]string{"2026-11-26T09:00:00Z", "2026-11-26T10:00:00Z"},
		[2]string{"2026-11-27T09:00:00Z", "2026-11-27T10:00:00Z"})
}

// A malformed or negative DURATION is a decode error, not a zero end.
func TestDecode_BadDurationIsError(t *testing.T) {
	for name, tc := range map[string]struct{ vevent, want string }{
		"malformed": {`UID:bad-1
DTSTART:20261126T090000Z
DURATION:1H`, `parse DURATION "1H"`},
		"negative": {`UID:bad-2
DTSTART:20261126T090000Z
DURATION:-PT1H`, `parse DURATION "-PT1H"`},
	} {
		t.Run(name, func(t *testing.T) {
			events, err := tzDecode(d5Calendar(t, tc.vevent), time.UTC)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("decode = %+v, %v; want an error containing %q", events, err, tc.want)
			}
		})
	}
}

// Through the driver: a daily DURATION:PT1H series ends one hour after each
// start, an all-day event without DTEND ends the next day, and an object
// with a malformed DURATION is skipped and named in the warnings.
func TestListEvents_DurationAndMissingEnd(t *testing.T) {
	h := newCalDAVHarness(t)
	cal := h.addCalendar("personal", "Personal")
	d5Seed(h, cal+"series.ics", `UID:dur-series
SUMMARY:Daily
DTSTART:20261126T090000Z
DURATION:PT1H
RRULE:FREQ=DAILY;COUNT=2`)
	d5Seed(h, cal+"allday.ics", `UID:allday-1
SUMMARY:Holiday
DTSTART;VALUE=DATE:20261126`)
	d5Seed(h, cal+"bad.ics", `UID:bad-1
SUMMARY:Broken
DTSTART:20261126T090000Z
DURATION:PT1X`)

	// No time range: the harness server's own range filter is not under test.
	list, err := h.driver().ListEvents(context.Background(), calendar.EventQuery{CalendarID: cal})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	byUID := map[string][]calendar.Event{}
	for _, e := range list.Events {
		byUID[e.UID] = append(byUID[e.UID], e)
	}
	wantSpans(t, byUID["dur-series"],
		[2]string{"2026-11-26T09:00:00Z", "2026-11-26T10:00:00Z"},
		[2]string{"2026-11-27T09:00:00Z", "2026-11-27T10:00:00Z"})
	wantSpans(t, byUID["allday-1"], [2]string{"2026-11-26T00:00:00Z", "2026-11-27T00:00:00Z"})
	if _, ok := byUID["bad-1"]; ok {
		t.Errorf("bad-1 listed, want it skipped")
	}
	if len(list.Warnings) != 1 || !strings.Contains(list.Warnings[0], cal+"bad.ics") || !strings.Contains(list.Warnings[0], "DURATION") {
		t.Errorf("warnings = %q, want one naming %sbad.ics and DURATION", list.Warnings, cal)
	}
}
