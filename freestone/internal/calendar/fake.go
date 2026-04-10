package calendar

import (
	"context"
	"fmt"
)

// Fake is an in-memory calendar provider for testing.
type Fake struct {
	ProviderName string
	Calendars    []Calendar
	Events       map[string][]Event // keyed by calendar ID
}

func (f *Fake) Name() string { return f.ProviderName }

func (f *Fake) ListCalendars(_ context.Context) ([]Calendar, error) {
	return f.Calendars, nil
}

// ListEvents returns events with StartUTC in the half-open interval
// [q.Start, q.End), matching standard CalDAV time-range semantics.
func (f *Fake) ListEvents(_ context.Context, q EventQuery) ([]Event, error) {
	events := f.Events[q.CalendarID]
	var result []Event
	for _, e := range events {
		if !q.Start.IsZero() && e.StartUTC.Before(q.Start) {
			continue
		}
		if !q.End.IsZero() && !e.StartUTC.Before(q.End) {
			continue
		}
		e.Provider = f.ProviderName
		result = append(result, e)
	}
	return result, nil
}

func (f *Fake) GetEvent(_ context.Context, calendarID, uid string) (*Event, error) {
	for _, e := range f.Events[calendarID] {
		if e.UID == uid {
			e.Provider = f.ProviderName
			return &e, nil
		}
	}
	return nil, fmt.Errorf("fake: event %s not found in %s", uid, calendarID)
}

func (f *Fake) CreateEvent(_ context.Context, calendarID string, e Event) (*Event, error) {
	e.Provider = f.ProviderName
	e.CalendarID = calendarID
	if e.UID == "" {
		e.UID = fmt.Sprintf("fake-%d", len(f.Events[calendarID])+1)
	}
	f.Events[calendarID] = append(f.Events[calendarID], e)
	return &e, nil
}

func (f *Fake) UpdateEvent(_ context.Context, e Event) (*Event, error) {
	events := f.Events[e.CalendarID]
	for i, existing := range events {
		if existing.UID == e.UID {
			e.Provider = f.ProviderName
			f.Events[e.CalendarID][i] = e
			return &e, nil
		}
	}
	return nil, fmt.Errorf("fake: event %s not found", e.UID)
}

func (f *Fake) DeleteEvent(_ context.Context, calendarID, uid, _ string) error {
	events := f.Events[calendarID]
	for i, e := range events {
		if e.UID == uid {
			f.Events[calendarID] = append(events[:i], events[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("fake: event %s not found", uid)
}

// NewFakeRegistry creates a Registry populated with the given fakes.
func NewFakeRegistry(fakes ...*Fake) *Registry {
	providers := make([]Provider, len(fakes))
	for i, f := range fakes {
		providers[i] = f
	}
	r, err := NewRegistry(providers...)
	if err != nil {
		panic(err)
	}
	return r
}
