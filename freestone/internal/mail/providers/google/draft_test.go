package google

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	gomail "github.com/emersion/go-message/mail"
	"google.golang.org/api/gmail/v1"

	domain "github.com/kai/freestone/internal/mail"
)

// --- D7: threaded, encoded, injection-safe Gmail drafts ---

// sentDraft decodes the draft the driver posted: its threadId and the
// parsed header of its raw RFC 5322 message.
func sentDraft(t *testing.T, h *gmailAPIHarness) (threadID string, hdr *gomail.Header, raw []byte) {
	t.Helper()
	var post *apiRequest
	for _, r := range h.recorded() {
		if r.Method == http.MethodPost && r.Path == gmailDraftsPath {
			if post != nil {
				t.Fatal("more than one draft posted")
			}
			r := r
			post = &r
		}
	}
	if post == nil {
		t.Fatal("no draft posted")
	}
	msg, ok := post.JSON(t)["message"].(map[string]any)
	if !ok {
		t.Fatalf("draft body has no message: %s", post.Body)
	}
	threadID, _ = msg["threadId"].(string)
	enc, _ := msg["raw"].(string)
	raw, err := base64.URLEncoding.DecodeString(enc)
	if err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	mr, err := gomail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parse raw draft: %v\n%s", err, raw)
	}
	return threadID, &mr.Header, raw
}

func respondDraftCreated(h *gmailAPIHarness) {
	h.respond(http.MethodPost, gmailDraftsPath, http.StatusOK, &gmail.Draft{Id: "d-1", Message: &gmail.Message{Id: "dm-1"}})
}

func respondParent(h *gmailAPIHarness, headers ...*gmail.MessagePartHeader) {
	h.respond(http.MethodGet, gmailMessagePath("m-2"), http.StatusOK, &gmail.Message{
		Id:       "m-2",
		ThreadId: "t-1",
		Payload:  &gmail.MessagePart{Headers: headers},
	})
}

func TestGmailHarness_SaveDraftReply_Threaded(t *testing.T) {
	h := newGmailAPIHarness(t)
	respondParent(h,
		&gmail.MessagePartHeader{Name: "Message-Id", Value: "<2@x>"},
		&gmail.MessagePartHeader{Name: "References", Value: "<1@x>"},
	)
	respondDraftCreated(h)

	id, err := h.driver().SaveDraftReply(context.Background(), domain.DraftReply{
		InReplyToID: "m-2",
		To:          []string{"alice@example.com"},
		Subject:     "Re: Grüße",
		BodyText:    "Danke.",
	})
	if err != nil {
		t.Fatalf("SaveDraftReply: %v", err)
	}
	if id != "dm-1" {
		t.Errorf("draft id = %q, want dm-1", id)
	}

	get := h.recorded()[0]
	if got := strings.Join(get.Query["metadataHeaders"], ","); !strings.Contains(got, "References") || !strings.Contains(got, "Message-ID") {
		t.Errorf("parent metadataHeaders = %q, want Message-ID and References", got)
	}

	threadID, hdr, raw := sentDraft(t, h)
	if threadID != "t-1" {
		t.Errorf("threadId = %q, want t-1", threadID)
	}
	if got := hdr.Get("In-Reply-To"); got != "<2@x>" {
		t.Errorf("In-Reply-To = %q, want <2@x>", got)
	}
	if got := hdr.Get("References"); got != "<1@x> <2@x>" {
		t.Errorf("References = %q, want <1@x> <2@x>", got)
	}
	if bytes.Contains(raw, []byte("Grüße")) {
		t.Errorf("subject written as raw UTF-8, want RFC 2047:\n%s", raw)
	}
	if subj, err := hdr.Subject(); err != nil || subj != "Re: Grüße" {
		t.Errorf("Subject = %q (%v), want Re: Grüße", subj, err)
	}
}

func TestGmailHarness_SaveDraftReply_HTMLOnlyIsTextHTML(t *testing.T) {
	h := newGmailAPIHarness(t)
	respondDraftCreated(h)

	if _, err := h.driver().SaveDraftReply(context.Background(), domain.DraftReply{
		To: []string{"alice@example.com"}, Subject: "hi", BodyHTML: "<p>hi</p>",
	}); err != nil {
		t.Fatalf("SaveDraftReply: %v", err)
	}
	_, hdr, _ := sentDraft(t, h)
	if mediaType, _, err := hdr.ContentType(); err != nil || mediaType != "text/html" {
		t.Errorf("Content-Type = %q (%v), want text/html", hdr.Get("Content-Type"), err)
	}
}

func TestGmailHarness_SaveDraftReply_RejectedInputPostsNothing(t *testing.T) {
	cases := map[string]domain.DraftReply{
		"CRLF in subject":   {To: []string{"alice@example.com"}, Subject: "hi\r\nBcc: evil@example.com"},
		"CRLF in recipient": {To: []string{"alice@example.com\r\nBcc: evil@example.com"}, Subject: "hi"},
		"invalid recipient": {Cc: []string{"not an address"}, Subject: "hi"},
		// respondParent below serves m-2 with no headers at all.
		"parent without Message-ID": {InReplyToID: "m-2", To: []string{"alice@example.com"}, Subject: "Re: hi"},
	}
	for name, draft := range cases {
		t.Run(name, func(t *testing.T) {
			h := newGmailAPIHarness(t)
			respondParent(h)
			respondDraftCreated(h)
			if _, err := h.driver().SaveDraftReply(context.Background(), draft); err == nil {
				t.Error("SaveDraftReply = nil error, want rejection")
			}
			for _, r := range h.recorded() {
				if r.Method == http.MethodPost {
					t.Errorf("draft posted despite rejection: %s %s", r.Method, r.Path)
				}
			}
		})
	}
}
