package icloud

import (
	"fmt"
	"strconv"
	"time"

	"github.com/emersion/go-ical"

	"github.com/kai/freestone/internal/calendar"
)

// applyPatch edits the master VEVENT of cal in place: only the properties
// the patch sets are touched, so every other property and sub-component
// (RRULE, VALARM, VTIMEZONE, ATTENDEE parameters, X- properties, overrides)
// is written back exactly as it was read. It returns the edited VEVENT.
func applyPatch(cal *ical.Calendar, p calendar.EventPatch, now time.Time) (*ical.Component, error) {
	master, err := masterEvent(cal, p.UID)
	if err != nil {
		return nil, err
	}
	props := master.Props

	patchText(props, ical.PropSummary, p.Summary)
	patchText(props, ical.PropDescription, p.Description)
	patchText(props, ical.PropLocation, p.Location)

	if isAllDay(props) {
		if err := patchDates(props, p.Start, p.End); err != nil {
			return nil, err
		}
	} else if p.Start != nil || p.End != nil {
		loc, err := startLocation(props)
		if err != nil {
			return nil, err
		}
		if p.Start != nil {
			props.SetDateTime(ical.PropDateTimeStart, p.Start.In(loc))
		}
		if p.End != nil {
			// DTEND and DURATION are mutually exclusive (RFC 5545 §3.6.1).
			props.Del(ical.PropDuration)
			props.SetDateTime(ical.PropDateTimeEnd, p.End.In(loc))
		}
	}

	props.SetDateTime(ical.PropDateTimeStamp, now.UTC())
	if err := bumpSequence(props); err != nil {
		return nil, err
	}
	return master, nil
}

// masterEvent returns the VEVENT with the given UID that carries no
// RECURRENCE-ID, i.e. the series master or the only instance.
func masterEvent(cal *ical.Calendar, uid string) (*ical.Component, error) {
	for _, child := range cal.Children {
		if child.Name != ical.CompEvent || child.Props.Get(ical.PropRecurrenceID) != nil {
			continue
		}
		if p := child.Props.Get(ical.PropUID); p != nil && p.Value == uid {
			return child, nil
		}
	}
	return nil, fmt.Errorf("no VEVENT with UID %q and no RECURRENCE-ID in calendar object", uid)
}

// patchText applies one text field of a patch: nil keeps, "" removes the
// property, anything else replaces it (escaped by SetText).
func patchText(props ical.Props, name string, v *string) {
	switch {
	case v == nil:
	case *v == "":
		props.Del(name)
	default:
		props.SetText(name, *v)
	}
}

// isAllDay reports whether the stored DTSTART is a DATE, i.e. the event
// is an all-day event and its times are written as dates.
func isAllDay(props ical.Props) bool {
	start := props.Get(ical.PropDateTimeStart)
	return start != nil && start.Params.Get(ical.ParamValue) == string(ical.ValueDate)
}

// patchDates writes new times of an all-day event as DATE values. The date
// is the one on the caller's own wall clock, not after conversion to UTC,
// so 2026-11-26T00:00:00+01:00 stays the 26th. DTEND is exclusive and must
// fall after DTSTART.
func patchDates(props ical.Props, start, end *time.Time) error {
	if start != nil && end != nil && !wallDate(*end).After(wallDate(*start)) {
		return fmt.Errorf("all-day end date %s must be after start date %s",
			end.Format(time.DateOnly), start.Format(time.DateOnly))
	}
	if start != nil {
		props.SetDate(ical.PropDateTimeStart, wallDate(*start))
	}
	if end != nil {
		// DTEND and DURATION are mutually exclusive (RFC 5545 §3.6.1).
		props.Del(ical.PropDuration)
		props.SetDate(ical.PropDateTimeEnd, wallDate(*end))
	}
	return nil
}

// wallDate is the calendar date of t in t's own location, as UTC midnight.
func wallDate(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// startLocation is the zone new DTSTART/DTEND values are written in: the
// existing DTSTART's TZID when it carries one, else UTC. A TZID that does
// not resolve is an error, not a silent switch to UTC.
func startLocation(props ical.Props) (*time.Location, error) {
	start := props.Get(ical.PropDateTimeStart)
	if start == nil {
		return time.UTC, nil
	}
	tzid := start.Params.Get(ical.PropTimezoneID)
	if tzid == "" {
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(tzid)
	if err != nil {
		return nil, fmt.Errorf("DTSTART TZID %q does not resolve: %w", tzid, err)
	}
	return loc, nil
}

// bumpSequence increments SEQUENCE (absent means 0, RFC 5545 §3.8.7.4).
func bumpSequence(props ical.Props) error {
	seq := 0
	if p := props.Get(ical.PropSequence); p != nil {
		n, err := p.Int()
		if err != nil {
			return fmt.Errorf("parse SEQUENCE %q: %w", p.Value, err)
		}
		seq = n
	}
	p := ical.NewProp(ical.PropSequence)
	p.Value = strconv.Itoa(seq + 1)
	props.Set(p)
	return nil
}
