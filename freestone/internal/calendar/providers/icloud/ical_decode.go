package icloud

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"

	"github.com/kai/freestone/internal/calendar"
)

// series is one UID's VEVENTs inside a calendar object: the master that
// carries the recurrence, and the overrides that replace single instances.
type series struct {
	uid       string
	master    *ical.Component
	overrides []*ical.Component
}

// decodeEvents converts an ical.Calendar into domain Events. VEVENTs are
// grouped by UID; a recurring master is expanded within [windowStart,
// windowEnd) and each instance a RECURRENCE-ID override replaces is emitted
// as that override. Any VEVENT that cannot be decoded or expanded fails the
// whole object: emitting the rest would show a series with instances
// silently missing or doubled. Floating date-times (no Z, no TZID) are read
// in floating.
func decodeEvents(cal *ical.Calendar, etag, calendarID string, windowStart, windowEnd time.Time, floating *time.Location) ([]calendar.Event, error) {
	groups, err := groupByUID(cal)
	if err != nil {
		return nil, err
	}

	var events []calendar.Event
	for _, s := range groups {
		decoded, err := decodeSeries(s, etag, calendarID, windowStart, windowEnd, floating)
		if err != nil {
			return nil, fmt.Errorf("uid %q: %w", s.uid, err)
		}
		events = append(events, decoded...)
	}
	return events, nil
}

// groupByUID collects the object's VEVENTs per UID, keeping the order in
// which UIDs first appear so output is deterministic.
func groupByUID(cal *ical.Calendar) ([]*series, error) {
	var order []*series
	byUID := make(map[string]*series)
	for _, child := range cal.Children {
		if child.Name != ical.CompEvent {
			continue
		}
		uid := ""
		if p := child.Props.Get(ical.PropUID); p != nil {
			uid = p.Value
		}
		s, ok := byUID[uid]
		if !ok {
			s = &series{uid: uid}
			byUID[uid] = s
			order = append(order, s)
		}
		if child.Props.Get(ical.PropRecurrenceID) != nil {
			s.overrides = append(s.overrides, child)
			continue
		}
		if s.master != nil {
			return nil, fmt.Errorf("uid %q: more than one VEVENT without RECURRENCE-ID", uid)
		}
		s.master = child
	}
	return order, nil
}

func decodeSeries(s *series, etag, calendarID string, windowStart, windowEnd time.Time, floating *time.Location) ([]calendar.Event, error) {
	overrides, err := decodeOverrides(s.overrides, etag, calendarID, floating)
	if err != nil {
		return nil, err
	}

	var events []calendar.Event
	if s.master != nil {
		base, err := decodeVEvent(s.master, etag, calendarID, floating)
		if err != nil {
			return nil, err
		}
		if isRecurring(s.master) {
			events, err = expandSeries(s.master, base, overrides, windowStart, windowEnd, floating)
			if err != nil {
				return nil, err
			}
		} else {
			events = append(events, base)
		}
	}

	// An override whose instance the master did not generate (no master in
	// this object, an instance outside the expansion window, or a moved
	// instance whose original slot lies outside it) still stands for a real
	// occurrence; the caller's window filter decides whether it is shown.
	for _, o := range overrides.unmatched() {
		events = append(events, o)
	}
	return events, nil
}

// isRecurring reports whether the VEVENT defines a recurrence set beyond
// its DTSTART.
func isRecurring(vevent *ical.Component) bool {
	return vevent.Props.Get(ical.PropRecurrenceRule) != nil || vevent.Props.Get(ical.PropRecurrenceDates) != nil
}

// overrideSet holds a series' RECURRENCE-ID overrides keyed by the UTC
// instant of the instance each replaces, and tracks which were used.
type overrideSet struct {
	order   []time.Time
	byStart map[time.Time]calendar.Event
	used    map[time.Time]bool
}

