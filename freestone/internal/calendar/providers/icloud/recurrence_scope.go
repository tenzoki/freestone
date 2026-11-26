package icloud

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-ical"

	"github.com/kai/freestone/internal/calendar"
)

// An iCloud series is one calendar object: the master VEVENT and every
// RECURRENCE-ID override share the UID that calendar_list_events gives each
// occurrence. A write addressed by that UID would act on all of them, so a
// write to a recurring object must say so with a scope, and an occurrence
// is addressed by its recurrence ID.

// seriesOf returns the VEVENTs of uid in cal.
func seriesOf(cal *ical.Calendar, uid string) (*series, error) {
	groups, err := groupByUID(cal)
	if err != nil {
		return nil, err
	}
	for _, s := range groups {
		if s.uid == uid {
			return s, nil
		}
	}
	return nil, fmt.Errorf("no VEVENT with UID %q in calendar object", uid)
}

// recurring reports whether the object holds more than one occurrence of
// the event: a master with RRULE or RDATE, or any override.
func (s *series) recurring() bool {
	return len(s.overrides) > 0 || (s.master != nil && isRecurring(s.master))
}

// title names the series in an error: its UID and, if it has one, its
// summary, taken from the master or else the first override.
func (s *series) title() string {
	c := s.master
	if c == nil && len(s.overrides) > 0 {
		c = s.overrides[0]
	}
	if c != nil {
		if p := c.Props.Get(ical.PropSummary); p != nil {
			if text, err := textValue(p); err == nil && text != "" {
				return fmt.Sprintf("%s (%q)", s.uid, text)
			}
		}
	}
	return s.uid
}

// checkUpdateScope decides whether an update with scope may act on s.
func checkUpdateScope(s *series, scope calendar.Scope) error {
	switch scope {
	case calendar.ScopeSeries:
		return nil
	case calendar.ScopeInstance:
		return fmt.Errorf("scope \"instance\" is not accepted for an update: single-occurrence edits are not supported on iCloud; pass scope \"series\" to update every occurrence of the series")
	case calendar.ScopeUnspecified:
		if s.recurring() {
			return fmt.Errorf("event %s is a recurring series and an update changes every occurrence: pass scope \"series\" to update the whole series; single-occurrence edits are not supported on iCloud", s.title())
		}
		return nil
	default:
		return fmt.Errorf("unknown scope %q", scope)
	}
}

// applyDeletion decides what deleting del means for the object cal holds.
// removeObject reports that the whole object goes; otherwise cal has been
// edited in place, one occurrence removed, and is to be written back.
func applyDeletion(cal *ical.Calendar, del calendar.EventDeletion, now time.Time, floating *time.Location) (removeObject bool, err error) {
	if !del.RecurrenceID.IsZero() && del.Scope != calendar.ScopeInstance {
		return false, fmt.Errorf("recurrence_id is used only with scope \"instance\"")
	}
	s, err := seriesOf(cal, del.UID)
	if err != nil {
		return false, err
	}
	switch del.Scope {
	case calendar.ScopeSeries:
		return true, nil
	case calendar.ScopeUnspecified:
		if s.recurring() {
			return false, fmt.Errorf("event %s is a recurring series and a delete removes every occurrence: pass scope \"series\" to delete the whole series, or scope \"instance\" with the occurrence's recurrence_id from calendar_list_events to delete one occurrence", s.title())
		}
		return true, nil
	case calendar.ScopeInstance:
		if !s.recurring() {
			return false, fmt.Errorf("event %s is not recurring, so it has no occurrence to delete with scope \"instance\"; delete it without scope", s.title())
		}
		if del.RecurrenceID.IsZero() {
			return false, fmt.Errorf("scope \"instance\" needs recurrence_id, the occurrence's value as calendar_list_events returns it")
		}
		return removeOccurrence(cal, s, del.RecurrenceID, now, floating)
	default:
		return false, fmt.Errorf("unknown scope %q", del.Scope)
	}
}

