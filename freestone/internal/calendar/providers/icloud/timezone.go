package icloud

import (
	_ "embed"
	"encoding/xml"
	"fmt"
	"sync"
	"time"
)

// windowsZonesXML is the CLDR Windows-to-IANA zone table, copied verbatim
// from upstream (see tzdata/SOURCE.md). Exchange and Outlook write Windows
// zone names such as "W. Europe Standard Time" into TZID.
//
//go:embed tzdata/windowsZones.xml
var windowsZonesXML []byte

// windowsZones maps a Windows zone name to the IANA name CLDR gives for
// territory "001", the zone's default region. It is parsed once, on the
// first TZID that is not an IANA name.
var windowsZones = sync.OnceValues(func() (map[string]string, error) {
	return parseWindowsZones(windowsZonesXML)
})

// parseWindowsZones reads the territory-001 mappings out of a CLDR
// windowsZones.xml. The file's DOCTYPE names a DTD that is not shipped;
// encoding/xml reports it as a directive and never fetches it.
func parseWindowsZones(data []byte) (map[string]string, error) {
	var doc struct {
		MapZones []struct {
			Other     string `xml:"other,attr"`
			Territory string `xml:"territory,attr"`
			Type      string `xml:"type,attr"`
		} `xml:"windowsZones>mapTimezones>mapZone"`
	}
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse windowsZones table: %w", err)
	}
	zones := make(map[string]string)
	for _, z := range doc.MapZones {
		if z.Territory == "001" {
			zones[z.Other] = z.Type
		}
	}
	if len(zones) == 0 {
		return nil, fmt.Errorf("parse windowsZones table: no territory 001 mappings")
	}
	return zones, nil
}

// resolveLocation maps an iCalendar TZID parameter to a Go location. It is
// the single seam every zoned date-time in a VEVENT goes through: an IANA
// name first, then a Windows zone name via the CLDR table, else an error.
//
// VTIMEZONE components are deliberately not interpreted: IANA and Windows
// names cover the producers this server meets (iCloud, Google, Exchange,
// Outlook), evaluating STANDARD/DAYLIGHT rules would be a second time-zone
// engine, and a TZID neither table knows fails visibly here.
func resolveLocation(tzid string) (*time.Location, error) {
	loc, ianaErr := time.LoadLocation(tzid)
	if ianaErr == nil {
		return loc, nil
	}
	zones, err := windowsZones()
	if err != nil {
		return nil, fmt.Errorf("resolve TZID %q: %w", tzid, err)
	}
	iana, ok := zones[tzid]
	if !ok {
		return nil, fmt.Errorf("unknown TZID %q", tzid)
	}
	loc, err = time.LoadLocation(iana)
	if err != nil {
		return nil, fmt.Errorf("TZID %q maps to %q: %w", tzid, iana, err)
	}
	return loc, nil
}
