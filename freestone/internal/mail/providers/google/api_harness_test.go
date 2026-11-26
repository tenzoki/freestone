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

	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"

	domain "github.com/kai/freestone/internal/mail"
)

// The Gmail wire harness: an httptest server standing in for the Gmail v1
// API. Tests register a canned answer per method and path and read back
// exactly what the driver sent. Same shape as the Google Calendar harness in
// internal/calendar/providers/google/api_harness_test.go.

// Paths the driver reaches, as the harness sees them (decoded, user "me").
const (
	gmailMessagesPath = "/gmail/v1/users/me/messages"
	gmailDraftsPath   = "/gmail/v1/users/me/drafts"
	gmailLabelsPath   = "/gmail/v1/users/me/labels"
)

func gmailMessagePath(id string) string { return gmailMessagesPath + "/" + id }

func gmailMessageModifyPath(id string) string { return gmailMessagePath(id) + "/modify" }

func gmailLabelPath(id string) string { return gmailLabelsPath + "/" + id }

// apiRequest is one HTTP request as the harness server saw it.
type apiRequest struct {
	Method string
	Path   string // decoded, e.g. /gmail/v1/users/me/messages/m-1
	Query  url.Values
	Header http.Header
	Body   []byte // raw request body; nil when empty
}

// JSON decodes the request body into a generic map, so a test can check
// which fields were sent at all.
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

// gmailAPIHarness routes Gmail v1 requests to canned answers.
type gmailAPIHarness struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	routes   map[string]apiResponse // key: "METHOD /path"
	requests []apiRequest
}

// newGmailAPIHarness starts a server with no routes; it is closed when the
// test ends. A request to an unregistered route fails the test.
func newGmailAPIHarness(t *testing.T) *gmailAPIHarness {
	t.Helper()
	h := &gmailAPIHarness{t: t, routes: map[string]apiResponse{}}
	h.srv = httptest.NewServer(http.HandlerFunc(h.serve))
	t.Cleanup(h.srv.Close)
	return h
}

// driver builds a Driver whose service talks to the harness server.
func (h *gmailAPIHarness) driver() *Driver {
	h.t.Helper()
	svc, err := gmail.NewService(context.Background(),
		option.WithEndpoint(h.srv.URL),
		option.WithHTTPClient(h.srv.Client()),
		option.WithoutAuthentication(),
	)
	if err != nil {
		h.t.Fatalf("harness: gmail.NewService: %v", err)
	}
	return &Driver{svc: svc}
}

// respond registers the answer to method+path (path decoded, as in
// apiRequest.Path). body is sent as is when it is a string, otherwise it
// is JSON-encoded, so a test can pass a *gmail.Message or
// *gmail.ListMessagesResponse. Registering the same route again replaces
// the answer.
func (h *gmailAPIHarness) respond(method, path string, status int, body any) {
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
func (h *gmailAPIHarness) recorded() []apiRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]apiRequest(nil), h.requests...)
}

// only returns the single recorded request, failing the test otherwise.
func (h *gmailAPIHarness) only() apiRequest {
	h.t.Helper()
	reqs := h.recorded()
	if len(reqs) != 1 {
		h.t.Fatalf("harness: %d requests recorded, want exactly 1: %+v", len(reqs), reqs)
	}
	return reqs[0]
}

func (h *gmailAPIHarness) serve(w http.ResponseWriter, r *http.Request) {
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

func TestGmailAPIHarness_Smoke(t *testing.T) {
	h := newGmailAPIHarness(t)
	h.respond(http.MethodGet, gmailMessagesPath, http.StatusOK, &gmail.ListMessagesResponse{
		Messages: []*gmail.Message{{Id: "m-1", ThreadId: "t-1"}},
	})
	h.respond(http.MethodGet, gmailMessagePath("m-1"), http.StatusOK, &gmail.Message{
		Id:       "m-1",
		ThreadId: "t-1",
		LabelIds: []string{"INBOX", "UNREAD"},
		Payload: &gmail.MessagePart{Headers: []*gmail.MessagePartHeader{
			{Name: "Subject", Value: "Hello harness"},
			{Name: "From", Value: "Alice <alice@example.com>"},
		}},
	})

	msgs, err := h.driver().ListMessages(context.Background(), domain.MessageQuery{MailboxID: "INBOX", Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 1 || msgs[0].ID != "m-1" || msgs[0].Subject != "Hello harness" {
		t.Fatalf("ListMessages = %+v, want m-1 Hello harness", msgs)
	}

	reqs := h.recorded()
	if len(reqs) != 2 {
		t.Fatalf("harness: %d requests recorded, want list + get: %+v", len(reqs), reqs)
	}
	list, get := reqs[0], reqs[1]
	if list.Path != gmailMessagesPath || list.Query.Get("labelIds") != "INBOX" || list.Query.Get("maxResults") != "10" {
		t.Errorf("list request = %s %v", list.Path, list.Query)
	}
	if get.Path != gmailMessagePath("m-1") || get.Query.Get("format") != "metadata" {
		t.Errorf("get request = %s %v", get.Path, get.Query)
	}
}
