package rfc5322

import (
	"bytes"
	"strings"
	"testing"

	"github.com/emersion/go-message/mail"
)

// readDraft parses composed bytes back with go-message, so assertions are
// about what a mail client reads, not about the exact folding on the wire.
func readDraft(t *testing.T, raw []byte) (*mail.Header, *mail.Reader) {
	t.Helper()
	mr, err := mail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parse composed draft: %v\n%s", err, raw)
	}
	return &mr.Header, mr
}

func compose(t *testing.T, d Draft) []byte {
	t.Helper()
	raw, err := ComposeDraft(d)
	if err != nil {
		t.Fatalf("ComposeDraft: %v", err)
	}
	return raw
}

func TestComposeDraft_EncodedWordSubject(t *testing.T) {
	raw := compose(t, Draft{To: []string{"a@example.com"}, Subject: "Re: Grüße aus München", Text: "hi"})

	if bytes.Contains(raw, []byte("Grüße")) {
		t.Errorf("subject written as raw UTF-8, want RFC 2047 encoded words:\n%s", raw)
	}
	if !bytes.Contains(raw, []byte("=?utf-8?")) {
		t.Errorf("no encoded word in draft:\n%s", raw)
	}
	h, _ := readDraft(t, raw)
	got, err := h.Subject()
	if err != nil || got != "Re: Grüße aus München" {
		t.Errorf("Subject = %q, %v; want the umlauts back", got, err)
	}
}

func TestComposeDraft_RejectsLineBreaks(t *testing.T) {
	cases := map[string]Draft{
		"CRLF in To":      {To: []string{"a@b.de\r\nBcc: x@y.de"}},
		"LF in Cc":        {To: []string{"a@b.de"}, Cc: []string{"c@b.de\nBcc: x@y.de"}},
		"CR in Bcc":       {To: []string{"a@b.de"}, Bcc: []string{"c@b.de\rX: y"}},
		"CRLF in From":    {From: "me@b.de\r\nBcc: x@y.de", To: []string{"a@b.de"}},
		"CRLF in Subject": {To: []string{"a@b.de"}, Subject: "hi\r\nBcc: x@y.de"},
		"LF in Subject":   {To: []string{"a@b.de"}, Subject: "hi\nBcc: x@y.de"},
	}
	for name, d := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := ComposeDraft(d)
			if err == nil {
				t.Fatalf("ComposeDraft = nil error, want rejection; draft:\n%s", raw)
			}
		})
	}
}

func TestComposeDraft_RejectsInvalidAddress(t *testing.T) {
	for _, addr := range []string{"not an address", "a@b.de, c@d.de", "", "<@>"} {
		if raw, err := ComposeDraft(Draft{To: []string{addr}, Text: "x"}); err == nil {
			t.Errorf("To %q: ComposeDraft = nil error, want rejection; draft:\n%s", addr, raw)
		}
	}
}

func TestComposeDraft_AddressesWritten(t *testing.T) {
	raw := compose(t, Draft{
		From: "Kai Müller <kai@example.com>",
		To:   []string{"Alice <alice@example.com>", "bob@example.com"},
		Cc:   []string{"carol@example.com"},
		Bcc:  []string{"dave@example.com"},
		Text: "x",
	})
	h, _ := readDraft(t, raw)
	for key, want := range map[string][]string{
		"From": {"kai@example.com"},
		"To":   {"alice@example.com", "bob@example.com"},
		"Cc":   {"carol@example.com"},
		"Bcc":  {"dave@example.com"},
	} {
		list, err := h.AddressList(key)
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		var got []string
		for _, a := range list {
			got = append(got, a.Address)
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s = %v, want %v", key, got, want)
		}
	}
	if from, _ := h.AddressList("From"); len(from) != 1 || from[0].Name != "Kai Müller" {
		t.Errorf("From = %v, want display name Kai Müller", from)
	}
}

func TestComposeDraft_NoFromWhenEmpty(t *testing.T) {
	h, _ := readDraft(t, compose(t, Draft{To: []string{"a@example.com"}, Text: "x"}))
	if h.Has("From") {
		t.Errorf("From = %q, want no From field when Draft.From is empty", h.Get("From"))
	}
	if h.Get("Date") == "" {
		t.Error("no Date field")
	}
}

