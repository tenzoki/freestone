package icloud

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/kai/freestone/internal/mail"
)

// The iCloud IMAP wire harness: an in-process go-imap v2 server
// (imapserver + imapmemserver) on 127.0.0.1, plain TCP, one user with INBOX
// and Drafts. Tests seed raw RFC 5322 messages, run the real driver against
// the server, and read back what the mailbox holds afterwards (flags,
// appended drafts) over a separate inspection connection that the stall
// hook never touches.

const (
	harnessUser     = "user@example.com"
	harnessPassword = "app-specific-password"
	// harnessInspector is the login the harness's own inspection connection
	// uses. It reaches the same mailboxes as harnessUser but bypasses stalls,
	// so a test can inspect the server while a driver command is blocked.
	harnessInspector = "harness-inspector"
	harnessDrafts    = "Drafts"
)

// Server operations a test can stall on, one per session method the driver
// reaches.
const (
	opLogin  = "LOGIN"
	opList   = "LIST"
	opStatus = "STATUS"
	opSelect = "SELECT"
	opSearch = "SEARCH"
	opFetch  = "FETCH"
	opStore  = "STORE"
	opAppend = "APPEND"
)

// storedMessage is one message as the server holds it.
type storedMessage struct {
	UID   imap.UID
	Flags []imap.Flag
	Raw   []byte // the full RFC 5322 message, fetched with BODY.PEEK[]
}

func (m storedMessage) hasFlag(f imap.Flag) bool { return slices.Contains(m.Flags, f) }

// stallGate blocks every command of one kind until released.
type stallGate struct {
	entered     chan struct{} // closed when the first command blocks
	enteredOnce sync.Once
	released    chan struct{}
	releaseOnce sync.Once
}

func (g *stallGate) release() { g.releaseOnce.Do(func() { close(g.released) }) }

type imapHarness struct {
	t    *testing.T
	addr string
	user *imapmemserver.User

	mu     sync.Mutex
	stalls map[string]*stallGate
	done   chan struct{} // closed at cleanup; unblocks every stalled command
}

// newIMAPHarness starts the server; it is shut down when the test ends.
func newIMAPHarness(t *testing.T) *imapHarness {
	t.Helper()

	user := imapmemserver.NewUser(harnessUser, harnessPassword)
	for _, name := range []string{"INBOX", harnessDrafts} {
		if err := user.Create(name, nil); err != nil {
			t.Fatalf("harness: create mailbox %s: %v", name, err)
		}
	}
	mem := imapmemserver.New()
	mem.AddUser(user)

	h := &imapHarness{t: t, user: user, stalls: map[string]*stallGate{}, done: make(chan struct{})}
	logger := &harnessLogger{t: t}

	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			inner, ok := mem.NewSession().(imapserver.SessionIMAP4rev2)
			if !ok {
				return nil, nil, &imap.Error{Type: imap.StatusResponseTypeNo, Text: "harness: memserver session lacks IMAP4rev2"}
			}
			return &harnessSession{SessionIMAP4rev2: inner, h: h}, nil, nil
		},
		// IMAP4rev1 plus the extensions the driver relies on, so the
		// driver's LIST ... RETURN (STATUS) and APPENDUID paths are exercised.
		Caps: imap.CapSet{
			imap.CapIMAP4rev1:    {},
			imap.CapUIDPlus:      {},
			imap.CapListExtended: {},
			imap.CapListStatus:   {},
		},
		Logger:       logger,
		InsecureAuth: true, // plain TCP on loopback
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("harness: listen: %v", err)
	}
	h.addr = ln.Addr().String()

	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()

	// Cleanup runs last-registered first: unblock stalls, then stop the
	// server, then silence the logger so late connection goroutines cannot
	// log into a finished test.
	t.Cleanup(logger.stop)
	t.Cleanup(func() {
		if err := srv.Close(); err != nil {
			t.Errorf("harness: close server: %v", err)
		}
		if err := <-served; err != nil {
			t.Errorf("harness: serve: %v", err)
		}
	})
	t.Cleanup(func() { close(h.done) })
	return h
}

// driver returns a Driver wired to the harness over plain TCP.
func (h *imapHarness) driver() *Driver {
	addr := h.addr
	return &Driver{
		addr:          addr,
		username:      harnessUser,
		password:      harnessPassword,
		draftsMailbox: harnessDrafts,
		dial: func(ctx context.Context) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", addr)
		},
	}
}

