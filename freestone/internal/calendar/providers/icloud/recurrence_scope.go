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

// applyToDate moves a DATE value by the shift's whole days only. A date has
// no time of day for the clock part to move; running it through the wall
// clock instead pulls it to the previous date on an earlier time, or west
// of UTC, where a DATE's UTC midnight is the evening before.
func (w wallShift) applyToDate(t time.Time) time.Time {
	y, m, d := t.Date()
	return localInstant(time.Date(y, m, d+w.days, 0, 0, 0, 0, time.UTC), t.Location())
}

// apply moves t on the wall clock of its own location. The sum is
// normalised, so a clock change past midnight rolls the date, and an hour
// lost or gained at a daylight-saving change is not counted. The reading
// reached is resolved as the moved series' own readings are
// (localInstant), so both name the same instant in a repeated or skipped
// hour.
func (w wallShift) apply(t time.Time) time.Time {
	return localInstant(wallClock(t).AddDate(0, 0, w.days).Add(w.clock), t.Location())
}

// shiftSeries carries a start move of the series s to every value that
// names one of its instants, then proves the move exact or refuses it. The
// master already carries the new DTSTART; before is the generated set the
// series had before the move.
//
// Every RDATE, EXDATE and RECURRENCE-ID value and the RRULE's UNTIL bound
// is moved by w on the series' wall clock, each in its own form; a DATE
// value moves by w's whole days only, as it has no time of day. The rule's
// other parts are not rewritten, so a part that pins a day, month, hour or
// minute (BYDAY, BYMONTHDAY, BYHOUR, ...) may no longer agree with the moved
// DTSTART, as a weekly Thursday series moved to a Friday shows. The move is
// therefore accepted only when the moved series generates exactly the old
// series' occurrences, each moved by w, compared one by one through both
// sets' iterators to their ends, and when every moved override and
// exclusion still names an occurrence. A series longer than
// maxOccurrenceScan is accepted only if its rule is endless, as the end of
// a bounded one, where a moved bound goes wrong, lies past the scan.
// Otherwise the move is refused; the caller writes nothing, since only the
// in-memory copy has been edited.
func shiftSeries(s *series, w wallShift, seriesLoc, floating *time.Location, before *occurrenceSet) error {
	if w.zero() {
		return nil
	}
	var moved []shiftedValue
	for _, o := range s.overrides {
		vs, err := shiftValues(o.Props, ical.PropRecurrenceID, w, seriesLoc, floating)
		if err != nil {
			return err
		}
		moved = append(moved, vs...)
	}
	vs, err := shiftValues(s.master.Props, ical.PropExceptionDates, w, seriesLoc, floating)
	if err != nil {
		return err
	}
	moved = append(moved, vs...)
	if _, err := shiftValues(s.master.Props, ical.PropRecurrenceDates, w, seriesLoc, floating); err != nil {
		return err
	}
	if err := shiftUntil(s.master.Props, w, seriesLoc); err != nil {
		return err
	}

	after, _, err := generatedSet(s.master, floating)
	if err != nil {
		return err
	}
	allDay := isAllDay(s.master.Props)
	show := func(t time.Time) string {
		if allDay {
			return t.Format(time.DateOnly)
		}
		return t.In(seriesLoc).Format(time.RFC3339)
	}
	refuse := func(cause string) error {
		return fmt.Errorf("moving series %s cannot be carried over exactly, so nothing was changed: %s", s.title(), cause)
	}

	if cause := shiftMismatch(before, after, w, seriesLoc, show); cause != "" {
		if pinned := pinningRuleParts(s.master.Props); pinned != "" {
			cause += "; the rule's " + pinned + " is not rewritten by a move and no longer agrees with the new start"
		}
		return refuse(cause)
	}

	for _, v := range moved {
		ok, err := occursAt(after, v.to)
		if err != nil {
			return err
		}
		if ok {
			continue
		}
		// The sets agree under the shift, so the value was off the series
		// before the move as well: an orphaned override or exclusion.
		if was, err := occursAt(before, v.from); err != nil {
			return err
		} else if !was {
			return refuse(fmt.Sprintf("its %s at %s already names no occurrence of the series, so there is no occurrence to carry it with", v.name, show(v.from)))
		}
		return refuse(fmt.Sprintf("its %s at %s would move to %s, which the moved series does not generate", v.name, show(v.from), show(v.to)))
	}
	return nil
}

