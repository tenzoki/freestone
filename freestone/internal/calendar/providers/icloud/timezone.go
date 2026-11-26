package icloud

import (
	"fmt"
	"time"
)

// resolveLocation maps an iCalendar TZID parameter to a Go location. It is
// the single seam every zoned date-time in a VEVENT goes through, so that
// adding another naming scheme (e.g. Windows zone names) changes one place.
//
// Only IANA names are resolved today.
func resolveLocation(tzid string) (*time.Location, error) {
	loc, err := time.LoadLocation(tzid)
	if err != nil {
		return nil, fmt.Errorf("unknown TZID %q: %w", tzid, err)
	}
	return loc, nil
}
