package icloud

import (
	"fmt"
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
		set.order = append(set.order, key)
		set.byStart[key] = e
	}
	return set, nil
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

	// Parse DTEND
	if p := vevent.Props.Get(ical.PropDateTimeEnd); p != nil {
		end, _, _, err := parseDateTime(p, floating)
		if err != nil {
			return e, fmt.Errorf("parse DTEND %q: %w", p.Value, err)
		}
		e.EndUTC = end.UTC()
		if e.TZID != "" {
			e.EndLocal = end.Format(time.RFC3339)
		}
	}

	return e, nil
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
// (no Z, no TZID) is read in floating (RFC 5545 §3.3.5).
func parseDateTimeValue(value string, params ical.Params, floating *time.Location) (time.Time, error) {
	// VALUE=DATE for all-day values.
	if params.Get(ical.ParamValue) == string(ical.ValueDate) {
		return time.Parse("20060102", value)
	}

	if tzid := params.Get(ical.ParamTimezoneID); tzid != "" {
		loc, err := resolveLocation(tzid)
		if err != nil {
			return time.Time{}, err
		}
		return time.ParseInLocation("20060102T150405", value, loc)
	}

	// UTC timestamp (ends with Z).
	if strings.HasSuffix(value, "Z") {
		return time.Parse("20060102T150405Z", value)
	}

	return time.ParseInLocation("20060102T150405", value, floating)
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
