package mail

import (
	"context"
	"fmt"
)

// Fake is an in-memory mail provider for testing.
type Fake struct {
	ProviderName string
	Mailboxes    []Mailbox
	Messages     map[string][]MessageSummary // keyed by mailbox ID
	FullMessages map[string]*Message         // keyed by message ID
}

func (f *Fake) Name() string { return f.ProviderName }

func (f *Fake) ListMailboxes(_ context.Context) ([]Mailbox, error) {
	return f.Mailboxes, nil
}

func (f *Fake) ListMessages(_ context.Context, q MessageQuery) ([]MessageSummary, error) {
	msgs := f.Messages[q.MailboxID]
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	var result []MessageSummary
	for _, m := range msgs {
		if q.Unseen && containsFlag(m.Flags, "\\Seen") {
			continue
		}
		if q.From != "" && m.From != q.From {
			continue
		}
		m.Provider = f.ProviderName
		result = append(result, m)
		if len(result) >= limit {
			break
		}
	}
	return result, nil
}

func (f *Fake) GetMessage(_ context.Context, _, id string) (*Message, error) {
	msg, ok := f.FullMessages[id]
	if !ok {
		return nil, fmt.Errorf("fake: message %s not found", id)
	}
	msg.Provider = f.ProviderName
	return msg, nil
}

func (f *Fake) SaveDraftReply(_ context.Context, _ DraftReply) (string, error) {
	return "", fmt.Errorf("fake: save draft not implemented in phase 1")
}

func (f *Fake) MarkRead(_ context.Context, _, _ string) error {
	return fmt.Errorf("fake: mark read not implemented in phase 1")
}

func containsFlag(flags []string, target string) bool {
	for _, f := range flags {
		if f == target {
			return true
		}
	}
	return false
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