// removeOccurrence takes the occurrence of s that originally starts at rid
// out of the series: an EXDATE on the master in the master's own form, so
// the rule no longer generates it, and the override VEVENT that replaced
// it, if there is one. removeObject reports that no VEVENT is left, which
// happens only for an object of overrides without a master.
func removeOccurrence(cal *ical.Calendar, s *series, rid, now time.Time, floating *time.Location) (removeObject bool, err error) {
	allDay := s.master != nil && isAllDay(s.master.Props)
	if allDay {
		rid = wallDate(rid)
	}

	override := -1
	for i, o := range s.overrides {
		p := o.Props.Get(ical.PropRecurrenceID)
		t, err := parseDateTimeValue(p.Value, p.Params, floating)
		if err != nil {
			return false, fmt.Errorf("parse RECURRENCE-ID %q: %w", p.Value, err)
		}
		if t.Equal(rid) {
			override = i
			break
		}
	}

	generated := false
	if s.master != nil {
		set, _, err := recurrenceSet(s.master, floating)
		if err != nil {
			return false, err
		}
		if generated, err = occursAt(set, rid); err != nil {
			return false, err
		}
	}
	if !generated && override < 0 {
		return false, fmt.Errorf("recurrence_id %s is not an occurrence of series %s", rid.Format(time.RFC3339), s.title())
	}

	if generated {
		exdate, err := exclusionFor(s.master.Props, rid, allDay, floating)
		if err != nil {
			return false, err
		}
		s.master.Props.Add(exdate)
		if err := bumpEdited(s.master.Props, now); err != nil {
			return false, err
		}
	}
	if override >= 0 {
		target := s.overrides[override]
		cal.Children = slices.DeleteFunc(cal.Children, func(c *ical.Component) bool { return c == target })
	}

	if !slices.ContainsFunc(cal.Children, func(c *ical.Component) bool { return c.Name == ical.CompEvent }) {
		return true, nil
	}
	dropDurationShadowedByDTEnd(cal)
	return false, nil
}

// exclusionFor is an EXDATE for rid written in the form of the master's
// DTSTART: a DATE for an all-day series, else the DTSTART's TZID, UTC or
// floating form (RFC 5545 §3.8.5.1 asks EXDATE to match DTSTART's type).
func exclusionFor(master ical.Props, rid time.Time, allDay bool, floating *time.Location) (*ical.Prop, error) {
	if allDay {
		p := ical.NewProp(ical.PropExceptionDates)
		p.SetDate(rid)
		return p, nil
	}
	form, err := storedTimeForm(master, floating)
	if err != nil {
		return nil, err
	}
	return form.prop(ical.PropExceptionDates, rid), nil
}

// bumpEdited stamps an edited VEVENT as RFC 5545 asks of a revision:
// DTSTAMP now, SEQUENCE incremented.
func bumpEdited(props ical.Props, now time.Time) error {
	props.SetDateTime(ical.PropDateTimeStamp, now.UTC())
	return bumpSequence(props)
}

// wallShift is how far a series moves on its own wall clock: whole days
// plus a change of the time of day. Applied to each occurrence's local
// start, it moves every occurrence the way regenerating the rule from the
// new DTSTART does, across daylight-saving changes included.
type wallShift struct {
	days  int
	clock time.Duration
}

// wallShiftBetween is the shift that moves from to to; both are in the
// series' location.
func wallShiftBetween(from, to time.Time) wallShift {
	day := func(t time.Time) time.Time {
		y, m, d := t.Date()
		return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	}
	clock := func(t time.Time) time.Duration {
		return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute +
			time.Duration(t.Second())*time.Second + time.Duration(t.Nanosecond())
	}
	return wallShift{
		days:  int(day(to).Sub(day(from)).Hours() / 24),
		clock: clock(to) - clock(from),
	}
}

func (w wallShift) zero() bool { return w.days == 0 && w.clock == 0 }

