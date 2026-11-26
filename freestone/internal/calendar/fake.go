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
func (f *Fake) ListEvents(_ context.Context, q EventQuery) (EventList, error) {
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
	return EventList{Events: result}, nil
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

// UpdateEvent applies the patch to the stored event, as the real providers
// do: fields the patch leaves nil keep their stored value.
func (f *Fake) UpdateEvent(_ context.Context, p EventPatch) (*Event, error) {
	events := f.Events[p.CalendarID]
	for i := range events {
		if events[i].UID != p.UID {
			continue
		}
		e := &events[i]
		if p.Summary != nil {
			e.Summary = *p.Summary
		}
		if p.Description != nil {
			e.Description = *p.Description
		}
		if p.Location != nil {
			e.Location = *p.Location
		}
		if p.Start != nil {
			e.StartUTC = p.Start.UTC()
		}
		if p.End != nil {
			e.EndUTC = p.End.UTC()
		}
		out := *e
		out.Provider = f.ProviderName
		return &out, nil
	}
	return nil, fmt.Errorf("fake: event %s not found", p.UID)
}

// DeleteEvent removes the event by UID; the fake holds no series, so the
// deletion's scope and recurrence ID are not consulted.
func (f *Fake) DeleteEvent(_ context.Context, d EventDeletion) error {
	events := f.Events[d.CalendarID]
	for i, e := range events {
		if e.UID == d.UID {
			f.Events[d.CalendarID] = append(events[:i], events[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("fake: event %s not found", d.UID)
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
