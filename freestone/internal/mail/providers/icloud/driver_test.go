package icloud

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"

	"github.com/kai/freestone/internal/mail"
)

func TestBuildSearchCriteria_Empty(t *testing.T) {
	q := mail.MessageQuery{}
	c := buildSearchCriteria(q)
	if c.Since != (time.Time{}) {
		t.Errorf("expected zero Since, got %v", c.Since)
	}
	if c.Before != (time.Time{}) {
		t.Errorf("expected zero Before, got %v", c.Before)
	}
}

func TestBuildSearchCriteria_Since(t *testing.T) {
	since := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	q := mail.MessageQuery{Since: since}
	c := buildSearchCriteria(q)
	if c.Since != since {
		t.Errorf("Since = %v, want %v", c.Since, since)
	}
}

func TestBuildSearchCriteria_Before(t *testing.T) {
	before := time.Date(2026, 4, 8, 0, 0, 0, 0, time.UTC)
	q := mail.MessageQuery{Before: before}
	c := buildSearchCriteria(q)
	if c.Before != before {
		t.Errorf("Before = %v, want %v", c.Before, before)
	}
}

func TestBuildSearchCriteria_Unseen(t *testing.T) {
	q := mail.MessageQuery{Unseen: true}
	c := buildSearchCriteria(q)
	found := false
	for _, f := range c.NotFlag {
		if f == imap.FlagSeen {
			found = true
		}
	}
	if !found {
		t.Error("expected NotFlag to contain \\Seen")
	}
}

func TestBuildSearchCriteria_From(t *testing.T) {
	q := mail.MessageQuery{From: "alice@test.com"}
	c := buildSearchCriteria(q)
	if len(c.Header) != 1 || c.Header[0].Key != "From" || c.Header[0].Value != "alice@test.com" {
		t.Errorf("unexpected header criteria: %+v", c.Header)
	}
}

func TestBuildSearchCriteria_Subject(t *testing.T) {
	q := mail.MessageQuery{Subject: "Important"}
	c := buildSearchCriteria(q)
	if len(c.Header) != 1 || c.Header[0].Key != "Subject" || c.Header[0].Value != "Important" {
		t.Errorf("unexpected header criteria: %+v", c.Header)
	}
}

func TestFormatAddress_WithName(t *testing.T) {
	addr := imap.Address{Name: "Alice", Mailbox: "alice", Host: "test.com"}
	got := formatAddress(addr)
	if got != "Alice <alice@test.com>" {
		t.Errorf("got %q", got)
	}
}

func TestFormatAddress_WithoutName(t *testing.T) {
	addr := imap.Address{Mailbox: "bob", Host: "test.com"}
	got := formatAddress(addr)
	if got != "bob@test.com" {
		t.Errorf("got %q", got)
	}
}

func TestDecodeBody_PlainText(t *testing.T) {
	raw := "Content-Type: text/plain\r\n\r\nHello world"
	text, html := decodeBody(strings.NewReader(raw))
	if text != "Hello world" {
		t.Errorf("text = %q, want %q", text, "Hello world")
	}
	if html != "" {
		t.Errorf("html = %q, want empty", html)
	}
}

// D2: fetching a message body must not mark it read. Only MarkRead sets \Seen.
func TestGetMessage_LeavesSeenUnset(t *testing.T) {
	h := newIMAPHarness(t)
	uid := h.seed("INBOX", smokeMessage)

	msg, err := h.driver().GetMessage(context.Background(), "INBOX", strconv.FormatUint(uint64(uid), 10))
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if !strings.Contains(msg.Text, "Body text.") {
		t.Errorf("GetMessage text = %q, want the body (the fetch must still read it)", msg.Text)
	}
	if m := h.message("INBOX", uid); m.hasFlag(imap.FlagSeen) {
		t.Errorf("flags after GetMessage = %v, want no \\Seen", m.Flags)
	}
}

func TestMarkRead_SetsSeen(t *testing.T) {
	h := newIMAPHarness(t)
	uid := h.seed("INBOX", smokeMessage)

	if err := h.driver().MarkRead(context.Background(), "INBOX", strconv.FormatUint(uint64(uid), 10)); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	if m := h.message("INBOX", uid); !m.hasFlag(imap.FlagSeen) {
		t.Errorf("flags after MarkRead = %v, want \\Seen", m.Flags)
	}
}
