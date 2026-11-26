package icloud

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

// --- D6: IMAP calls honour the tool context ---

// trackedConn records whether the driver closed its connection, and can fail
// the LOGOUT command on the wire.
type trackedConn struct {
	net.Conn
	closed     atomic.Bool
	failLogout bool
}

func (c *trackedConn) Close() error {
	c.closed.Store(true)
	return c.Conn.Close()
}

func (c *trackedConn) Write(p []byte) (int, error) {
	if c.failLogout && bytes.Contains(p, []byte(" LOGOUT")) {
		return 0, errors.New("injected write failure on LOGOUT")
	}
	return c.Conn.Write(p)
}

// trackedDriver returns a harness driver whose connections are recorded in
// the returned slice as they are dialled.
func trackedDriver(h *imapHarness, failLogout bool) (*Driver, func() []*trackedConn) {
	d := h.driver()
	dial := d.dial
	var mu sync.Mutex
	var conns []*trackedConn
	d.dial = func(ctx context.Context) (net.Conn, error) {
		conn, err := dial(ctx)
		if err != nil {
			return nil, err
		}
		tc := &trackedConn{Conn: conn, failLogout: failLogout}
		mu.Lock()
		conns = append(conns, tc)
		mu.Unlock()
		return tc, nil
	}
	return d, func() []*trackedConn {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(conns)
	}
}

func requireAllClosed(t *testing.T, conns []*trackedConn) {
	t.Helper()
	if len(conns) == 0 {
		t.Fatal("driver dialled no connection")
	}
	for i, c := range conns {
		if !c.closed.Load() {
			t.Errorf("connection %d still open after the call returned", i)
		}
	}
}

// Each driver method, with the server command it blocks on.
var stalledCalls = []struct {
	name string
	op   string
	call func(ctx context.Context, d *Driver, uid string) error
}{
	{"ListMailboxes stalled on LIST", opList, func(ctx context.Context, d *Driver, _ string) error {
		_, err := d.ListMailboxes(ctx)
		return err
	}},
	{"ListMessages stalled on SELECT", opSelect, func(ctx context.Context, d *Driver, _ string) error {
		_, err := d.ListMessages(ctx, mail.MessageQuery{MailboxID: "INBOX"})
		return err
	}},
	{"ListMessages stalled on LOGIN", opLogin, func(ctx context.Context, d *Driver, _ string) error {
		_, err := d.ListMessages(ctx, mail.MessageQuery{MailboxID: "INBOX"})
		return err
	}},
	{"GetMessage stalled on FETCH", opFetch, func(ctx context.Context, d *Driver, uid string) error {
		_, err := d.GetMessage(ctx, "INBOX", uid)
		return err
	}},
	{"SaveDraftReply stalled on APPEND", opAppend, func(ctx context.Context, d *Driver, _ string) error {
		_, err := d.SaveDraftReply(ctx, mail.DraftReply{To: []string{"alice@example.com"}, Subject: "Re: hi", BodyText: "ok"})
		return err
	}},
	{"MarkRead stalled on STORE", opStore, func(ctx context.Context, d *Driver, uid string) error {
		return d.MarkRead(ctx, "INBOX", uid)
	}},
}

// A stalled server must not hold a call past its context: the library alone
// would wait 30 s for the response.
func TestSession_DeadlineWhileStalled(t *testing.T) {
	for _, tc := range stalledCalls {
		t.Run(tc.name, func(t *testing.T) {
			h := newIMAPHarness(t)
			uid := strconv.FormatUint(uint64(h.seed("INBOX", smokeMessage)), 10)
			d, conns := trackedDriver(h, false)
			entered, _ := h.stall(tc.op)

			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			start := time.Now()
			errc := make(chan error, 1)
			go func() { errc <- tc.call(ctx, d, uid) }()

			var err error
			select {
			case err = <-errc:
			case <-time.After(5 * time.Second):
				t.Fatal("call still blocked 5 s after a 200 ms deadline")
			}
			if elapsed := time.Since(start); elapsed > 2*time.Second {
				t.Errorf("call returned after %v, want within 2 s", elapsed)
			}
			select {
			case <-entered:
			default:
				t.Fatalf("call returned before reaching the stalled %s: %v", tc.op, err)
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("err = %v, want one wrapping context.DeadlineExceeded", err)
			}
			requireAllClosed(t, conns())
		})
	}
}

func TestSession_CancelWhileStalled(t *testing.T) {
	h := newIMAPHarness(t)
	h.seed("INBOX", smokeMessage)
	d, conns := trackedDriver(h, false)
	entered, _ := h.stall(opSearch)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() {
		_, err := d.ListMessages(ctx, mail.MessageQuery{MailboxID: "INBOX"})
		errc <- err
	}()
	select {
	case <-entered:
	case err := <-errc:
		t.Fatalf("ListMessages returned before reaching the stalled SEARCH: %v", err)
	}
	cancel()

	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want one wrapping context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ListMessages still blocked 5 s after cancel")
	}
	requireAllClosed(t, conns())
}

func TestSession_SuccessClosesConnection(t *testing.T) {
	h := newIMAPHarness(t)
	uid := strconv.FormatUint(uint64(h.seed("INBOX", smokeMessage)), 10)
	d, conns := trackedDriver(h, false)

	for _, tc := range stalledCalls {
		if err := tc.call(context.Background(), d, uid); err != nil {
			t.Fatalf("%s without a stall: %v", tc.name, err)
		}
	}
	if got := len(conns()); got != len(stalledCalls) {
		t.Fatalf("dialled %d connections, want one per call (%d)", got, len(stalledCalls))
	}
	requireAllClosed(t, conns())
}

// A LOGOUT failure after the draft was appended must not report the save as
// failed (a retrying caller would save a second draft), and must be logged.
func TestSession_LogoutFailureAfterSuccessIsLoggedNotReturned(t *testing.T) {
	h := newIMAPHarness(t)
	d, conns := trackedDriver(h, true)
	var logs bytes.Buffer
	d.log = slog.New(slog.NewTextHandler(&lockedWriter{w: &logs}, nil))

	id, err := d.SaveDraftReply(context.Background(), mail.DraftReply{To: []string{"alice@example.com"}, Subject: "Re: hi", BodyText: "ok"})
	if err != nil {
		t.Fatalf("SaveDraftReply = %v, want success despite the LOGOUT failure", err)
	}
	if id == "" {
		t.Error("SaveDraftReply returned no draft ID")
	}
	if n := len(h.drafts()); n != 1 {
		t.Errorf("Drafts holds %d messages, want 1", n)
	}
	if out := logs.String(); !strings.Contains(out, "logout failed") || !strings.Contains(out, "op_succeeded=true") {
		t.Errorf("log = %q, want the LOGOUT failure recorded", out)
	}
	requireAllClosed(t, conns())
}

// A failed operation returns its own error, not a cleanup error.
func TestSession_FailedOperationReturnsItsOwnError(t *testing.T) {
	h := newIMAPHarness(t)
	d, _ := trackedDriver(h, true)
	d.log = slog.New(slog.DiscardHandler)

	err := d.MarkRead(context.Background(), "INBOX", "not-a-uid")
	if err == nil || !strings.Contains(err.Error(), "invalid message ID") {
		t.Fatalf("MarkRead = %v, want the invalid-ID error", err)
	}
	if strings.Contains(err.Error(), "logout") {
		t.Errorf("err = %v, want no LOGOUT failure joined in", err)
	}
}

type lockedWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
