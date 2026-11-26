package icloud

import (
	"bytes"
	"context"
	"strconv"
	"testing"

	"github.com/emersion/go-imap/v2"
	gomail "github.com/emersion/go-message/mail"

	"github.com/kai/freestone/internal/mail"
)

// --- D7: conformant, threaded, injection-safe drafts ---

// threadParent is a message deep in a conversation: it has its own
// References chain, which a reply must extend rather than restart.
const threadParent = "From: Alice <alice@example.com>\r\n" +
	"To: user@example.com\r\n" +
	"Subject: Projekt\r\n" +
	"Date: Thu, 26 Nov 2026 09:00:00 +0000\r\n" +
	"Message-ID: <2@x>\r\n" +
	"In-Reply-To: <1@x>\r\n" +
	"References: <1@x>\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"Second message.\r\n"

// onlyDraft returns the single message in Drafts, parsed.
func onlyDraft(t *testing.T, h *imapHarness) (storedMessage, *gomail.Header) {
	t.Helper()
	drafts := h.drafts()
	if len(drafts) != 1 {
		t.Fatalf("Drafts holds %d messages, want 1", len(drafts))
	}
	mr, err := gomail.CreateReader(bytes.NewReader(drafts[0].Raw))
	if err != nil {
		t.Fatalf("parse draft: %v\n%s", err, drafts[0].Raw)
	}
	return drafts[0], &mr.Header
}

func TestIMAPHarness_SaveDraftReply_ThreadedWithFromAndDraftFlag(t *testing.T) {
	h := newIMAPHarness(t)
	parent := h.seed("INBOX", threadParent)

	_, err := h.driver().SaveDraftReply(context.Background(), mail.DraftReply{
		InReplyToMailboxID: "INBOX",
		InReplyToID:        strconv.FormatUint(uint64(parent), 10),
		To:                 []string{"Alice <alice@example.com>"},
		Subject:            "Re: Projekt",
		BodyText:           "Danke.",
	})
	if err != nil {
		t.Fatalf("SaveDraftReply: %v", err)
	}

	draft, hdr := onlyDraft(t, h)
	if !draft.hasFlag(imap.FlagDraft) {
		t.Errorf("draft flags = %v, want \\Draft", draft.Flags)
	}
	from, err := hdr.AddressList("From")
	if err != nil || len(from) != 1 || from[0].Address != harnessUser {
		t.Errorf("From = %q (%v), want %s", hdr.Get("From"), err, harnessUser)
	}
	if got := hdr.Get("In-Reply-To"); got != "<2@x>" {
		t.Errorf("In-Reply-To = %q, want <2@x>", got)
	}
	if got := hdr.Get("References"); got != "<1@x> <2@x>" {
		t.Errorf("References = %q, want <1@x> <2@x>", got)
	}
	// The parent lookup reads headers with PEEK: the parent stays unread.
	if m := h.message("INBOX", parent); m.hasFlag(imap.FlagSeen) {
		t.Errorf("parent flags after reply = %v, want no \\Seen", m.Flags)
	}
}

func TestIMAPHarness_SaveDraftReply_HTMLOnlyIsTextHTML(t *testing.T) {
	h := newIMAPHarness(t)
	_, err := h.driver().SaveDraftReply(context.Background(), mail.DraftReply{
		To:       []string{"alice@example.com"},
		Subject:  "Hallo",
		BodyHTML: "<p>Grüße</p>",
	})
	if err != nil {
		t.Fatalf("SaveDraftReply: %v", err)
	}
	_, hdr := onlyDraft(t, h)
	if mediaType, _, err := hdr.ContentType(); err != nil || mediaType != "text/html" {
		t.Errorf("Content-Type = %q (%v), want text/html", hdr.Get("Content-Type"), err)
	}
}

func TestIMAPHarness_SaveDraftReply_RejectedInputSavesNothing(t *testing.T) {
	cases := map[string]mail.DraftReply{
		"CRLF in subject":    {To: []string{"alice@example.com"}, Subject: "hi\r\nBcc: evil@example.com", BodyText: "x"},
		"CRLF in recipient":  {To: []string{"alice@example.com\r\nBcc: evil@example.com"}, Subject: "hi", BodyText: "x"},
		"invalid recipient":  {To: []string{"not an address"}, Subject: "hi", BodyText: "x"},
		"missing parent":     {InReplyToID: "999", To: []string{"alice@example.com"}, Subject: "Re: hi", BodyText: "x"},
		"unparseable parent": {InReplyToID: "abc", To: []string{"alice@example.com"}, Subject: "Re: hi", BodyText: "x"},
	}
	for name, draft := range cases {
		t.Run(name, func(t *testing.T) {
			h := newIMAPHarness(t)
			h.seed("INBOX", threadParent)
			if _, err := h.driver().SaveDraftReply(context.Background(), draft); err == nil {
				t.Errorf("SaveDraftReply = nil error, want rejection")
			}
			if drafts := h.drafts(); len(drafts) != 0 {
				t.Errorf("Drafts holds %d messages after a rejected draft, want 0:\n%s", len(drafts), drafts[0].Raw)
			}
		})
	}
}