// seed stores a raw RFC 5322 message in mailbox with the given flags and
// returns its UID. It talks to the in-memory store directly, so it never
// passes through the stall hook.
func (h *imapHarness) seed(mailbox, raw string, flags ...imap.Flag) imap.UID {
	h.t.Helper()
	data, err := h.user.Append(mailbox, bytes.NewReader([]byte(raw)), &imap.AppendOptions{Flags: flags})
	if err != nil {
		h.t.Fatalf("harness: seed %s: %v", mailbox, err)
	}
	return data.UID
}

// messages returns every message in mailbox, in UID order, without
// changing any flag (EXAMINE + BODY.PEEK[]).
func (h *imapHarness) messages(mailbox string) []storedMessage {
	h.t.Helper()
	c := h.inspect()
	defer func() {
		if err := c.Logout().Wait(); err != nil {
			h.t.Errorf("harness: inspector logout: %v", err)
		}
	}()

	sel, err := c.Select(mailbox, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		h.t.Fatalf("harness: examine %s: %v", mailbox, err)
	}
	if sel.NumMessages == 0 {
		return nil
	}

	var all imap.SeqSet
	all.AddRange(1, 0) // 1:*
	section := &imap.FetchItemBodySection{Peek: true}
	bufs, err := c.Fetch(all, &imap.FetchOptions{
		UID:         true,
		Flags:       true,
		BodySection: []*imap.FetchItemBodySection{section},
	}).Collect()
	if err != nil {
		h.t.Fatalf("harness: fetch %s: %v", mailbox, err)
	}

	out := make([]storedMessage, 0, len(bufs))
	for _, b := range bufs {
		out = append(out, storedMessage{UID: b.UID, Flags: b.Flags, Raw: b.FindBodySection(section)})
	}
	slices.SortFunc(out, func(a, b storedMessage) int { return int(a.UID) - int(b.UID) })
	return out
}

// message returns the message with uid in mailbox, failing the test if it
// does not exist.
func (h *imapHarness) message(mailbox string, uid imap.UID) storedMessage {
	h.t.Helper()
	for _, m := range h.messages(mailbox) {
		if m.UID == uid {
			return m
		}
	}
	h.t.Fatalf("harness: no message UID %d in %s", uid, mailbox)
	return storedMessage{}
}

// drafts returns every message in the Drafts mailbox.
func (h *imapHarness) drafts() []storedMessage { return h.messages(harnessDrafts) }

// stall makes every driver command of kind op (opFetch, opSelect, ...)
// block inside the server before it is processed. entered is closed when the
// first such command blocks; release lets all of them, present and future,
// proceed. Stalled commands are released anyway when the test ends.
func (h *imapHarness) stall(op string) (entered <-chan struct{}, release func()) {
	g := &stallGate{entered: make(chan struct{}), released: make(chan struct{})}
	h.mu.Lock()
	h.stalls[op] = g
	h.mu.Unlock()
	return g.entered, g.release
}

// gate blocks the calling server goroutine while op is stalled.
func (h *imapHarness) gate(op string) {
	h.mu.Lock()
	g := h.stalls[op]
	h.mu.Unlock()
	if g == nil {
		return
	}
	g.enteredOnce.Do(func() { close(g.entered) })
	select {
	case <-g.released:
	case <-h.done:
	}
}

// inspect opens the harness's own logged-in client connection.
func (h *imapHarness) inspect() *imapclient.Client {
	h.t.Helper()
	conn, err := net.Dial("tcp", h.addr)
	if err != nil {
		h.t.Fatalf("harness: inspector dial: %v", err)
	}
	c := imapclient.New(conn, nil)
	if err := c.Login(harnessInspector, harnessPassword).Wait(); err != nil {
		c.Close()
		h.t.Fatalf("harness: inspector login: %v", err)
	}
	return c
}

// harnessSession wraps a memserver session, routing each command the driver
// sends through the stall hook. The inspector's session is never stalled.
type harnessSession struct {
	imapserver.SessionIMAP4rev2
	h         *imapHarness
	inspector bool
}

func (s *harnessSession) gate(op string) {
	if !s.inspector {
		s.h.gate(op)
	}
}

func (s *harnessSession) Login(username, password string) error {
	if username == harnessInspector {
		s.inspector = true
		username = harnessUser
	} else {
		s.gate(opLogin)
	}
	return s.SessionIMAP4rev2.Login(username, password)
}

func (s *harnessSession) List(w *imapserver.ListWriter, ref string, patterns []string, options *imap.ListOptions) error {
	s.gate(opList)
	return s.SessionIMAP4rev2.List(w, ref, patterns, options)
}

