package icloud

import (
	"time"

	"github.com/emersion/go-ical"
	"github.com/teambition/rrule-go"

	"github.com/kai/freestone/internal/calendar"
)

// maxExpansions caps the number of instances to prevent unbounded RRULE explosion.
const maxExpansions = 366

// expandRRule expands a recurring VEVENT into individual instances within the
// given query window [windowStart, windowEnd). If both are zero, falls back to
// a 1-year window from the event's DTSTART to avoid unbounded expansion.
func expandRRule(vevent *ical.Component, base calendar.Event, windowStart, windowEnd time.Time) ([]calendar.Event, error) {
	rruleProp := vevent.Props.Get("RRULE")
	if rruleProp == nil {
		return nil, nil
	}

	dtstart := base.StartUTC
	duration := base.EndUTC.Sub(base.StartUTC)

	ruleStr := "DTSTART:" + dtstart.UTC().Format("20060102T150405Z") + "\nRRULE:" + rruleProp.Value
	rule, err := rrule.StrToRRule(ruleStr)
	if err != nil {
		return nil, err
	}

	// Collect EXDATE values for exclusion.
	exdates := make(map[time.Time]bool)
	for _, p := range vevent.Props.Values("EXDATE") {
		t, _, _, _ := parseDateTime(&p)
		exdates[t.UTC()] = true
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
	next := rule.Iterator()
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
		if exdates[instUTC] {
			continue
		}

		count++
		if count > maxExpansions {
			break
		}

		e := base
		e.StartUTC = instUTC
		e.EndUTC = instUTC.Add(duration)
		e.RecurringParentUID = base.UID
		e.InstanceStartUTC = instUTC

		if base.TZID != "" {
			loc, err := time.LoadLocation(base.TZID)
			if err == nil {
				e.StartLocal = instUTC.In(loc).Format(time.RFC3339)
				e.EndLocal = e.EndUTC.In(loc).Format(time.RFC3339)
			}
		}

		events = append(events, e)
	}

	return events, nil
}
