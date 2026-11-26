package icloud

import (
	"bytes"
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"

	"github.com/kai/freestone/internal/calendar"
)

// richEventWithExdateICS is richEventICS with the 2026-12-10 occurrence
// cancelled, so a series move must carry an EXDATE as well as an override.
var richEventWithExdateICS = strings.Replace(richEventICS,
	"RRULE:FREQ=WEEKLY;BYDAY=TH\n",
	"RRULE:FREQ=WEEKLY;BYDAY=TH\nEXDATE;TZID=Europe/Berlin:20261210T090000\n", 1)

var berlin = mustLoadLocation("Europe/Berlin")

func mustLoadLocation(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

// seededSeries is a harness holding one object, the driver, the object's
// href and its etag as seeded.
func seededSeries(t *testing.T, ics string) (*calDAVHarness, *Driver, string, string, string) {
	t.Helper()
	h := newCalDAVHarness(t)
	cal := h.addCalendar("personal", "Personal")
	href := cal + "uid-1.ics"
	etag := h.seedObject(href, ics)
	return h, h.driver(), cal, href, etag
}

// listDay lists the events whose start falls on the given Berlin date.
func listDay(t *testing.T, d *Driver, cal string, y int, m time.Month, day int) []calendar.Event {
	t.Helper()
	start := time.Date(y, m, day, 0, 0, 0, 0, berlin)
	return listRange(t, d, cal, start, start.AddDate(0, 0, 1))
}

func listRange(t *testing.T, d *Driver, cal string, start, end time.Time) []calendar.Event {
	t.Helper()
	list, err := d.ListEvents(context.Background(), calendar.EventQuery{CalendarID: cal, Start: start, End: end})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(list.Warnings) != 0 {
		t.Fatalf("ListEvents warnings: %v", list.Warnings)
	}
	return list.Events
}

// assertNothingWritten fails if any PUT or DELETE reached the server or the
// stored object changed.
func assertNothingWritten(t *testing.T, h *calDAVHarness, href, etag string) {
	t.Helper()
	if w := append(h.recordedWith(http.MethodPut), h.recordedWith(http.MethodDelete)...); len(w) != 0 {
		t.Errorf("writes = %+v, want none", w)
	}
	if o, ok := h.object(href); !ok || o.ETag != etag {
		t.Errorf("stored object present %v etag %q, want it unchanged at %q", ok, o.ETag, etag)
	}
}

func TestUpdateEvent_RecurringWithoutScopeIsRefused(t *testing.T) {
	h, d, cal, href, etag := seededSeries(t, richEventICS)

	summary := "Standup v2"
	_, err := d.UpdateEvent(context.Background(), calendar.EventPatch{CalendarID: cal, UID: "uid-1", Summary: &summary})
	if err == nil {
		t.Fatal("UpdateEvent of a series without scope: want an error")
	}
	for _, want := range []string{"uid-1", "Standup", "recurring series", `scope "series"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to contain %q", err, want)
		}
	}
	assertNothingWritten(t, h, href, etag)
}

func TestDeleteEvent_RecurringWithoutScopeIsRefused(t *testing.T) {
	h, d, cal, href, etag := seededSeries(t, richEventICS)

	err := d.DeleteEvent(context.Background(), calendar.EventDeletion{CalendarID: cal, UID: "uid-1"})
	if err == nil {
		t.Fatal("DeleteEvent of a series without scope: want an error")
	}
	for _, want := range []string{"uid-1", "Standup", "recurring series", `scope "series"`, `scope "instance"`, "recurrence_id"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to contain %q", err, want)
		}
	}
	assertNothingWritten(t, h, href, etag)
}

func TestUpdateEvent_InstanceScopeIsRefused(t *testing.T) {
	h, d, cal, href, etag := seededSeries(t, richEventICS)

	summary := "Standup v2"
	_, err := d.UpdateEvent(context.Background(), calendar.EventPatch{CalendarID: cal, UID: "uid-1", Summary: &summary, Scope: calendar.ScopeInstance})
	if err == nil || !strings.Contains(err.Error(), "not supported on iCloud") {
		t.Fatalf("err = %v, want single-occurrence edits refused as not supported on iCloud", err)
	}
	assertNothingWritten(t, h, href, etag)
}

// Moving the series from Thursday 09:00 to Thursday 14:30 Berlin moves the
// override's RECURRENCE-ID and the EXDATE with it: 2026-12-03 still holds
// only the moved occurrence, and 2026-12-10 stays cancelled.
func TestUpdateEvent_SeriesTimeMoveCarriesOverridesAndExdates(t *testing.T) {
	h, d, cal, href, _ := seededSeries(t, richEventWithExdateICS)

	start := time.Date(2026, 11, 26, 14, 30, 0, 0, berlin)
	end := start.Add(30 * time.Minute)
	if _, err := d.UpdateEvent(context.Background(), calendar.EventPatch{
		CalendarID: cal, UID: "uid-1", Start: &start, End: &end, Scope: calendar.ScopeSeries,
	}); err != nil {
		t.Fatalf("UpdateEvent: %v", err)
	}

	dec3 := listDay(t, d, cal, 2026, 12, 3)
	if len(dec3) != 1 {
		t.Fatalf("events on 2026-12-03 = %+v, want exactly one", dec3)
	}
	if dec3[0].Summary != "Standup (moved)" || !dec3[0].StartUTC.Equal(time.Date(2026, 12, 3, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("2026-12-03 event = %q at %v, want the override at 10:00Z", dec3[0].Summary, dec3[0].StartUTC)
	}
	if got := listDay(t, d, cal, 2026, 12, 10); len(got) != 0 {
		t.Errorf("events on 2026-12-10 = %+v, want none: the EXDATE must stay effective", got)
	}
	dec17 := listDay(t, d, cal, 2026, 12, 17)
	if len(dec17) != 1 || !dec17[0].StartUTC.Equal(time.Date(2026, 12, 17, 13, 30, 0, 0, time.UTC)) {
		t.Errorf("events on 2026-12-17 = %+v, want one at 13:30Z", dec17)
	}

	stored, _ := h.object(href)
	override := harnessComponent(t, stored.Data.Component, ical.CompEvent, 1)
	master := harnessComponent(t, stored.Data.Component, ical.CompEvent, 0)
	if override == nil || master == nil {
		t.FailNow()
	}
	for _, c := range []struct {
		comp *ical.Component
		name string
		want string
	}{
		{override, ical.PropRecurrenceID, "20261203T143000"},
		{override, ical.PropDateTimeStart, "20261203T110000"},
		{master, ical.PropExceptionDates, "20261210T143000"},
	} {
		p := c.comp.Props.Get(c.name)
		if p == nil {
			t.Errorf("%s missing", c.name)
			continue
		}
		if p.Value != c.want || p.Params.Get(ical.ParamTimezoneID) != "Europe/Berlin" {
			t.Errorf("%s = %q TZID %q, want %q in Europe/Berlin", c.name, p.Value, p.Params.Get(ical.ParamTimezoneID), c.want)
		}
	}
}

// Moving the series to a Friday leaves the rule (BYDAY=TH) in place, so the
// override's moved RECURRENCE-ID would name an instant the series no
// longer generates: the move is refused and nothing is written.
func TestUpdateEvent_SeriesMoveOffTheRuleIsRefused(t *testing.T) {
	h, d, cal, href, etag := seededSeries(t, richEventICS)

	start := time.Date(2026, 11, 27, 9, 0, 0, 0, berlin)
	end := start.Add(time.Hour)
	_, err := d.UpdateEvent(context.Background(), calendar.EventPatch{
		CalendarID: cal, UID: "uid-1", Start: &start, End: &end, Scope: calendar.ScopeSeries,
	})
	if err == nil || !strings.Contains(err.Error(), "cannot be carried over exactly") {
		t.Fatalf("err = %v, want the move refused as not exact", err)
	}
	assertNothingWritten(t, h, href, etag)
}

func TestUpdateEvent_SeriesTitleOnlyNeedsNoShift(t *testing.T) {
	h, d, cal, href, _ := seededSeries(t, richEventICS)

	summary := "Standup v2"
	if _, err := d.UpdateEvent(context.Background(), calendar.EventPatch{CalendarID: cal, UID: "uid-1", Summary: &summary, Scope: calendar.ScopeSeries}); err != nil {
		t.Fatalf("UpdateEvent: %v", err)
	}
	stored, _ := h.object(href)
	if o := harnessComponent(t, stored.Data.Component, ical.CompEvent, 1); o != nil {
		if got := propValue(t, o, ical.PropRecurrenceID); got != "20261203T090000" {
			t.Errorf("RECURRENCE-ID = %q, want it untouched", got)
		}
	}
}

// Listed occurrences carry recurrence_id, the original start; for the
// override that is its RECURRENCE-ID, not its moved start. A non-recurring
// event carries none.
func TestListEvents_OccurrencesCarryRecurrenceID(t *testing.T) {
	h, d, cal, _, _ := seededSeries(t, richEventICS)
	h.seedObject(cal+"uid-10.ics", uid10ICS)

	events := listRange(t, d, cal, time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC), time.Date(2026, 12, 4, 0, 0, 0, 0, time.UTC))
	got := map[string]string{}
	for _, e := range events {
		got[e.Summary+"@"+e.StartUTC.Format(time.RFC3339)] = e.RecurrenceID
	}
	want := map[string]string{
		"Standup@2026-11-26T08:00:00Z":         "2026-11-26T09:00:00+01:00",
		"Standup (moved)@2026-12-03T10:00:00Z": "2026-12-03T09:00:00+01:00",
		"Other@2026-11-27T09:00:00Z":           "",
	}
	if len(got) != len(want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for k, v := range want {
		if g, ok := got[k]; !ok || g != v {
			t.Errorf("%s: recurrence_id = %q (listed %v), want %q", k, g, ok, v)
		}
	}
}

// recurrenceIDListed returns the recurrence_id calendar_list_events gives
// the occurrence of uid-1 that starts at startUTC.
func recurrenceIDListed(t *testing.T, d *Driver, cal string, startUTC time.Time) time.Time {
	t.Helper()
	for _, e := range listRange(t, d, cal, startUTC.Add(-12*time.Hour), startUTC.Add(12*time.Hour)) {
		if e.UID == "uid-1" && e.StartUTC.Equal(startUTC) {
			rid, err := time.Parse(time.RFC3339, e.RecurrenceID)
			if err != nil {
				t.Fatalf("recurrence_id %q: %v", e.RecurrenceID, err)
			}
			return rid
		}
	}
	t.Fatalf("no uid-1 occurrence listed at %v", startUTC)
	return time.Time{}
}

// occurrenceStarts lists uid-1's occurrences from 2026-11-26 to 2027-01-01.
func occurrenceStarts(t *testing.T, d *Driver, cal string) []string {
	t.Helper()
	var out []string
	for _, e := range listRange(t, d, cal, time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC), time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)) {
		out = append(out, e.Summary+"@"+e.StartUTC.Format(time.RFC3339))
	}
	return out
}

func TestDeleteEvent_InstanceRemovesExactlyOneOccurrence(t *testing.T) {
	for _, tc := range []struct {
		name      string
		startUTC  time.Time
		gone      string
		exdate    string
		overrides int
	}{
		{"plain", time.Date(2026, 12, 10, 8, 0, 0, 0, time.UTC), "Standup@2026-12-10T08:00:00Z", "20261210T090000", 1},
		{"overridden", time.Date(2026, 12, 3, 10, 0, 0, 0, time.UTC), "Standup (moved)@2026-12-03T10:00:00Z", "20261203T090000", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, d, cal, href, etag := seededSeries(t, richEventICS)
			before := occurrenceStarts(t, d, cal)
			rid := recurrenceIDListed(t, d, cal, tc.startUTC)

			if err := d.DeleteEvent(context.Background(), calendar.EventDeletion{
				CalendarID: cal, UID: "uid-1", Scope: calendar.ScopeInstance, RecurrenceID: rid,
			}); err != nil {
				t.Fatalf("DeleteEvent: %v", err)
			}

			var want []string
			for _, s := range before {
				if s != tc.gone {
					want = append(want, s)
				}
			}
			if len(want) != len(before)-1 {
				t.Fatalf("occurrences before = %v, want %s among them", before, tc.gone)
			}
			if got := occurrenceStarts(t, d, cal); strings.Join(got, " ") != strings.Join(want, " ") {
				t.Errorf("occurrences after = %v, want %v", got, want)
			}
			if tc.name == "overridden" {
				if got := listDay(t, d, cal, 2026, 12, 3); len(got) != 0 {
					t.Errorf("events on 2026-12-03 = %+v, want none", got)
				}
			}

			if dels := h.recordedWith(http.MethodDelete); len(dels) != 0 {
				t.Errorf("DELETE requests = %+v, want none: the series stays", dels)
			}
			puts := h.recordedWith(http.MethodPut)
			if len(puts) != 1 || puts[0].Path != href || puts[0].IfMatch != strconv.Quote(etag) {
				t.Errorf("PUT requests = %+v, want one on %s with If-Match %s", puts, href, strconv.Quote(etag))
			}

			stored, _ := h.object(href)
			root := stored.Data.Component
			harnessComponent(t, root, ical.CompTimezone, 0)
			master := harnessComponent(t, root, ical.CompEvent, 0)
			if master == nil {
				t.FailNow()
			}
			harnessComponent(t, master, ical.CompAlarm, 0)
			ex := master.Props.Get(ical.PropExceptionDates)
			if ex == nil || ex.Value != tc.exdate || ex.Params.Get(ical.ParamTimezoneID) != "Europe/Berlin" {
				t.Errorf("EXDATE = %+v, want %s in Europe/Berlin", ex, tc.exdate)
			}
			n := 0
			for _, c := range root.Children {
				if c.Name == ical.CompEvent && c.Props.Get(ical.PropRecurrenceID) != nil {
					n++
				}
			}
			if n != tc.overrides {
				t.Errorf("override VEVENTs = %d, want %d", n, tc.overrides)
			}
		})
	}
}

func TestDeleteEvent_InstanceErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		ics  string
		del  calendar.EventDeletion
		want string
	}{
		{"no recurrence_id", richEventICS, calendar.EventDeletion{Scope: calendar.ScopeInstance}, "needs recurrence_id"},
		{"not an occurrence", richEventICS, calendar.EventDeletion{Scope: calendar.ScopeInstance, RecurrenceID: time.Date(2026, 12, 4, 9, 0, 0, 0, berlin)}, "is not an occurrence"},
		{"already cancelled", richEventWithExdateICS, calendar.EventDeletion{Scope: calendar.ScopeInstance, RecurrenceID: time.Date(2026, 12, 10, 9, 0, 0, 0, berlin)}, "is not an occurrence"},
		{"recurrence_id without instance scope", richEventICS, calendar.EventDeletion{Scope: calendar.ScopeSeries, RecurrenceID: time.Date(2026, 12, 10, 9, 0, 0, 0, berlin)}, `only with scope "instance"`},
		{"instance of a non-recurring event", uid10ICS, calendar.EventDeletion{Scope: calendar.ScopeInstance, RecurrenceID: time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)}, "is not recurring"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, d, cal, href, etag := seededSeries(t, tc.ics)
			tc.del.CalendarID = cal
			tc.del.UID = "uid-1"
			if tc.ics == uid10ICS {
				tc.del.UID = "uid-10"
			}
			err := d.DeleteEvent(context.Background(), tc.del)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
			assertNothingWritten(t, h, href, etag)
		})
	}
}

// A non-recurring event needs no scope; "series" is accepted on it.
func TestNonRecurringEvent_AcceptsNoScopeAndSeries(t *testing.T) {
	_, d, cal, _, _ := seededSeries(t, uid10ICS)
	summary := "Other v2"
	for _, scope := range []calendar.Scope{calendar.ScopeUnspecified, calendar.ScopeSeries} {
		if _, err := d.UpdateEvent(context.Background(), calendar.EventPatch{CalendarID: cal, UID: "uid-10", Summary: &summary, Scope: scope}); err != nil {
			t.Errorf("UpdateEvent scope %q: %v", scope, err)
		}
	}
	if err := d.DeleteEvent(context.Background(), calendar.EventDeletion{CalendarID: cal, UID: "uid-10", Scope: calendar.ScopeSeries}); err != nil {
		t.Errorf("DeleteEvent scope series: %v", err)
	}
}

func TestDeleteEvent_SeriesScopeRemovesTheObject(t *testing.T) {
	h, d, cal, href, _ := seededSeries(t, richEventICS)
	if err := d.DeleteEvent(context.Background(), calendar.EventDeletion{CalendarID: cal, UID: "uid-1", Scope: calendar.ScopeSeries}); err != nil {
		t.Fatalf("DeleteEvent: %v", err)
	}
	if _, ok := h.object(href); ok {
		t.Errorf("object %s still stored", href)
	}
}

// decodeICS parses iCalendar text with LF line ends. go-ical's decoder,
// unlike its encoder, accepts a VEVENT with both DTEND and DURATION.
func decodeICS(t *testing.T, ics string) *ical.Calendar {
	t.Helper()
	text := strings.ReplaceAll(ics, "\n", "\r\n")
	cal, err := ical.NewDecoder(strings.NewReader(text)).Decode()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return cal
}

func encodeICS(cal *ical.Calendar) error {
	return ical.NewEncoder(&bytes.Buffer{}).Encode(cal)
}

// The EXDATE takes the master's DTSTART form: a DATE for an all-day series,
// a UTC value for a UTC series.
func TestApplyDeletion_ExdateInMasterForm(t *testing.T) {
	for _, tc := range []struct {
		name, dtstart, dtend string
		rid                  time.Time
		value, valueParam    string
	}{
		{"all-day", "DTSTART;VALUE=DATE:20261126", "DTEND;VALUE=DATE:20261127",
			time.Date(2026, 12, 3, 0, 0, 0, 0, time.UTC), "20261203", "DATE"},
		{"utc", "DTSTART:20261126T080000Z", "DTEND:20261126T090000Z",
			time.Date(2026, 12, 3, 9, 0, 0, 0, berlin), "20261203T080000Z", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cal := decodeICS(t, "BEGIN:VCALENDAR\nVERSION:2.0\nPRODID:-//t//EN\nBEGIN:VEVENT\nUID:s\nDTSTAMP:20261101T080000Z\n"+
				tc.dtstart+"\n"+tc.dtend+"\nRRULE:FREQ=WEEKLY\nSUMMARY:Series\nEND:VEVENT\nEND:VCALENDAR\n")
			removeObject, err := applyDeletion(cal, calendar.EventDeletion{UID: "s", Scope: calendar.ScopeInstance, RecurrenceID: tc.rid}, time.Now(), time.UTC)
			if err != nil || removeObject {
				t.Fatalf("applyDeletion = %v, %v; want the object kept", removeObject, err)
			}
			ex := cal.Children[0].Props.Get(ical.PropExceptionDates)
			if ex == nil || ex.Value != tc.value || ex.Params.Get(ical.ParamTimezoneID) != "" {
				t.Fatalf("EXDATE = %+v, want %s without TZID", ex, tc.value)
			}
			if got := ex.Params.Get(ical.ParamValue); got != tc.valueParam {
				t.Errorf("EXDATE VALUE = %q, want %q", got, tc.valueParam)
			}
			if err := encodeICS(cal); err != nil {
				t.Errorf("encode: %v", err)
			}
		})
	}
}

const dtendAndDurationICS = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//t//EN
BEGIN:VEVENT
UID:both
DTSTAMP:20261101T080000Z
DTSTART:20261126T090000Z
DTEND:20261126T100000Z
DURATION:PT3H
SUMMARY:Both
END:VEVENT
END:VCALENDAR
`

// Issue 261126-1110: a VEVENT with DTEND and DURATION reads by its DTEND,
// and a title-only update drops the DURATION, so the object encodes and
// the span every caller saw is kept.
func TestApplyPatch_TitleOnlyDropsDurationShadowedByDTEnd(t *testing.T) {
	cal := decodeICS(t, dtendAndDurationICS)
	before, err := decodeEvents(cal, "", "c", time.Time{}, time.Time{}, time.UTC)
	if err != nil {
		t.Fatal(err)
	}

	summary := "Both v2"
	if _, err := applyPatch(cal, calendar.EventPatch{UID: "both", Summary: &summary}, time.Now(), time.UTC); err != nil {
		t.Fatalf("applyPatch: %v", err)
	}
	if err := encodeICS(cal); err != nil {
		t.Fatalf("encode after a title-only update: %v", err)
	}
	vevent := cal.Children[0]
	if p := vevent.Props.Get(ical.PropDuration); p != nil {
		t.Errorf("DURATION = %q, want dropped", p.Value)
	}
	if got := propValue(t, vevent, ical.PropDateTimeEnd); got != "20261126T100000Z" {
		t.Errorf("DTEND = %q, want kept", got)
	}
	after, err := decodeEvents(cal, "", "c", time.Time{}, time.Time{}, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if !after[0].EndUTC.Equal(before[0].EndUTC) {
		t.Errorf("end after = %v, want the span read before, %v", after[0].EndUTC, before[0].EndUTC)
	}
}

// The same holds for an override when one occurrence is deleted, since the
// whole object is encoded again.
func TestApplyDeletion_DropsDurationShadowedByDTEnd(t *testing.T) {
	cal := decodeICS(t, "BEGIN:VCALENDAR\nVERSION:2.0\nPRODID:-//t//EN\n"+
		"BEGIN:VEVENT\nUID:s\nDTSTAMP:20261101T080000Z\nDTSTART:20261126T090000Z\nDTEND:20261126T100000Z\nRRULE:FREQ=DAILY;COUNT=5\nSUMMARY:S\nEND:VEVENT\n"+
		"BEGIN:VEVENT\nUID:s\nDTSTAMP:20261101T080000Z\nRECURRENCE-ID:20261128T090000Z\nDTSTART:20261128T110000Z\nDTEND:20261128T120000Z\nDURATION:PT2H\nSUMMARY:S moved\nEND:VEVENT\n"+
		"END:VCALENDAR\n")
	if _, err := applyDeletion(cal, calendar.EventDeletion{UID: "s", Scope: calendar.ScopeInstance, RecurrenceID: time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)}, time.Now(), time.UTC); err != nil {
		t.Fatalf("applyDeletion: %v", err)
	}
	if err := encodeICS(cal); err != nil {
		t.Fatalf("encode after deleting an occurrence: %v", err)
	}
}