func (s *harnessSession) Status(mailbox string, options *imap.StatusOptions) (*imap.StatusData, error) {
	s.gate(opStatus)
	return s.SessionIMAP4rev2.Status(mailbox, options)
}

func (s *harnessSession) Select(mailbox string, options *imap.SelectOptions) (*imap.SelectData, error) {
	s.gate(opSelect)
	return s.SessionIMAP4rev2.Select(mailbox, options)
}

func (s *harnessSession) Search(kind imapserver.NumKind, criteria *imap.SearchCriteria, options *imap.SearchOptions) (*imap.SearchData, error) {
	s.gate(opSearch)
	return s.SessionIMAP4rev2.Search(kind, criteria, options)
}

func (s *harnessSession) Fetch(w *imapserver.FetchWriter, numSet imap.NumSet, options *imap.FetchOptions) error {
	s.gate(opFetch)
	return s.SessionIMAP4rev2.Fetch(w, numSet, options)
}

func (s *harnessSession) Store(w *imapserver.FetchWriter, numSet imap.NumSet, flags *imap.StoreFlags, options *imap.StoreOptions) error {
	s.gate(opStore)
	return s.SessionIMAP4rev2.Store(w, numSet, flags, options)
}

func (s *harnessSession) Append(mailbox string, r imap.LiteralReader, options *imap.AppendOptions) (*imap.AppendData, error) {
	s.gate(opAppend)
	return s.SessionIMAP4rev2.Append(mailbox, r, options)
}

// harnessLogger forwards server log lines to the test until it is stopped.
type harnessLogger struct {
	t       *testing.T
	mu      sync.Mutex
	stopped bool
}

func (l *harnessLogger) Printf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.stopped {
		l.t.Logf("imapserver: "+format, args...)
	}
}

func (l *harnessLogger) stop() {
	l.mu.Lock()
	l.stopped = true
	l.mu.Unlock()
}

// --- smoke tests ---

const smokeMessage = "From: Alice <alice@example.com>\r\n" +
	"To: user@example.com\r\n" +
	"Subject: Hello harness\r\n" +
	"Date: Thu, 26 Nov 2026 09:00:00 +0000\r\n" +
	"Message-ID: <hello-1@example.com>\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"Body text.\r\n"

func TestIMAPHarness_ListMailboxes(t *testing.T) {
	h := newIMAPHarness(t)
	h.seed("INBOX", smokeMessage)
	h.seed("INBOX", smokeMessage, imap.FlagSeen)

	mboxes, err := h.driver().ListMailboxes(context.Background())
	if err != nil {
		t.Fatalf("ListMailboxes: %v", err)
	}
	byName := map[string]mail.Mailbox{}
	for _, mb := range mboxes {
		byName[mb.Name] = mb
	}
	inbox, ok := byName["INBOX"]
	if !ok {
		t.Fatalf("ListMailboxes = %+v, want INBOX", mboxes)
	}
	if inbox.Messages != 2 || inbox.Unseen != 1 {
		t.Errorf("INBOX messages/unseen = %d/%d, want 2/1", inbox.Messages, inbox.Unseen)
	}
	if _, ok := byName[harnessDrafts]; !ok {
		t.Errorf("ListMailboxes = %+v, want %s", mboxes, harnessDrafts)
	}
}

func TestIMAPHarness_ListMessages(t *testing.T) {
	h := newIMAPHarness(t)
	uid := h.seed("INBOX", smokeMessage)

	msgs, err := h.driver().ListMessages(context.Background(), mail.MessageQuery{MailboxID: "INBOX"})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("ListMessages = %+v, want 1 message", msgs)
	}
	got := msgs[0]
	if got.Subject != "Hello harness" || got.From != "Alice <alice@example.com>" {
		t.Errorf("summary = %+v, want subject %q from Alice", got, "Hello harness")
	}

	// Listing fetches no body, so it must leave \Seen unset.
	if m := h.message("INBOX", uid); m.hasFlag(imap.FlagSeen) {
		t.Errorf("flags after ListMessages = %v, want no \\Seen", m.Flags)
	}
}

func TestIMAPHarness_Stall(t *testing.T) {
	h := newIMAPHarness(t)
	h.seed("INBOX", smokeMessage)
	entered, release := h.stall(opSearch)

	errc := make(chan error, 1)
	go func() {
		_, err := h.driver().ListMessages(context.Background(), mail.MessageQuery{MailboxID: "INBOX"})
		errc <- err
	}()

	select {
	case <-entered:
	case err := <-errc:
		t.Fatalf("ListMessages returned before reaching the stalled SEARCH: %v", err)
	}
	// The inspector is not stalled while the driver is.
	if n := len(h.messages("INBOX")); n != 1 {
		t.Errorf("inspector saw %d messages during stall, want 1", n)
	}
	release()
	if err := <-errc; err != nil {
		t.Fatalf("ListMessages after release: %v", err)
	}
}

