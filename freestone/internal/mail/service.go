// Package mail defines the domain types and provider interface for
// mail operations. Provider drivers live in subdirectories.
package mail

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Mailbox represents a mail folder / label.
type Mailbox struct {
	Provider   string   `json:"provider"`
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Delimiter  string   `json:"delimiter,omitempty"`
	Attributes []string `json:"attributes,omitempty"`
	Messages   uint32   `json:"messages"`
	Unseen     uint32   `json:"unseen"`
}

// MessageSummary is a lightweight message representation for list views.
type MessageSummary struct {
	Provider  string    `json:"provider"`
	MailboxID string    `json:"mailbox_id"`
	ID        string    `json:"id"`
	Subject   string    `json:"subject"`
	From      string    `json:"from"`
	To        []string  `json:"to,omitempty"`
	Date      time.Time `json:"date"`
	Flags     []string  `json:"flags,omitempty"`
	Size      uint32    `json:"size"`
}

// Message is a full message with headers and body.
type Message struct {
	MessageSummary
	Headers map[string]string `json:"headers,omitempty"`
	Text    string            `json:"text,omitempty"`
	HTML    string            `json:"html,omitempty"`
	// Warnings names each part of the message that could not be fully
	// decoded (an unsupported charset, for instance) and is returned as
	// received instead.
	Warnings []string `json:"warnings,omitempty"`
}

// MessageQuery specifies filters for listing messages.
type MessageQuery struct {
	Provider  string
	MailboxID string
	Since     time.Time
	Before    time.Time
	Unseen    bool
	From      string
	Subject   string
	Limit     int
}

// DraftReply contains the data needed to save a reply draft.
type DraftReply struct {
	Provider           string
	InReplyToMailboxID string
	InReplyToID        string
	Subject            string
	BodyText           string
	BodyHTML           string
	To                 []string
	Cc                 []string
	Bcc                []string
}

// Provider defines the contract each mail backend must implement.
type Provider interface {
	Name() string
	ListMailboxes(ctx context.Context) ([]Mailbox, error)
	ListMessages(ctx context.Context, q MessageQuery) ([]MessageSummary, error)
	GetMessage(ctx context.Context, mailboxID, id string) (*Message, error)
	// Phase 2 write operations
	SaveDraftReply(ctx context.Context, d DraftReply) (string, error)
	MarkRead(ctx context.Context, mailboxID, id string) error
}

// Registry routes mail operations to the appropriate provider.
type Registry struct {
	providers map[string]Provider
}

// NewRegistry creates a Registry from one or more providers.
func NewRegistry(ps ...Provider) (*Registry, error) {
	m := make(map[string]Provider, len(ps))
	for _, p := range ps {
		name := p.Name()
		if _, exists := m[name]; exists {
			return nil, fmt.Errorf("mail: duplicate provider name %q", name)
		}
		m[name] = p
	}
	return &Registry{providers: m}, nil
}

// Provider returns the named provider or an error.
func (r *Registry) Provider(name string) (Provider, error) {
	p, ok := r.providers[name]
	if !ok {
		return nil, fmt.Errorf("mail: unknown provider %q", name)
	}
	return p, nil
}

// ListAllMailboxes aggregates mailboxes from every registered provider.
// If some providers fail, partial results from successful providers are
// still returned. Errors are collected and returned as a single joined error.
func (r *Registry) ListAllMailboxes(ctx context.Context) ([]Mailbox, error) {
	var all []Mailbox
	var errs []string
	for _, name := range r.ProviderNames() {
		p := r.providers[name]
		mboxes, err := p.ListMailboxes(ctx)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		for i := range mboxes {
			mboxes[i].Provider = name
		}
		all = append(all, mboxes...)
	}
	if len(errs) > 0 && len(all) == 0 {
		return nil, fmt.Errorf("mail: all providers failed: %s", strings.Join(errs, "; "))
	}
	// Partial success: return results even if some providers failed.
	return all, nil
}

// ProviderNames returns the names of all registered providers in sorted order.
func (r *Registry) ProviderNames() []string {
	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
