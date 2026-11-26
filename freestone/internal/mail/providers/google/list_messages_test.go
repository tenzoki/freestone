package google

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/api/gmail/v1"

	domain "github.com/kai/freestone/internal/mail"
)

// seedListing registers a list answer naming n messages m-0 … m-(n-1), in
// that order, and a metadata answer for each whose subject names its index.
func seedListing(h *gmailAPIHarness, n int) {
	list := &gmail.ListMessagesResponse{}
	for i := range n {
		id := fmt.Sprintf("m-%d", i)
		list.Messages = append(list.Messages, &gmail.Message{Id: id})
		h.respond(http.MethodGet, gmailMessagePath(id), http.StatusOK, &gmail.Message{
			Id: id,
			Payload: &gmail.MessagePart{Headers: []*gmail.MessagePartHeader{
				{Name: "Subject", Value: "subject " + id},
			}},
		})
	}
	h.respond(http.MethodGet, gmailMessagesPath, http.StatusOK, list)
}

func isMetadataGet(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, gmailMessagesPath+"/")
}

// inFlightGauge counts metadata requests the harness is holding open and
// remembers the highest count seen.
type inFlightGauge struct {
	mu   sync.Mutex
	cond *sync.Cond
	now  int
	peak int
}

func newInFlightGauge() *inFlightGauge {
	g := &inFlightGauge{}
	g.cond = sync.NewCond(&g.mu)
	return g
}

// enter counts the request in and returns the count including it.
func (g *inFlightGauge) enter() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.now++
	g.peak = max(g.peak, g.now)
	g.cond.Broadcast()
	return g.now
}

func (g *inFlightGauge) leave() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.now--
	g.cond.Broadcast()
}

// waitFor blocks until at least want requests are in flight or guard has
// passed, and reports whether the count was reached.
func (g *inFlightGauge) waitFor(want int, guard time.Duration) bool {
	timer := time.AfterFunc(guard, func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.cond.Broadcast()
	})
	defer timer.Stop()
	deadline := time.Now().Add(guard)

	g.mu.Lock()
	defer g.mu.Unlock()
	for g.now < want {
		if !time.Now().Before(deadline) {
			return false
		}
		g.cond.Wait()
	}
	return true
}

func (g *inFlightGauge) peakSeen() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.peak
}

func listAll(t *testing.T, h *gmailAPIHarness) []domain.MessageSummary {
	t.Helper()
	msgs, err := h.driver().ListMessages(context.Background(), domain.MessageQuery{Limit: 200})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	return msgs
}

func requireListOrder(t *testing.T, msgs []domain.MessageSummary, n int) {
	t.Helper()
	if len(msgs) != n {
		t.Fatalf("ListMessages returned %d summaries, want %d", len(msgs), n)
	}
	for i, m := range msgs {
		want := fmt.Sprintf("m-%d", i)
		if m.ID != want || m.Subject != "subject "+want {
			t.Errorf("summary %d = %s %q, want %s in list order", i, m.ID, m.Subject, want)
		}
	}
}

// The metadata gets overlap: the first get is held until a second one is in
// flight. A driver that issues them one after another never gets there.
func TestGmailHarness_ListMessages_MetadataInParallel(t *testing.T) {
	const n = 12
	h := newGmailAPIHarness(t)
	seedListing(h, n)

	gauge := newInFlightGauge()
	var gaveUp atomic.Bool
	h.beforeRespond = func(r *http.Request) {
		if !isMetadataGet(r) {
			return
		}
		gauge.enter()
		defer gauge.leave()
		// Hold until two have overlapped once; the last get of a listing
		// may rightly be alone. After a miss, stop holding, so a sequential
		// driver fails in one guard period rather than n.
		if gauge.peakSeen() < 2 && !gaveUp.Load() && !gauge.waitFor(2, time.Second) {
			gaveUp.Store(true)
		}
	}

	msgs := listAll(t, h)
	if peak := gauge.peakSeen(); peak < 2 {
		t.Errorf("peak in-flight metadata gets = %d after a 1 s hold, want at least 2: gets are sequential", peak)
	}
	requireListOrder(t, msgs, n)
}

// No more than metadataWorkers gets are in flight at once. Every get is
// held until a ninth one joins it or 100 ms pass; a driver without the
// bound gets there at once.
func TestGmailHarness_ListMessages_AtMostEightInFlight(t *testing.T) {
	const n = 30
	h := newGmailAPIHarness(t)
	seedListing(h, n)

	gauge := newInFlightGauge()
	h.beforeRespond = func(r *http.Request) {
		if !isMetadataGet(r) {
			return
		}
		gauge.enter()
		defer gauge.leave()
		gauge.waitFor(9, 100*time.Millisecond)
	}

	msgs := listAll(t, h)
	if peak := gauge.peakSeen(); peak > 8 {
		t.Errorf("peak in-flight metadata gets = %d, want at most 8", peak)
	} else if peak < 8 {
		t.Errorf("peak in-flight metadata gets = %d, want the pool filled to 8", peak)
	}
	requireListOrder(t, msgs, n)
}

// Answers arrive in reverse list order; the listing keeps list order.
func TestGmailHarness_ListMessages_KeepsListOrderUnderConcurrentAnswers(t *testing.T) {
	const n = 8
	h := newGmailAPIHarness(t)
	seedListing(h, n)

	h.beforeRespond = func(r *http.Request) {
		if !isMetadataGet(r) {
			return
		}
		var i int
		if _, err := fmt.Sscanf(r.URL.Path, gmailMessagesPath+"/m-%d", &i); err != nil {
			t.Errorf("harness: unexpected metadata path %s: %v", r.URL.Path, err)
			return
		}
		// m-0 answers last, m-(n-1) first.
		time.Sleep(time.Duration(n-i) * 15 * time.Millisecond)
	}

	requireListOrder(t, listAll(t, h), n)
}

// One failing get fails the listing with that get's error, and cancels the
// gets still running instead of letting them finish.
func TestGmailHarness_ListMessages_OneFailureFailsTheList(t *testing.T) {
	const n = 6
	h := newGmailAPIHarness(t)
	seedListing(h, n)
	h.respond(http.MethodGet, gmailMessagePath("m-0"), http.StatusNotFound,
		`{"error":{"code":404,"message":"Requested entity was not found."}}`)

	var notCancelled atomic.Int32
	h.beforeRespond = func(r *http.Request) {
		if !isMetadataGet(r) || r.URL.Path == gmailMessagePath("m-0") {
			return
		}
		// Hold every other get until the driver abandons it.
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
			notCancelled.Add(1)
		}
	}

	msgs, err := h.driver().ListMessages(context.Background(), domain.MessageQuery{Limit: 200})
	if err == nil {
		t.Fatalf("ListMessages = %d summaries, nil error; want the m-0 failure", len(msgs))
	}
	if msgs != nil {
		t.Errorf("ListMessages returned %d summaries with the error, want none", len(msgs))
	}
	if !strings.Contains(err.Error(), "m-0") || !strings.Contains(err.Error(), "404") {
		t.Errorf("error = %v, want the m-0 404", err)
	}
	if c := notCancelled.Load(); c > 0 {
		t.Errorf("%d gets ran to their 5 s guard after the failure, want them cancelled", c)
	}
}

// A context that has already ended dispatches nothing and says why.
func TestGmailHarness_ListMessages_EndedContextIsAnError(t *testing.T) {
	h := newGmailAPIHarness(t)
	seedListing(h, 3)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := h.driver().ListMessages(ctx, domain.MessageQuery{Limit: 200})
	if err == nil {
		t.Fatal("ListMessages with a cancelled context returned nil error")
	}
}