// --- D4: charsets and encoded words ---

// germanMessage is a German mail as Outlook and older clients send it: an
// ISO-8859-1 quoted-printable body and RFC 2047 encoded words in the sender
// name and subject. The subject is Windows-1252 on purpose: Go's
// mime.WordDecoder handles ISO-8859-1 words by itself, so only a charset
// beyond it shows whether the client's word decoder knows charsets.
const germanMessage = "From: =?iso-8859-1?q?J=FCrgen?= <juergen@example.com>\r\n" +
	"To: user@example.com\r\n" +
	"Subject: =?windows-1252?q?Gr=FC=DFe_=80?=\r\n" +
	"Date: Thu, 26 Nov 2026 09:00:00 +0000\r\n" +
	"Message-ID: <german-1@example.com>\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: text/plain; charset=ISO-8859-1\r\n" +
	"Content-Transfer-Encoding: quoted-printable\r\n" +
	"\r\n" +
	"Viele Gr=FC=DFe aus M=FCnchen\r\n"

func TestIMAPHarness_GetMessage_DecodesCharsets(t *testing.T) {
	h := newIMAPHarness(t)
	uid := h.seed("INBOX", germanMessage)

	msg, err := h.driver().GetMessage(context.Background(), "INBOX", strconv.FormatUint(uint64(uid), 10))
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if want := "Viele Grüße aus München\r\n"; msg.Text != want {
		t.Errorf("Text = %q, want %q", msg.Text, want)
	}
	if msg.Subject != "Grüße €" {
		t.Errorf("Subject = %q, want %q", msg.Subject, "Grüße €")
	}
	if msg.From != "Jürgen <juergen@example.com>" {
		t.Errorf("From = %q, want %q", msg.From, "Jürgen <juergen@example.com>")
	}
	if got := msg.Headers["Message-ID"]; got != "<german-1@example.com>" {
		t.Errorf("Headers[Message-ID] = %q, want <german-1@example.com>", got)
	}
	if len(msg.Warnings) != 0 {
		t.Errorf("Warnings = %v, want none", msg.Warnings)
	}
}

func TestIMAPHarness_ListMessages_DecodesEncodedWordSubject(t *testing.T) {
	h := newIMAPHarness(t)
	h.seed("INBOX", germanMessage)

	msgs, err := h.driver().ListMessages(context.Background(), mail.MessageQuery{MailboxID: "INBOX"})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("ListMessages = %+v, want 1 message", msgs)
	}
	if msgs[0].Subject != "Grüße €" {
		t.Errorf("Subject = %q, want %q", msgs[0].Subject, "Grüße €")
	}
}

func TestIMAPHarness_GetMessage_UnknownCharsetWarns(t *testing.T) {
	h := newIMAPHarness(t)
	raw := strings.Replace(smokeMessage, "charset=utf-8", "charset=x-unknown", 1)
	uid := h.seed("INBOX", raw)

	msg, err := h.driver().GetMessage(context.Background(), "INBOX", strconv.FormatUint(uint64(uid), 10))
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if !strings.Contains(msg.Text, "Body text.") {
		t.Errorf("Text = %q, want the undecoded body", msg.Text)
	}
	if len(msg.Warnings) != 1 || !strings.Contains(msg.Warnings[0], "x-unknown") {
		t.Errorf("Warnings = %v, want one naming x-unknown", msg.Warnings)
	}
	// warnings is part of the mail_get_message output.
	out, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(out), `"warnings":[`) {
		t.Errorf("JSON = %s, want a warnings field", out)
	}
}

func TestIMAPHarness_GetMessage_UnparseableIsError(t *testing.T) {
	h := newIMAPHarness(t)
	uid := h.seed("INBOX", "Subject: broken\r\n"+
		"Content-Type: multipart/mixed\r\n"+ // no boundary
		"\r\n"+
		"--x\r\nContent-Type: text/plain\r\n\r\nhi\r\n--x--\r\n")

	msg, err := h.driver().GetMessage(context.Background(), "INBOX", strconv.FormatUint(uint64(uid), 10))
	if err == nil {
		t.Fatalf("GetMessage = %+v, nil error; want a decode error", msg)
	}
}
