package google

import (
	"testing"
	"time"

	"google.golang.org/api/gmail/v1"

	domain "github.com/kai/freestone/internal/mail"
)

func TestBuildGmailQuery(t *testing.T) {
	tests := []struct {
		name string
		q    domain.MessageQuery
		want string
	}{
		{
			name: "empty",
			q:    domain.MessageQuery{},
			want: "",
		},
		{
			name: "from only",
			q:    domain.MessageQuery{From: "alice@test.com"},
			want: "from:alice@test.com",
		},
		{
			name: "subject and unseen",
			q:    domain.MessageQuery{Subject: "Invoice", Unseen: true},
			want: "subject:Invoice is:unread",
		},
		{
			name: "date range",
			q: domain.MessageQuery{
				Since:  time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
				Before: time.Date(2026, 4, 8, 0, 0, 0, 0, time.UTC),
			},
			want: "after:2026/04/01 before:2026/04/08",
		},
		{
			name: "combined",
			q: domain.MessageQuery{
				From:    "bob@test.com",
				Subject: "Report",
				Since:   time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
				Unseen:  true,
			},
			want: "from:bob@test.com subject:Report after:2026/03/01 is:unread",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildGmailQuery(tt.q)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMessageToSummary(t *testing.T) {
	msg := &gmail.Message{
		Id:           "msg-123",
		InternalDate: 1712563200000, // 2024-04-08T08:00:00Z in ms
		SizeEstimate: 1024,
		LabelIds:     []string{"INBOX", "UNREAD"},
		Payload: &gmail.MessagePart{
			Headers: []*gmail.MessagePartHeader{
				{Name: "Subject", Value: "Test Subject"},
				{Name: "From", Value: "alice@example.com"},
				{Name: "To", Value: "bob@example.com, carol@example.com"},
				{Name: "Date", Value: "Mon, 8 Apr 2024 10:00:00 +0200"},
			},
		},
	}

	s := messageToSummary(msg)

	if s.Provider != "google" {
		t.Errorf("provider = %q", s.Provider)
	}
	if s.ID != "msg-123" {
		t.Errorf("id = %q", s.ID)
	}
	if s.Subject != "Test Subject" {
		t.Errorf("subject = %q", s.Subject)
	}
	if s.From != "alice@example.com" {
		t.Errorf("from = %q", s.From)
	}
	if len(s.To) != 2 {
		t.Errorf("to len = %d", len(s.To))
	}
	if s.MailboxID != "INBOX" {
		t.Errorf("mailboxID = %q, want INBOX", s.MailboxID)
	}
	if s.Size != 1024 {
		t.Errorf("size = %d", s.Size)
	}
	if s.Date.IsZero() {
		t.Error("expected non-zero date")
	}
}

func TestMessageToSummary_InternalDateFallback(t *testing.T) {
	msg := &gmail.Message{
		Id:           "msg-456",
		InternalDate: 1712563200000,
		Payload: &gmail.MessagePart{
			Headers: []*gmail.MessagePartHeader{
				{Name: "Subject", Value: "No Date Header"},
			},
		},
	}

	s := messageToSummary(msg)
	if s.Date.IsZero() {
		t.Error("expected internalDate fallback")
	}
}

func TestSplitAddresses(t *testing.T) {
	addrs := splitAddresses("alice@test.com, bob@test.com,  carol@test.com ")
	if len(addrs) != 3 {
		t.Errorf("len = %d, want 3", len(addrs))
	}
}

func TestSplitAddresses_Empty(t *testing.T) {
	addrs := splitAddresses("")
	if addrs != nil {
		t.Errorf("expected nil, got %v", addrs)
	}
}
