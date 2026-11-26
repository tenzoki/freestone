package icloud

import (
	"fmt"
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
	duration := base.EndUTC.Sub(base.StartUTC)

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
	next := set.Iterator()
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
		e.EndUTC = instUTC.Add(duration)
		e.RecurringParentUID = base.UID
		e.InstanceStartUTC = instUTC
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
func recurrenceSet(vevent *ical.Component, floating *time.Location) (*rrule.Set, time.Time, error) {
	dtstart, err := recurrenceStart(vevent, floating)
	if err != nil {
		return nil, time.Time{}, err
	}

	set := &rrule.Set{}
	set.DTStart(dtstart)
	// DTSTART is always the first instance of the set, also when only RDATEs
	// define the rest. The set iterator drops the duplicate the RRULE yields.
	set.RDate(dtstart)

	if p := vevent.Props.Get(ical.PropRecurrenceRule); p != nil {
		opt, err := rrule.StrToROptionInLocation(p.Value, dtstart.Location())
		if err != nil {
			return nil, time.Time{}, fmt.Errorf("parse RRULE %q: %w", p.Value, err)
		}
		opt.Dtstart = dtstart
		rule, err := rrule.NewRRule(*opt)
		if err != nil {
			return nil, time.Time{}, fmt.Errorf("build RRULE %q: %w", p.Value, err)
		}
		set.RRule(rule)
	}

	for _, p := range vevent.Props.Values(ical.PropRecurrenceDates) {
		dates, err := parseDateTimeValues(&p, floating)
		if err != nil {
			return nil, time.Time{}, err
		}
		for _, d := range dates {
			set.RDate(d)
		}
	}
	for _, p := range vevent.Props.Values(ical.PropExceptionDates) {
		dates, err := parseDateTimeValues(&p, floating)
		if err != nil {
			return nil, time.Time{}, err
		}
		for _, d := range dates {
			set.ExDate(d)
		}
	}
	return set, dtstart, nil
}

// recurrenceStart returns the VEVENT's DTSTART in the event's own location.
// The recurrence must be generated on that local wall clock: generating it
// from the UTC instant pins every occurrence to the UTC offset in force at
// DTSTART, which shifts the local hour across DST and evaluates BYDAY on the
// UTC date instead of the local one.
func recurrenceStart(vevent *ical.Component, floating *time.Location) (time.Time, error) {
	p := vevent.Props.Get(ical.PropDateTimeStart)
	if p == nil {
		return time.Time{}, fmt.Errorf("recurring event has no DTSTART")
	}
	start, _, _, err := parseDateTime(p, floating)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse DTSTART %q: %w", p.Value, err)
	}
	return start, nil
}
