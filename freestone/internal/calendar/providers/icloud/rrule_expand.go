package icloud

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/teambition/rrule-go"

	"github.com/kai/freestone/internal/calendar"
)

// maxExpansions caps the number of instances to prevent unbounded RRULE explosion.
const maxExpansions = 366

// expandSeries expands a recurring master VEVENT into its instances within
// the query window [windowStart, windowEnd). If both are zero, it falls back
// to a 1-year window from the event's DTSTART to avoid unbounded expansion.
// An instance that an override replaces is emitted as that override; the
// override is marked used in overrides.
func expandSeries(master *ical.Component, base calendar.Event, overrides *overrideSet, windowStart, windowEnd time.Time, floating *time.Location) ([]calendar.Event, error) {
	set, dtstart, err := recurrenceSet(master, floating)
	if err != nil {
		return nil, err
	}
	span, err := decodeSpan(master, dtstart, isAllDay(master.Props), floating)
	if err != nil {
		return nil, err
	}

	// Bound expansion to the query window. If no window is provided, default
	// to 1 year from DTSTART to prevent unbounded memory allocation on rules
	// without COUNT or UNTIL.
	if windowStart.IsZero() {
		windowStart = dtstart
	}
	if windowEnd.IsZero() {
		windowEnd = dtstart.AddDate(1, 0, 0)
	}

	// Use an iterator instead of Between() to avoid pre-allocating a
	// potentially huge slice for rules without COUNT or UNTIL.
	next := set.iterator()
	var events []calendar.Event
	count := 0

	for {
		inst, ok := next()
		if !ok {
			break
		}
		instUTC := inst.UTC()
		// Skip instances before the query window.
		if instUTC.Before(windowStart) {
			continue
		}
		// Stop once we pass the query window.
		if !instUTC.Before(windowEnd) {
			break
		}

		count++
		if count > maxExpansions {
			break
		}

		if o, ok := overrides.take(instUTC); ok {
			events = append(events, o)
			continue
		}

		e := base
		e.StartUTC = instUTC
		// Nominal days of a DURATION fall on the series' own wall clock.
		e.EndUTC = span.endOf(inst.In(dtstart.Location())).UTC()
		e.RecurringParentUID = base.UID
		e.InstanceStartUTC = instUTC
		e.RecurrenceID = recurrenceIDOf(inst.In(dtstart.Location()))
		if base.TZID != "" {
			// Render in the series' own location; an RDATE may carry another.
			e.StartLocal = instUTC.In(dtstart.Location()).Format(time.RFC3339)
			e.EndLocal = e.EndUTC.In(dtstart.Location()).Format(time.RFC3339)
		}

		events = append(events, e)
	}

	return events, nil
}

// recurrenceSet builds the VEVENT's recurrence set (RFC 5545 §3.8.5): its
// DTSTART, the RRULE if any, every RDATE, minus every EXDATE. It is the one
// place a series' instants are defined, so overrides, exclusions and extra
// dates cannot disagree about which instants exist.
func recurrenceSet(vevent *ical.Component, floating *time.Location) (*occurrenceSet, time.Time, error) {
	set, dtstart, err := generatedSet(vevent, floating)
	if err != nil {
		return nil, time.Time{}, err
	}
	for _, p := range vevent.Props.Values(ical.PropExceptionDates) {
		dates, err := parseDateTimeValues(&p, floating)
		if err != nil {
			return nil, time.Time{}, err
		}
		set.exdates = append(set.exdates, dates...)
	}
	return set, dtstart, nil
}

