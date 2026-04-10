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
	Drafts       map[string]DraftReply       // keyed by draft ID (Phase 2)
	draftSeq     int
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

func (f *Fake) SaveDraftReply(_ context.Context, d DraftReply) (string, error) {
	id := fmt.Sprintf("draft-%d", f.draftSeq+1)
	f.draftSeq++
	if f.Drafts == nil {
		f.Drafts = make(map[string]DraftReply)
	}
	f.Drafts[id] = d
	return id, nil
}

func (f *Fake) MarkRead(_ context.Context, mailboxID, id string) error {
	msgs := f.Messages[mailboxID]
	for i, m := range msgs {
		if m.ID == id {
			if !containsFlag(m.Flags, `\Seen`) {
				f.Messages[mailboxID][i].Flags = append(m.Flags, `\Seen`)
			}
			return nil
		}
	}
	return fmt.Errorf("fake: message %s not found in %s", id, mailboxID)
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
