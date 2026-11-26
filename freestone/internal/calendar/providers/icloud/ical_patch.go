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
//
// A recurring object is edited only under calendar.ScopeSeries, and a time
// change then moves the series' RECURRENCE-IDs and EXDATEs with it
// (shiftSeries). DURATION is dropped from any VEVENT that also has DTEND,
// since the object could not be encoded otherwise.
func applyPatch(cal *ical.Calendar, p calendar.EventPatch, now time.Time, floating *time.Location) (*ical.Component, error) {
	s, err := seriesOf(cal, p.UID)
	if err != nil {
		return nil, err
	}
	if err := checkUpdateScope(s, p.Scope); err != nil {
		return nil, err
	}
	master, err := masterEvent(cal, p.UID)
	if err != nil {
		return nil, err
	}
	props := master.Props

	// The shift is measured before DTSTART is rewritten, on the series'
	// own wall clock (UTC dates for an all-day series).
	var shift wallShift
	var seriesLoc *time.Location
	if s.recurring() && p.Start != nil {
		oldStart, err := recurrenceStart(master, floating)
		if err != nil {
			return nil, err
		}
		seriesLoc = oldStart.Location()
		newStart := p.Start.In(seriesLoc)
		if isAllDay(props) {
			newStart = wallDate(*p.Start)
		}
		shift = wallShiftBetween(oldStart, newStart)
	}

	patchText(props, ical.PropSummary, p.Summary)
	patchText(props, ical.PropDescription, p.Description)
	patchText(props, ical.PropLocation, p.Location)

	if isAllDay(props) {
		if err := patchDates(props, p.Start, p.End); err != nil {
			return nil, err
		}
	} else if p.Start != nil || p.End != nil {
		form, err := storedTimeForm(props, floating)
		if err != nil {
			return nil, err
		}
		if p.Start != nil {
			props.Set(form.prop(ical.PropDateTimeStart, *p.Start))
		}
		if p.End != nil {
			// DTEND and DURATION are mutually exclusive (RFC 5545 §3.6.1).
			props.Del(ical.PropDuration)
			props.Set(form.prop(ical.PropDateTimeEnd, *p.End))
		}
	}

	if s.recurring() {
		if err := shiftSeries(s, shift, seriesLoc, floating); err != nil {
			return nil, err
		}
	}
	dropDurationShadowedByDTEnd(cal)

	if err := bumpEdited(props, now); err != nil {
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

// timeForm is the DATE-TIME form of the stored DTSTART, which new DTSTART
// and DTEND values are written in, so a patch moves an event in time but
// never changes its kind of time: a zoned event keeps its TZID string as
// the producer wrote it (a Windows name stays a Windows name, matching the
// VTIMEZONE the object carries), a UTC event stays UTC, and a floating
// event stays floating.
type timeForm struct {
	tzid string         // the stored TZID, "" for UTC and floating
	loc  *time.Location // the location wall-clock values are written in
	utc  bool
}

// storedTimeForm reads the form of the stored DTSTART through
// dateTimeZone, the seam decoding uses, so any TZID that lists also
// updates. A TZID that does not resolve is an error, not a silent switch
// to UTC. Floating values are written in floating, the location they are
// read in.
func storedTimeForm(props ical.Props, floating *time.Location) (timeForm, error) {
	start := props.Get(ical.PropDateTimeStart)
	if start == nil {
		return timeForm{}, fmt.Errorf("VEVENT has no DTSTART")
	}
	loc, utc, err := dateTimeZone(start.Value, start.Params, floating)
	if err != nil {
		return timeForm{}, fmt.Errorf("DTSTART: %w", err)
	}
	return timeForm{tzid: start.Params.Get(ical.ParamTimezoneID), loc: loc, utc: utc}, nil
}

// prop is a DATE-TIME property called name for the instant t in this form.
// go-ical's SetDateTime is not used for local values: it writes the Go
// location's name as TZID, an IANA name no VTIMEZONE in the object defines,
// and it cannot write a floating value at all.
func (f timeForm) prop(name string, t time.Time) *ical.Prop {
	p := ical.NewProp(name)
	if f.utc {
		p.SetDateTime(t.UTC())
		return p
	}
	p.SetValueType(ical.ValueDateTime)
	p.Value = t.In(f.loc).Format(dateTimeLocalLayout)
	if f.tzid != "" {
		p.Params.Set(ical.ParamTimezoneID, f.tzid)
	}
	return p
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