// generatedSet is the recurrence set before EXDATE removes anything: the
// instants the series' DTSTART, RRULE and RDATEs generate. An EXDATE or a
// RECURRENCE-ID is meaningful only if it names one of them.
func generatedSet(vevent *ical.Component, floating *time.Location) (*occurrenceSet, time.Time, error) {
	dtstart, wall, err := recurrenceStart(vevent, floating)
	if err != nil {
		return nil, time.Time{}, err
	}

	// DTSTART is always the first instance of the set, also when only RDATEs
	// define the rest. The set iterator drops the duplicate the RRULE yields.
	set := &occurrenceSet{loc: dtstart.Location(), rdates: []time.Time{dtstart}}

	if p := vevent.Props.Get(ical.PropRecurrenceRule); p != nil {
		// The rule runs on the series' wall clock, carried in UTC fields so
		// that rrule-go meets no daylight-saving change; each reading it
		// yields is resolved to an instant by localInstant. Its UNTIL is
		// kept out of rrule-go, which would compare it with rrule-go's own
		// instants, and is applied to the resolved ones instead.
		opt, err := rrule.StrToROptionInLocation(p.Value, time.UTC)
		if err != nil {
			return nil, time.Time{}, fmt.Errorf("parse RRULE %q: %w", p.Value, err)
		}
		if !opt.Until.IsZero() {
			until, _, err := ruleUntil(p.Value, set.loc)
			if err != nil {
				return nil, time.Time{}, err
			}
			set.until = until
			opt.Until = time.Time{}
		}
		opt.Dtstart = wall
		rule, err := rrule.NewRRule(*opt)
		if err != nil {
			return nil, time.Time{}, fmt.Errorf("build RRULE %q: %w", p.Value, err)
		}
		set.rule = rule
	}

	for _, p := range vevent.Props.Values(ical.PropRecurrenceDates) {
		dates, err := parseDateTimeValues(&p, floating)
		if err != nil {
			return nil, time.Time{}, err
		}
		set.rdates = append(set.rdates, dates...)
	}
	return set, dtstart, nil
}

// ruleUntil reads the UNTIL of the RRULE value rule as RFC 5545 §3.3.10
// bounds a rule with it: a UTC DATE-TIME as that instant, a local DATE-TIME
// as its reading in loc, the series' location, and a DATE, which the RFC
// does not allow on a DATE-TIME series but producers write, as the start of
// that date in loc. layout is the form the value is written in. It reports
// a zero time if rule has no UNTIL.
func ruleUntil(rule string, loc *time.Location) (until time.Time, layout string, err error) {
	for _, part := range strings.Split(rule, ";") {
		key, v, ok := strings.Cut(part, "=")
		if !ok || !strings.EqualFold(key, "UNTIL") {
			continue
		}
		switch {
		case len(v) == len(dateLayout):
			layout = dateLayout
		case strings.HasSuffix(v, "Z"):
			layout = dateTimeUTCLayout
		default:
			layout = dateTimeLocalLayout
		}
		t, err := time.Parse(layout, v)
		if err != nil {
			return time.Time{}, "", fmt.Errorf("parse RRULE UNTIL %q: %w", v, err)
		}
		if layout == dateTimeUTCLayout {
			return t, layout, nil
		}
		return localInstant(t, loc), layout, nil
	}
	return time.Time{}, "", nil
}

// dateLayout is the DATE form of RFC 5545 §3.3.4.
const dateLayout = "20060102"

// occurrenceSet is a series' recurrence set: the instants of its RDATEs
// (DTSTART among them) and of its rule, minus its EXDATEs. It stands in
// for rrule.Set, which takes the rule's instants as rrule-go computes them;
// those pick the second occurrence of a repeated reading.
type occurrenceSet struct {
	loc     *time.Location // the series' location, which the rule's readings are on
	rule    *rrule.RRule   // nil without RRULE; runs on wall readings in UTC fields
	until   time.Time      // the rule's UNTIL as an instant; zero without one
	rdates  []time.Time
	exdates []time.Time
}