// apply moves t on the wall clock of its own location. time.Date
// normalises the sum, so a clock change past midnight rolls the date, and
// an hour lost or gained at a daylight-saving change is not counted.
func (w wallShift) apply(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d+w.days, t.Hour(), t.Minute(), t.Second(), t.Nanosecond()+int(w.clock), t.Location())
}

// shiftSeries moves every RECURRENCE-ID and EXDATE of s by the same wall
// shift as its DTSTART moved, so no override or exclusion is left naming
// an instant the series no longer has. The master already carries the new
// DTSTART. Each moved value must be generated by the moved series; if one
// is not (the move puts it on a day or time the rule does not produce, as
// moving a weekly Thursday series to a Friday does), the move cannot be
// carried exactly and is refused.
func shiftSeries(s *series, w wallShift, seriesLoc, floating *time.Location) error {
	if w.zero() {
		return nil
	}
	var moved []time.Time
	for _, o := range s.overrides {
		ts, err := shiftValues(o.Props, ical.PropRecurrenceID, w, seriesLoc, floating)
		if err != nil {
			return err
		}
		moved = append(moved, ts...)
	}
	ts, err := shiftValues(s.master.Props, ical.PropExceptionDates, w, seriesLoc, floating)
	if err != nil {
		return err
	}
	moved = append(moved, ts...)

	set, _, err := generatedSet(s.master, floating)
	if err != nil {
		return err
	}
	for _, t := range moved {
		ok, err := occursAt(set, t)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("moving series %s puts an override or exclusion at %s, which the moved series does not generate; the move cannot be carried over exactly, so nothing was changed", s.title(), t.In(seriesLoc).Format(time.RFC3339))
		}
	}
	return nil
}

// shiftValues rewrites every value of the date-time properties called name
// moved by w on the series' wall clock, each in its own property's form,
// and returns the moved instants.
func shiftValues(props ical.Props, name string, w wallShift, seriesLoc, floating *time.Location) ([]time.Time, error) {
	var moved []time.Time
	list := props[name]
	for i := range list {
		p := &list[i]
		if p.Params.Get(ical.ParamValue) == string(ical.ValuePeriod) {
			return nil, fmt.Errorf("%s;VALUE=PERIOD is not supported", name)
		}
		date := p.Params.Get(ical.ParamValue) == string(ical.ValueDate)
		values := strings.Split(p.Value, ",")
		for j, v := range values {
			t, err := parseDateTimeValue(v, p.Params, floating)
			if err != nil {
				return nil, fmt.Errorf("parse %s %q: %w", name, v, err)
			}
			shifted := w.apply(t.In(seriesLoc))
			if date {
				values[j] = shifted.Format("20060102")
				shifted = wallDate(shifted)
			} else {
				loc, utc, err := dateTimeZone(v, p.Params, floating)
				if err != nil {
					return nil, err
				}
				if utc {
					values[j] = shifted.UTC().Format(dateTimeUTCLayout)
				} else {
					values[j] = shifted.In(loc).Format(dateTimeLocalLayout)
				}
			}
			moved = append(moved, shifted)
		}
		p.Value = strings.Join(values, ",")
	}
	return moved, nil
}

// dropDurationShadowedByDTEnd removes DURATION from every VEVENT that also
// carries DTEND. RFC 5545 §3.6.1 forbids the pair and go-ical refuses to
// encode it, so an object holding one could not be written back at all;
// decodeSpan already reads such an event by its DTEND, so dropping the
// DURATION keeps the span every caller has seen.
func dropDurationShadowedByDTEnd(cal *ical.Calendar) {
	for _, c := range cal.Children {
		if c.Name != ical.CompEvent {
			continue
		}
		if c.Props.Get(ical.PropDateTimeEnd) != nil && c.Props.Get(ical.PropDuration) != nil {
			c.Props.Del(ical.PropDuration)
		}
	}
}