func decodeOverrides(vevents []*ical.Component, etag, calendarID string, floating *time.Location) (*overrideSet, error) {
	set := &overrideSet{byStart: make(map[time.Time]calendar.Event), used: make(map[time.Time]bool)}
	for _, v := range vevents {
		ridProp := v.Props.Get(ical.PropRecurrenceID)
		if r := ridProp.Params.Get(ical.ParamRange); r != "" {
			return nil, fmt.Errorf("RECURRENCE-ID;RANGE=%s is not supported", r)
		}
		rid, err := parseDateTimeValue(ridProp.Value, ridProp.Params, floating)
		if err != nil {
			return nil, fmt.Errorf("parse RECURRENCE-ID %q: %w", ridProp.Value, err)
		}
		e, err := decodeVEvent(v, etag, calendarID, floating)
		if err != nil {
			return nil, fmt.Errorf("override %s: %w", ridProp.Value, err)
		}
		key := rid.UTC()
		if _, dup := set.byStart[key]; dup {
			return nil, fmt.Errorf("duplicate RECURRENCE-ID %q", ridProp.Value)
		}
		e.RecurringParentUID = e.UID
		e.InstanceStartUTC = key
		e.RecurrenceID = recurrenceIDOf(rid)
		set.order = append(set.order, key)
		set.byStart[key] = e
	}
	return set, nil
}

// recurrenceIDOf is the recurrence_id a listed occurrence carries: its
// original start on the wall clock it was read in. calendar_delete_event
// takes the value back to address the occurrence.
func recurrenceIDOf(originalStart time.Time) string {
	return originalStart.Format(time.RFC3339)
}

// take returns the override for the instance at instUTC and marks it used.
func (s *overrideSet) take(instUTC time.Time) (calendar.Event, bool) {
	e, ok := s.byStart[instUTC]
	if ok {
		s.used[instUTC] = true
	}
	return e, ok
}

func (s *overrideSet) unmatched() []calendar.Event {
	var out []calendar.Event
	for _, k := range s.order {
		if !s.used[k] {
			out = append(out, s.byStart[k])
		}
	}
	return out
}

// decodeVEvent converts one VEVENT. A VEVENT without UID or DTSTART is an
// error rather than a zero-value event: it is not an event a caller can
// address or place in time, and a server that strips properties from its
// answer (iCloud does, for some partial-retrieval requests) must surface as
// a decode error, not as an empty entry.
func decodeVEvent(vevent *ical.Component, etag, calendarID string, floating *time.Location) (calendar.Event, error) {
	e := calendar.Event{
		Provider:   "icloud",
		CalendarID: calendarID,
		ETag:       etag,
	}

	uid := vevent.Props.Get(ical.PropUID)
	if uid == nil || uid.Value == "" {
		return e, fmt.Errorf("VEVENT has no UID")
	}
	e.UID = uid.Value
	if vevent.Props.Get(ical.PropDateTimeStart) == nil {
		return e, fmt.Errorf("VEVENT %q has no DTSTART", e.UID)
	}
	for _, f := range []struct {
		name string
		dst  *string
	}{
		{ical.PropSummary, &e.Summary},
		{ical.PropDescription, &e.Description},
		{ical.PropLocation, &e.Location},
		{ical.PropStatus, &e.Status},
	} {
		p := vevent.Props.Get(f.name)
		if p == nil {
			continue
		}
		text, err := textValue(p)
		if err != nil {
			return e, fmt.Errorf("parse %s: %w", f.name, err)
		}
		*f.dst = text
	}
	if p := vevent.Props.Get(ical.PropOrganizer); p != nil {
		e.Organizer = bareAddress(p.Value)
	}

	for _, p := range vevent.Props.Values(ical.PropAttendee) {
		e.Attendees = append(e.Attendees, bareAddress(p.Value))
	}

	dtstart := vevent.Props.Get(ical.PropDateTimeStart)
	start, tzid, allDay, err := parseDateTime(dtstart, floating)
	if err != nil {
		return e, fmt.Errorf("parse DTSTART %q: %w", dtstart.Value, err)
	}
	e.StartUTC = start.UTC()
	e.TZID = tzid
	e.AllDay = allDay
	if tzid != "" {
		e.StartLocal = start.Format(time.RFC3339)
	}

	span, err := decodeSpan(vevent, start, allDay, floating)
	if err != nil {
		return e, err
	}
	end := span.endOf(start)
	e.EndUTC = end.UTC()
	if e.TZID != "" {
		e.EndLocal = end.Format(time.RFC3339)
	}

	return e, nil
}

// eventSpan is how long a VEVENT lasts: nominal days, added on the wall
// clock of the start's own location, plus an exact part. DTEND gives an
// exact span; DURATION gives days and weeks as nominal days (RFC 5545
// §3.3.6), so a P1D event across a daylight-saving change ends at the same
// local time the next day. One span serves every instance of a series
// (RFC 5545 §3.8.5.3).
type eventSpan struct {
	days  int
	exact time.Duration
}