// iterator yields the set's instants in order, each once, never through
// Between(), which pre-allocates without bound.
func (s *occurrenceSet) iterator() func() (time.Time, bool) {
	rdates := sortedSeconds(s.rdates)
	exdates := sortedSeconds(s.exdates)

	var ruleNext func() (time.Time, bool)
	if s.rule != nil {
		next := s.rule.Iterator()
		done := false
		ruleNext = func() (time.Time, bool) {
			if done {
				return time.Time{}, false
			}
			wall, ok := next()
			if !ok {
				done = true
				return time.Time{}, false
			}
			// Resolution keeps the readings' order, so the first one past
			// UNTIL ends the rule.
			t := localInstant(wall, s.loc)
			if !s.until.IsZero() && t.After(s.until) {
				done = true
				return time.Time{}, false
			}
			return t, true
		}
	} else {
		ruleNext = func() (time.Time, bool) { return time.Time{}, false }
	}
	ruleHead, ruleOK := ruleNext()

	var last time.Time
	started := false
	return func() (time.Time, bool) {
		for {
			var t time.Time
			switch {
			case len(rdates) > 0 && (!ruleOK || !ruleHead.Before(rdates[0])):
				t, rdates = rdates[0], rdates[1:]
			case ruleOK:
				t = ruleHead
				ruleHead, ruleOK = ruleNext()
			default:
				return time.Time{}, false
			}
			if started && t.Equal(last) {
				continue
			}
			last, started = t, true
			for len(exdates) > 0 && exdates[0].Before(t) {
				exdates = exdates[1:]
			}
			if len(exdates) > 0 && exdates[0].Equal(t) {
				continue
			}
			return t, true
		}
	}
}

// endless reports whether the set has no last occurrence: an RRULE without
// UNTIL or COUNT. RDATEs alone are always finite.
func (s *occurrenceSet) endless() bool {
	return s.rule != nil && s.rule.OrigOptions.Count == 0 && s.until.IsZero()
}

// bound names what ends a set that is not endless: its rule's COUNT or
// UNTIL, or, without a rule, its RDATEs.
func (s *occurrenceSet) bound(show func(time.Time) string) string {
	switch {
	case s.rule == nil:
		return "its last RDATE"
	case s.rule.OrigOptions.Count > 0:
		return fmt.Sprintf("its rule's COUNT=%d", s.rule.OrigOptions.Count)
	default:
		return "its rule's UNTIL " + show(s.until)
	}
}

// sortedSeconds is a sorted copy of ts at second precision, the precision
// of iCalendar values.
func sortedSeconds(ts []time.Time) []time.Time {
	out := make([]time.Time, len(ts))
	for i, t := range ts {
		out[i] = t.Truncate(time.Second)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}

// maxOccurrenceScan bounds how many instants occursAt walks before it gives
// up, so a rule with a tiny interval cannot stall a write.
const maxOccurrenceScan = 100000

// occursAt reports whether set generates the instant t. It walks the set's
// iterator up to t, never Between(), which pre-allocates without bound.
func occursAt(set *occurrenceSet, t time.Time) (bool, error) {
	next := set.iterator()
	for i := 0; i < maxOccurrenceScan; i++ {
		inst, ok := next()
		if !ok || inst.After(t) {
			return false, nil
		}
		if inst.Equal(t) {
			return true, nil
		}
	}
	return false, fmt.Errorf("more than %d occurrences before %s; cannot tell whether it is one", maxOccurrenceScan, t.Format(time.RFC3339))
}

// recurrenceStart returns the VEVENT's DTSTART in the event's own location,
// and the reading it states (parseDateTimeWall). The recurrence must be
// generated on that local wall clock: generating it from the UTC instant
// pins every occurrence to the UTC offset in force at DTSTART, which shifts
// the local hour across DST and evaluates BYDAY on the UTC date instead of
// the local one. The reading, not the instant, carries the clock time: a
// DTSTART in a spring-forward gap resolves to an instant after the gap,
// whose clock time the other occurrences do not have.
func recurrenceStart(vevent *ical.Component, floating *time.Location) (start, wall time.Time, err error) {
	p := vevent.Props.Get(ical.PropDateTimeStart)
	if p == nil {
		return time.Time{}, time.Time{}, fmt.Errorf("recurring event has no DTSTART")
	}
	wall, loc, err := parseDateTimeWall(p.Value, p.Params, floating)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("parse DTSTART %q: %w", p.Value, err)
	}
	return localInstant(wall, loc), wall, nil
}
