package icloud

import (
	"context"
	"strings"
	"testing"

	"github.com/kai/freestone/internal/mail"
)

func TestIMAPHarness_SaveDraftReply_ConfiguredFromAddress(t *testing.T) {
	h := newIMAPHarness(t)
	d := h.driver()
	d.fromAddress = "Kai <kai@example.org>"

	if _, err := d.SaveDraftReply(context.Background(), mail.DraftReply{To: []string{"alice@example.com"}, Subject: "hi", BodyText: "x"}); err != nil {
		t.Fatalf("SaveDraftReply: %v", err)
	}
	_, hdr := onlyDraft(t, h)
	from, err := hdr.AddressList("From")
	if err != nil || len(from) != 1 || from[0].Address != "kai@example.org" || from[0].Name != "Kai" {
		t.Errorf("From = %q (%v), want Kai <kai@example.org>", hdr.Get("From"), err)
	}
}

func TestSaveDraftReply_NoSenderIsAnError(t *testing.T) {
	h := newIMAPHarness(t)
	d := h.driver()
	d.username = "apple-id-without-at"

	_, err := d.SaveDraftReply(context.Background(), mail.DraftReply{To: []string{"alice@example.com"}, Subject: "hi", BodyText: "x"})
	if err == nil || !strings.Contains(err.Error(), "mail.icloud.from_address") {
		t.Fatalf("SaveDraftReply = %v, want an error naming mail.icloud.from_address", err)
	}
	if drafts := h.drafts(); len(drafts) != 0 {
		t.Errorf("Drafts holds %d messages, want 0", len(drafts))
	}
}