// shiftMismatch walks the series' generated set before and after a move in
// step and returns, in words, the first occurrence that is not the old one
// moved by w, or why the walk could not decide; "" when both sets end
// together with every occurrence matched. A walk that reaches
// maxOccurrenceScan is accepted only when both sets are endless (an RRULE
// without UNTIL or COUNT), for which a bounded walk is the best proof there
// is; a bounded set not walked to its end is refused, since its end is
// where a moved UNTIL or COUNT would lose or add occurrences. Iterator() is
// used, never Between().
func shiftMismatch(before, after *occurrenceSet, w wallShift, seriesLoc *time.Location, show func(time.Time) string) string {
	nextBefore, nextAfter := before.iterator(), after.iterator()
	for i := 0; i < maxOccurrenceScan; i++ {
		b, okBefore := nextBefore()
		a, okAfter := nextAfter()
		switch {
		case !okBefore && !okAfter:
			return ""
		case !okAfter:
			return fmt.Sprintf("its occurrence at %s would move to %s, which the moved series no longer generates", show(b), show(w.apply(b.In(seriesLoc))))
		case !okBefore:
			return fmt.Sprintf("the moved series generates an occurrence at %s that no old occurrence moves to", show(a))
		}
		if want := w.apply(b.In(seriesLoc)); !want.Equal(a) {
			return fmt.Sprintf("its occurrence at %s would move to %s, but the moved series generates %s in its place", show(b), show(want), show(a))
		}
	}
	if before.endless() && after.endless() {
		return ""
	}
	return fmt.Sprintf("the series ends at %s but has more than %d occurrences, the most a move is verified over, so the move cannot be verified to the series' end", before.bound(show), maxOccurrenceScan)
}

// pinningRuleParts names the RRULE parts of props that pin an occurrence
// to a day, month, hour or minute, e.g. "BYDAY=TH", or "" if there are none.
func pinningRuleParts(props ical.Props) string {
	p := props.Get(ical.PropRecurrenceRule)
	if p == nil {
		return ""
	}
	var pinned []string
	for _, part := range strings.Split(p.Value, ";") {
		key, _, _ := strings.Cut(part, "=")
		if strings.HasPrefix(strings.ToUpper(key), "BY") {
			pinned = append(pinned, part)
		}
	}
	return strings.Join(pinned, ";")
}

// shiftedValue is one date-time value a move rewrote: the instant it named
// before and after, and the property it belongs to.
type shiftedValue struct {
	name     string
	from, to time.Time
}

// shiftValues rewrites every value of the date-time properties called name
// moved by w on the series' wall clock, each in its own property's form,
// a DATE by w's whole days, and returns each value before and after.
func shiftValues(props ical.Props, name string, w wallShift, seriesLoc, floating *time.Location) ([]shiftedValue, error) {
	var moved []shiftedValue
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
			var shifted time.Time
			if date {
				shifted = w.applyToDate(t)
				values[j] = shifted.Format("20060102")
			} else {
				shifted = w.apply(t.In(seriesLoc))
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
			moved = append(moved, shiftedValue{name: name, from: t, to: shifted})
		}
		p.Value = strings.Join(values, ",")
	}
	return moved, nil
}

// shiftUntil moves the RRULE's UNTIL bound by w on the series' wall clock,
// in UNTIL's own form: a DATE, a UTC DATE-TIME, or a local one. It is read
// by ruleUntil, as the series' recurrence set reads it. A DATE bound moves
// by w's whole days only: it is read as the start of its date, so it
// admits the occurrences on earlier dates, and moving it by the days each
// occurrence's date moves keeps the same ones in the common case.
// shiftMismatch decides whether it does.
func shiftUntil(props ical.Props, w wallShift, seriesLoc *time.Location) error {
	p := props.Get(ical.PropRecurrenceRule)
	if p == nil {
		return nil
	}
	t, layout, err := ruleUntil(p.Value, seriesLoc)
	if err != nil || t.IsZero() {
		return err
	}
	var shifted time.Time
	switch layout {
	case dateLayout:
		shifted = w.applyToDate(t)
	case dateTimeUTCLayout:
		shifted = w.apply(t.In(seriesLoc)).UTC()
	default:
		shifted = w.apply(t)
	}
	parts := strings.Split(p.Value, ";")
	for i, part := range parts {
		if key, _, ok := strings.Cut(part, "="); ok && strings.EqualFold(key, "UNTIL") {
			parts[i] = key + "=" + shifted.Format(layout)
		}
	}
	p.Value = strings.Join(parts, ";")
	return nil
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
