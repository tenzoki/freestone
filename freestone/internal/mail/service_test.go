package mail

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestNewRegistry_DuplicateName(t *testing.T) {
	f1 := &Fake{ProviderName: "test"}
	f2 := &Fake{ProviderName: "test"}
	_, err := NewRegistry(f1, f2)
	if err == nil {
		t.Fatal("expected error for duplicate provider name")
	}
}

func TestRegistry_ProviderLookup(t *testing.T) {
	f := &Fake{ProviderName: "icloud"}
	r := NewFakeRegistry(f)

	p, err := r.Provider("icloud")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "icloud" {
		t.Errorf("expected icloud, got %s", p.Name())
	}

	_, err = r.Provider("nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

func TestRegistry_ListAllMailboxes(t *testing.T) {
	f1 := &Fake{
		ProviderName: "icloud",
		Mailboxes: []Mailbox{
			{ID: "INBOX", Name: "Inbox", Messages: 42},
		},
	}
	f2 := &Fake{
		ProviderName: "google",
		Mailboxes: []Mailbox{
			{ID: "INBOX", Name: "Inbox", Messages: 99},
		},
	}
	r := NewFakeRegistry(f1, f2)

	mboxes, err := r.ListAllMailboxes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(mboxes) != 2 {
		t.Fatalf("expected 2 mailboxes, got %d", len(mboxes))
	}

	providers := map[string]bool{}
	for _, m := range mboxes {
		providers[m.Provider] = true
	}
	if !providers["icloud"] || !providers["google"] {
		t.Errorf("expected both providers, got %v", providers)
	}
}

func TestFake_ListMessagesWithLimit(t *testing.T) {
	msgs := make([]MessageSummary, 10)
	for i := range msgs {
		msgs[i] = MessageSummary{
			ID:      fmt.Sprintf("msg-%d", i),
			Subject: fmt.Sprintf("Subject %d", i),
			Date:    time.Now(),
		}
	}
	f := &Fake{
		ProviderName: "icloud",
		Messages:     map[string][]MessageSummary{"INBOX": msgs},
	}

	result, err := f.ListMessages(context.Background(), MessageQuery{
		MailboxID: "INBOX",
		Limit:     3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result))
	}
}

func TestFake_ListMessagesUnseenFilter(t *testing.T) {
	f := &Fake{
		ProviderName: "icloud",
		Messages: map[string][]MessageSummary{
			"INBOX": {
				{ID: "1", Subject: "Read", Flags: []string{"\\Seen"}},
				{ID: "2", Subject: "Unread"},
			},
		},
	}

	result, err := f.ListMessages(context.Background(), MessageQuery{
		MailboxID: "INBOX",
		Unseen:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 unread, got %d", len(result))
	}
	if result[0].ID != "2" {
		t.Errorf("expected msg 2, got %s", result[0].ID)
	}
}

func TestFake_GetMessage(t *testing.T) {
	f := &Fake{
		ProviderName: "icloud",
		FullMessages: map[string]*Message{
			"msg-1": {
				MessageSummary: MessageSummary{ID: "msg-1", Subject: "Hello"},
				Text:           "Hello world",
			},
		},
	}

	msg, err := f.GetMessage(context.Background(), "INBOX", "msg-1")
	if err != nil {
		t.Fatal(err)
	}
	if msg.Text != "Hello world" {
		t.Errorf("got text %q", msg.Text)
	}

	_, err = f.GetMessage(context.Background(), "INBOX", "nonexistent")
	if err == nil {
		t.Fatal("expected error for missing message")
	}
}