// endOf is the end of the instance that starts at start. Nominal days move
// start's wall clock, and the reading they reach is resolved as any local
// reading is (localInstant); without them start is kept as the instant it
// is, which a reading of it would not be in a repeated hour.
func (s eventSpan) endOf(start time.Time) time.Time {
	if s.days == 0 {
		return start.Add(s.exact)
	}
	return localInstant(wallClock(start).AddDate(0, 0, s.days), start.Location()).Add(s.exact)
}

// decodeSpan reads a VEVENT's span from DTEND or DURATION. RFC 5545 makes
// them mutually exclusive; when a producer writes both anyway, DTEND is
// used, because dropping an event the calendar holds is worse than reading
// past a non-conformant property. With neither, RFC 5545 §3.6.1 fixes it:
// an event whose DTSTART is a DATE lasts one day, one whose DTSTART is a
// DATE-TIME ends when it starts. start is the parsed DTSTART.
func decodeSpan(vevent *ical.Component, start time.Time, allDay bool, floating *time.Location) (eventSpan, error) {
	dtend := vevent.Props.Get(ical.PropDateTimeEnd)
	duration := vevent.Props.Get(ical.PropDuration)
	switch {
	case dtend != nil:
		end, _, _, err := parseDateTime(dtend, floating)
		if err != nil {
			return eventSpan{}, fmt.Errorf("parse DTEND %q: %w", dtend.Value, err)
		}
		return eventSpan{exact: end.Sub(start)}, nil
	case duration != nil:
		span, err := parseDuration(duration.Value)
		if err != nil {
			return eventSpan{}, fmt.Errorf("parse DURATION %q: %w", duration.Value, err)
		}
		return span, nil
	case allDay:
		return eventSpan{days: 1}, nil
	default:
		return eventSpan{}, nil
	}
}

// parseDuration parses an RFC 5545 §3.3.6 DURATION value: P followed by
// weeks (nW) alone, or by days (nD) and/or a time part (T with nH, nM, nS
// in that order). Weeks and days are nominal, the time part exact. A
// negative duration is an error: a VEVENT cannot end before it starts.
func parseDuration(value string) (eventSpan, error) {
	s := strings.TrimPrefix(strings.ToUpper(value), "+")
	if strings.HasPrefix(s, "-") {
		return eventSpan{}, fmt.Errorf("negative duration")
	}
	rest, ok := strings.CutPrefix(s, "P")
	if !ok {
		return eventSpan{}, fmt.Errorf("does not start with P")
	}
	if rest == "" {
		return eventSpan{}, fmt.Errorf("no components")
	}

	// number reads the digits in front of designator; found is false when
	// rest does not continue with digits and that designator.
	number := func(designator byte) (n int, found bool, err error) {
		i := 0
		for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
			i++
		}
		if i == 0 || i == len(rest) || rest[i] != designator {
			return 0, false, nil
		}
		n, err = strconv.Atoi(rest[:i])
		if err != nil {
			return 0, false, err
		}
		rest = rest[i+1:]
		return n, true, nil
	}

	var span eventSpan
	if w, found, err := number('W'); err != nil {
		return eventSpan{}, err
	} else if found {
		if rest != "" {
			return eventSpan{}, fmt.Errorf("weeks combined with other components")
		}
		return eventSpan{days: 7 * w}, nil
	}
	d, foundDays, err := number('D')
	if err != nil {
		return eventSpan{}, err
	}
	span.days = d
	if rest == "" {
		if !foundDays {
			return eventSpan{}, fmt.Errorf("no components")
		}
		return span, nil
	}
	var foundTime bool
	if rest, foundTime = strings.CutPrefix(rest, "T"); !foundTime || rest == "" {
		return eventSpan{}, fmt.Errorf("malformed")
	}
	for _, unit := range []struct {
		designator byte
		size       time.Duration
	}{{'H', time.Hour}, {'M', time.Minute}, {'S', time.Second}} {
		n, found, err := number(unit.designator)
		if err != nil {
			return eventSpan{}, err
		}
		if found {
			span.exact += time.Duration(n) * unit.size
		}
	}
	if rest != "" {
		return eventSpan{}, fmt.Errorf("malformed")
	}
	return span, nil
}

// textValue returns a single-valued TEXT property with its RFC 5545 escapes
// (\n, \, \; \\) resolved. Prop.Text is not used because it returns only
// the first element of a comma-separated list, which would cut a value
// written with a bare comma, as some producers do, at that comma; joining
// the list back keeps such a comma as the literal it was meant to be.
func textValue(p *ical.Prop) (string, error) {
	parts, err := p.TextList()
	if err != nil {
		return "", err
	}
	return strings.Join(parts, ","), nil
}

