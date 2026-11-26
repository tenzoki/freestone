package icloud

import "time"

// localInstant is the instant a wall-clock reading names in loc, read as
// RFC 5545 §3.3.5 reads a local DATE-TIME. A reading that occurs twice, in
// the hour a fall-back repeats, is its first occurrence. A reading that
// does not occur, in the gap a spring-forward skips, is read with the
// offset in effect before the gap, so it names the instant that far after
// the gap starts (02:30 in a 02:00-03:00 gap is 03:30 after it).
//
// wall carries the reading in its fields and its location is ignored;
// fields out of range are normalised as time.Date does. Every zoned or
// floating value of the package goes through here: time.Date alone does
// not promise which instant it picks, and in practice picks the second
// occurrence of a repeated reading.
func localInstant(wall time.Time, loc *time.Location) time.Time {
	y, mo, d := wall.Date()
	h, mi, s := wall.Clock()
	w := time.Date(y, mo, d, h, mi, s, wall.Nanosecond(), time.UTC)
	t := time.Date(y, mo, d, h, mi, s, wall.Nanosecond(), loc)
	_, off := t.Zone()
	shown := t.Add(time.Duration(off) * time.Second).UTC()

	switch {
	case shown.Equal(w):
		// The reading occurs. It occurs twice if the zone period t lies in
		// began by setting the clock back past it; the first occurrence is
		// then in the period before.
		start, _ := t.ZoneBounds()
		if start.IsZero() {
			return t
		}
		_, prevOff := start.Add(-time.Nanosecond).Zone()
		if prevOff <= off {
			return t
		}
		if earlier := t.Add(-time.Duration(prevOff-off) * time.Second); earlier.Before(start) {
			return earlier
		}
		return t
	case shown.After(w):
		// The reading is in a gap and t lies after it: read it with the
		// offset of the period before the gap.
		start, _ := t.ZoneBounds()
		_, prevOff := start.Add(-time.Nanosecond).Zone()
		return w.Add(-time.Duration(prevOff) * time.Second).In(loc)
	default:
		// The reading is in a gap and t lies before it, in the period
		// whose offset is the one before the gap.
		return w.Add(-time.Duration(off) * time.Second).In(loc)
	}
}

// wallClock is t's reading on the wall clock of its own location, carried
// in the fields of a UTC time, as localInstant takes it.
func wallClock(t time.Time) time.Time {
	y, mo, d := t.Date()
	h, mi, s := t.Clock()
	return time.Date(y, mo, d, h, mi, s, t.Nanosecond(), time.UTC)
}