func TestComposeDraft_ReferencesChain(t *testing.T) {
	raw := compose(t, Draft{
		To:               []string{"a@example.com"},
		Text:             "x",
		ParentMessageID:  "2@x",
		ParentReferences: []string{"1@x"},
	})
	h, _ := readDraft(t, raw)
	if got := h.Get("References"); got != "<1@x> <2@x>" {
		t.Errorf("References = %q, want %q", got, "<1@x> <2@x>")
	}
	if got := h.Get("In-Reply-To"); got != "<2@x>" {
		t.Errorf("In-Reply-To = %q, want <2@x>", got)
	}
}

func TestComposeDraft_NotAReplyHasNoThreading(t *testing.T) {
	h, _ := readDraft(t, compose(t, Draft{To: []string{"a@example.com"}, Text: "x"}))
	if h.Has("In-Reply-To") || h.Has("References") {
		t.Errorf("threading fields on a non-reply: In-Reply-To %q References %q", h.Get("In-Reply-To"), h.Get("References"))
	}
}

func TestComposeDraft_RejectsBadThreading(t *testing.T) {
	for name, d := range map[string]Draft{
		"references without parent": {ParentReferences: []string{"1@x"}},
		"line break in parent id":   {ParentMessageID: "2@x>\r\nBcc: <x@y"},
		"bracketed parent id":       {ParentMessageID: "<2@x>"},
		"space in reference":        {ParentMessageID: "2@x", ParentReferences: []string{"1@x 3@x"}},
	} {
		t.Run(name, func(t *testing.T) {
			if raw, err := ComposeDraft(d); err == nil {
				t.Fatalf("ComposeDraft = nil error, want rejection; draft:\n%s", raw)
			}
		})
	}
}

func TestComposeDraft_BodyKinds(t *testing.T) {
	cases := []struct {
		name       string
		text, html string
		wantType   string
		wantText   string
		wantHTML   string
	}{
		{"text only", "Hallo\nWelt", "", "text/plain", "Hallo\r\nWelt", ""},
		{"html only", "", "<p>Grüße</p>", "text/html", "", "<p>Grüße</p>"},
		{"both", "Hallo", "<p>Hallo</p>", "multipart/alternative", "Hallo", "<p>Hallo</p>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := compose(t, Draft{To: []string{"a@example.com"}, Text: tc.text, HTML: tc.html})
			h, _ := readDraft(t, raw)
			mediaType, _, err := h.ContentType()
			if err != nil || mediaType != tc.wantType {
				t.Fatalf("Content-Type = %q, %v; want %s\n%s", mediaType, err, tc.wantType, raw)
			}
			body, err := Decode(bytes.NewReader(raw))
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if body.Text != tc.wantText || body.HTML != tc.wantHTML {
				t.Errorf("Text/HTML = %q/%q, want %q/%q", body.Text, body.HTML, tc.wantText, tc.wantHTML)
			}
		})
	}
}

func TestComposeDraft_CRLFLineEndings(t *testing.T) {
	raw := compose(t, Draft{
		To:   []string{"a@example.com"},
		Text: "line one\nline two\r\nline three\n",
		HTML: "<p>one</p>\n<p>two</p>",
	})
	for i, b := range raw {
		if b == '\n' && (i == 0 || raw[i-1] != '\r') {
			t.Fatalf("bare LF at byte %d:\n%q", i, raw)
		}
	}
}

func TestParseMsgIDList(t *testing.T) {
	ids, err := ParseMsgIDList("<1@x>  <2@x>\r\n <3@x>")
	if err != nil || strings.Join(ids, ",") != "1@x,2@x,3@x" {
		t.Errorf("ParseMsgIDList = %v, %v", ids, err)
	}
	if ids, err := ParseMsgIDList(""); err != nil || len(ids) != 0 {
		t.Errorf("empty: %v, %v", ids, err)
	}
	if _, err := ParseMsgIDList("garbage"); err == nil {
		t.Error("garbage: want an error")
	}
}

func TestHeaderReferences(t *testing.T) {
	ids, err := HeaderReferences([]byte("References: <1@x>\r\n <2@x>\r\n\r\n"))
	if err != nil || strings.Join(ids, ",") != "1@x,2@x" {
		t.Errorf("HeaderReferences = %v, %v", ids, err)
	}
	if ids, err := HeaderReferences([]byte("\r\n")); err != nil || len(ids) != 0 {
		t.Errorf("no References field: %v, %v", ids, err)
	}
}