func parseDateTime(p *ical.Prop, floating *time.Location) (t time.Time, tzid string, allDay bool, err error) {
	t, err = parseDateTimeValue(p.Value, p.Params, floating)
	if err != nil {
		return t, "", false, err
	}
	tzid = p.Params.Get(ical.ParamTimezoneID)
	allDay = p.Params.Get(ical.ParamValue) == string(ical.ValueDate)
	return t, tzid, allDay, nil
}

// parseDateTimeValues parses a list-valued date-time property (EXDATE,
// RDATE), whose values are comma-separated and share one parameter set.
func parseDateTimeValues(p *ical.Prop, floating *time.Location) ([]time.Time, error) {
	if p.Params.Get(ical.ParamValue) == string(ical.ValuePeriod) {
		return nil, fmt.Errorf("%s;VALUE=PERIOD is not supported", p.Name)
	}
	var out []time.Time
	for _, v := range strings.Split(p.Value, ",") {
		t, err := parseDateTimeValue(v, p.Params, floating)
		if err != nil {
			return nil, fmt.Errorf("parse %s %q: %w", p.Name, v, err)
		}
		out = append(out, t)
	}
	return out, nil
}

// parseDateTimeValue parses one DATE or DATE-TIME value under its
// property's parameters (VALUE, TZID). A zoned value is read in its TZID's
// location, and a TZID that does not resolve is an error. A floating value
// (no Z, no TZID) is read in floating (RFC 5545 §3.3.5). A local reading
// that a daylight-saving change repeats or skips is resolved by
// localInstant.
func parseDateTimeValue(value string, params ical.Params, floating *time.Location) (time.Time, error) {
	wall, loc, err := parseDateTimeWall(value, params, floating)
	if err != nil {
		return time.Time{}, err
	}
	return localInstant(wall, loc), nil
}

// parseDateTimeWall parses one DATE or DATE-TIME value into the reading it
// states, carried in the fields of a UTC time, and the location that
// reading is on: UTC for a DATE and for a UTC DATE-TIME. A recurrence runs
// on this reading, which a reading in a daylight-saving gap does not keep
// once resolved to an instant.
func parseDateTimeWall(value string, params ical.Params, floating *time.Location) (time.Time, *time.Location, error) {
	// VALUE=DATE for all-day values.
	if params.Get(ical.ParamValue) == string(ical.ValueDate) {
		t, err := time.Parse("20060102", value)
		return t, time.UTC, err
	}

	loc, utc, err := dateTimeZone(value, params, floating)
	if err != nil {
		return time.Time{}, nil, err
	}
	if utc {
		t, err := time.Parse(dateTimeUTCLayout, value)
		return t, time.UTC, err
	}
	t, err := time.Parse(dateTimeLocalLayout, value)
	return t, loc, err
}

// The two DATE-TIME forms of RFC 5545 §3.3.5: UTC with a trailing Z, and
// local (zoned by TZID, or floating) without.
const (
	dateTimeUTCLayout   = "20060102T150405Z"
	dateTimeLocalLayout = "20060102T150405"
)

// dateTimeZone is the one place that decides which of the three DATE-TIME
// forms a value has: zoned by its TZID (resolved through resolveLocation),
// UTC (a trailing Z), or floating, read in floating. utc reports the second
// form; loc is the location the value's wall clock is read in.
func dateTimeZone(value string, params ical.Params, floating *time.Location) (loc *time.Location, utc bool, err error) {
	if tzid := params.Get(ical.ParamTimezoneID); tzid != "" {
		loc, err := resolveLocation(tzid)
		if err != nil {
			return nil, false, err
		}
		return loc, false, nil
	}
	if strings.HasSuffix(value, "Z") {
		return time.UTC, true, nil
	}
	return floating, false, nil
}

// bareAddress strips the mailto: scheme from a CAL-ADDRESS value, so
// organizer and attendees read as plain addresses on both providers. The
// scheme is case-insensitive (RFC 3986 §3.1); any other URI is kept as is.
func bareAddress(v string) string {
	const scheme = "mailto:"
	if len(v) >= len(scheme) && strings.EqualFold(v[:len(scheme)], scheme) {
		return v[len(scheme):]
	}
	return v
}
