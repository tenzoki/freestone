package google

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"google.golang.org/api/calendar/v3"
	"google.golang.org/api/option"

	domain "github.com/kai/freestone/internal/calendar"
)

// The Google Calendar wire harness: an httptest server standing in for the
// Calendar v3 API. Tests register a canned answer per method and path and
// read back exactly what the driver sent.

// apiRequest is one HTTP request as the harness server saw it.
type apiRequest struct {
	Method string
	Path   string // decoded, e.g. /calendars/primary/events/evt-1
	Query  url.Values
	Header http.Header
	Body   []byte // raw request body; nil when empty
}

// JSON decodes the request body into a generic map, so a test can check
// which fields were sent at all (a PATCH carries only the set fields).
func (r apiRequest) JSON(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.Body, &m); err != nil {
		t.Fatalf("harness: %s %s body is not a JSON object: %v (%q)", r.Method, r.Path, err, r.Body)
	}
	return m
}

type apiResponse struct {
	status int
	body   []byte
}

// calendarAPIHarness routes Calendar v3 requests to canned answers.
type calendarAPIHarness struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	routes   map[string]apiResponse // key: "METHOD /path"
	requests []apiRequest
}

// newCalendarAPIHarness starts a server with no routes; it is closed when
// the test ends. A request to an unregistered route fails the test.
func newCalendarAPIHarness(t *testing.T) *calendarAPIHarness {
	t.Helper()
	h := &calendarAPIHarness{t: t, routes: map[string]apiResponse{}}
	h.srv = httptest.NewServer(http.HandlerFunc(h.serve))
	t.Cleanup(h.srv.Close)
	return h
}

// driver builds a Driver whose service talks to the harness server.
func (h *calendarAPIHarness) driver() *Driver {
	h.t.Helper()
	svc, err := calendar.NewService(context.Background(),
		option.WithEndpoint(h.srv.URL),
		option.WithHTTPClient(h.srv.Client()),
		option.WithoutAuthentication(),
	)
	if err != nil {
		h.t.Fatalf("harness: calendar.NewService: %v", err)
	}
	return &Driver{svc: svc}
}

// respond registers the answer to method+path (path decoded, as in
// apiRequest.Path). body is sent as is when it is a string, otherwise it
// is JSON-encoded, so a test can pass a *calendar.Event or *calendar.Events.
// Registering the same route again replaces the answer.
func (h *calendarAPIHarness) respond(method, path string, status int, body any) {
	h.t.Helper()
	var raw []byte
	switch b := body.(type) {
	case string:
		raw = []byte(b)
	default:
		var err error
		if raw, err = json.Marshal(b); err != nil {
			h.t.Fatalf("harness: encode response for %s %s: %v", method, path, err)
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.routes[method+" "+path] = apiResponse{status: status, body: raw}
}

// recorded returns a copy of every request seen so far, in arrival order.
func (h *calendarAPIHarness) recorded() []apiRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]apiRequest(nil), h.requests...)
}

// only returns the single recorded request, failing the test otherwise.
func (h *calendarAPIHarness) only() apiRequest {
	h.t.Helper()
	reqs := h.recorded()
	if len(reqs) != 1 {
		h.t.Fatalf("harness: %d requests recorded, want exactly 1: %+v", len(reqs), reqs)
	}
	return reqs[0]
}

func (h *calendarAPIHarness) serve(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		h.t.Errorf("harness: read %s %s body: %v", r.Method, r.URL.Path, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(body) == 0 {
		body = nil
	}

	h.mu.Lock()
	h.requests = append(h.requests, apiRequest{
		Method: r.Method,
		Path:   r.URL.Path,
		Query:  r.URL.Query(),
		Header: r.Header.Clone(),
		Body:   body,
	})
	resp, ok := h.routes[r.Method+" "+r.URL.Path]
	h.mu.Unlock()

	if !ok {
		h.t.Errorf("harness: no route for %s %s", r.Method, r.URL.Path)
		http.Error(w, `{"error":{"code":501,"message":"harness: no route"}}`, http.StatusNotImplemented)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.status)
	if _, err := w.Write(resp.body); err != nil {
		h.t.Errorf("harness: write response: %v", err)
	}
}

// --- smoke test ---

func TestCalendarAPIHarness_Smoke(t *testing.T) {
	h := newCalendarAPIHarness(t)
	h.respond(http.MethodGet, "/calendars/primary/events", http.StatusOK, &calendar.Events{
		Items: []*calendar.Event{{
			Id:      "evt-1",
			Summary: "Standup",
			Status:  "confirmed",
			Start:   &calendar.EventDateTime{DateTime: "2026-11-26T09:00:00Z"},
			End:     &calendar.EventDateTime{DateTime: "2026-11-26T10:00:00Z"},
		}},
	})

	list, err := h.driver().ListEvents(context.Background(), domain.EventQuery{
		CalendarID: "primary",
		Start:      time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC),
		End:        time.Date(2026, 11, 27, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	events := list.Events
	if len(events) != 1 || events[0].UID != "evt-1" || events[0].Summary != "Standup" {
		t.Fatalf("ListEvents = %+v, want evt-1 Standup", events)
	}

	req := h.only()
	if req.Method != http.MethodGet {
		t.Errorf("method = %s, want GET", req.Method)
	}
	if got := req.Query.Get("timeMin"); got != "2026-11-26T00:00:00Z" {
		t.Errorf("timeMin = %q", got)
	}
	if got := req.Query.Get("singleEvents"); got != "true" {
		t.Errorf("singleEvents = %q, want true", got)
	}
}
