package google

import (
	"strings"
	"testing"

	"google.golang.org/api/calendar/v3"
)

// A malformed originalStartTime must fail the conversion: a zero
// instance_start_utc would be indistinguishable from "not an instance".
func TestEventFromGoogle_MalformedOriginalStartTime(t *testing.T) {
	cases := map[string]*calendar.EventDateTime{
		"dateTime": {DateTime: "2026-04-08 10:00"},
		"date":     {Date: "08.04.2026"},
	}
	for name, ost := range cases {
		t.Run(name, func(t *testing.T) {
			item := &calendar.Event{
				Id:                "evt-123_20260408T100000Z",
				RecurringEventId:  "evt-123",
				Start:             &calendar.EventDateTime{DateTime: "2026-04-08T10:00:00Z"},
				End:               &calendar.EventDateTime{DateTime: "2026-04-08T11:00:00Z"},
				OriginalStartTime: ost,
			}
			ev, err := eventFromGoogle(item)
			if err == nil {
				t.Fatalf("expected an error, got event with InstanceStartUTC %v", ev.InstanceStartUTC)
			}
			if !strings.Contains(err.Error(), "originalStartTime") || !strings.Contains(err.Error(), item.Id) {
				t.Errorf("error %q does not name the field and the event", err)
			}
		})
	}
}
