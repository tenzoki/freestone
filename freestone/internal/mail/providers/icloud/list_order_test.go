package icloud

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	"github.com/emersion/go-imap/v2"

	"github.com/kai/freestone/internal/mail"
)

// seedNumbered stores n messages in INBOX, oldest first, and returns their
// UIDs in arrival order.
func seedNumbered(h *imapHarness, n int) []imap.UID {
	uids := make([]imap.UID, n)
	for i := range n {
		uids[i] = h.seed("INBOX", fmt.Sprintf("From: alice@example.com\r\n"+
			"To: user@example.com\r\n"+
			"Subject: message %d\r\n"+
			"Date: Thu, 26 Nov 2026 09:0%d:00 +0100\r\n"+
			"\r\n"+
			"body %d\r\n", i, i, i))
	}
	return uids
}

func listedUIDs(t *testing.T, msgs []mail.MessageSummary) []imap.UID {
	t.Helper()
	uids := make([]imap.UID, len(msgs))
	for i, m := range msgs {
		n, err := strconv.ParseUint(m.ID, 10, 32)
		if err != nil {
			t.Fatalf("summary %d: ID %q is not a UID: %v", i, m.ID, err)
		}
		uids[i] = imap.UID(n)
	}
	return uids
}

// iCloud lists newest first, as Gmail does: the highest UID leads.
func TestIMAPHarness_ListMessages_NewestFirst(t *testing.T) {
	h := newIMAPHarness(t)
	seeded := seedNumbered(h, 5)

	msgs, err := h.driver().ListMessages(context.Background(), mail.MessageQuery{MailboxID: "INBOX"})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	got := listedUIDs(t, msgs)
	want := []imap.UID{seeded[4], seeded[3], seeded[2], seeded[1], seeded[0]}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("listed UIDs = %v, want newest first %v", got, want)
	}
	if msgs[0].Subject != "message 4" {
		t.Errorf("first summary subject = %q, want the newest, %q", msgs[0].Subject, "message 4")
	}
}

// A limit keeps the newest messages, still newest first.
func TestIMAPHarness_ListMessages_LimitKeepsNewest(t *testing.T) {
	h := newIMAPHarness(t)
	seeded := seedNumbered(h, 5)

	msgs, err := h.driver().ListMessages(context.Background(), mail.MessageQuery{MailboxID: "INBOX", Limit: 2})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	got := listedUIDs(t, msgs)
	want := []imap.UID{seeded[4], seeded[3]}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("listed UIDs with limit 2 = %v, want %v", got, want)
	}
}
