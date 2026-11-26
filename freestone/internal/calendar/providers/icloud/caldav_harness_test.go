package icloud

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"

	"github.com/kai/freestone/internal/calendar"
)

// The CalDAV wire harness: go-webdav's own caldav.Handler served on an
// httptest.Server over an in-memory backend, so driver tests exercise the
// real client, the real XML on the wire and the real server semantics.
//
// go-webdav derives the resource type from path depth (1 = principal,
// 2 = home set, 3 = calendar, 4 = object), so every href below follows
// /<user>/calendars/<calendar>/<object>.ics.
const (
	harnessPrincipal = "/user/"
	harnessHomeSet   = "/user/calendars/"
)

// harnessRequest is one HTTP request as the harness server saw it.
type harnessRequest struct {
	Method  string
	Path    string
	IfMatch string // raw header value, quotes included; "" when absent
}

// harnessObject is a stored calendar object.
type harnessObject struct {
	Data *ical.Calendar
	ETag string // unquoted
}

// calDAVHarness is the in-memory caldav.Backend plus the server serving it.
type calDAVHarness struct {
	t   *testing.T
	srv *httptest.Server

	mu        sync.Mutex
	calendars map[string]caldav.Calendar // by path, trailing slash
	objects   map[string]harnessObject   // by href, any name
	requests  []harnessRequest
	etagSeq   int

	// icloudPartialRetrieval makes REPORT answers to a partial-retrieval
	// request behave as live iCloud does rather than as RFC 4791 says; see
	// serveICloudPartialRetrieval. Set it before the first request.
	icloudPartialRetrieval bool
}

// withICloudPartialRetrieval switches the harness to iCloud's answer for a
// calendar-data request with nested allprop and returns it.
func (h *calDAVHarness) withICloudPartialRetrieval() *calDAVHarness {
	h.icloudPartialRetrieval = true
	return h
}

// newCalDAVHarness starts a server with an empty home set; it is closed
// when the test ends.
func newCalDAVHarness(t *testing.T) *calDAVHarness {
	t.Helper()
	h := &calDAVHarness{
		t:         t,
		calendars: map[string]caldav.Calendar{},
		objects:   map[string]harnessObject{},
	}
	h.srv = httptest.NewServer(h.recordAndGuard(&caldav.Handler{Backend: h}))
	t.Cleanup(h.srv.Close)
	return h
}

// driver builds a Driver through the production constructor, so principal
// and home-set discovery run over the wire too.
func (h *calDAVHarness) driver() *Driver {
	h.t.Helper()
	d, err := New(context.Background(), h.srv.URL, "user", "secret", time.UTC, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		h.t.Fatalf("harness: New: %v", err)
	}
	return d
}

// addCalendar creates a calendar named name under the home set and returns
// its path (with trailing slash), which is the driver's calendar ID.
func (h *calDAVHarness) addCalendar(name, displayName string) string {
	p := harnessHomeSet + name + "/"
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calendars[p] = caldav.Calendar{Path: p, Name: displayName}
	return p
}

// seedObject stores iCalendar text at href (which need not be <uid>.ics)
// and returns the ETag it was given. Lines may end in LF or CRLF.
func (h *calDAVHarness) seedObject(href, ics string) string {
	h.t.Helper()
	text := strings.ReplaceAll(strings.ReplaceAll(ics, "\r\n", "\n"), "\n", "\r\n")
	cal, err := ical.NewDecoder(strings.NewReader(text)).Decode()
	if err != nil {
		h.t.Fatalf("harness: seed %s: %v", href, err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.store(href, cal)
}

// object returns what is stored at href.
func (h *calDAVHarness) object(href string) (harnessObject, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	o, ok := h.objects[href]
	return o, ok
}

// hrefs returns every stored object href, sorted.
func (h *calDAVHarness) hrefs() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, 0, len(h.objects))
	for p := range h.objects {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// recorded returns a copy of every request seen so far, in arrival order.
func (h *calDAVHarness) recorded() []harnessRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]harnessRequest(nil), h.requests...)
}

// recordedWith returns the recorded requests with the given method.
func (h *calDAVHarness) recordedWith(method string) []harnessRequest {
	var out []harnessRequest
	for _, r := range h.recorded() {
		if r.Method == method {
			out = append(out, r)
		}
	}
	return out
}

// store writes cal at href under a fresh ETag. Caller holds mu.
func (h *calDAVHarness) store(href string, cal *ical.Calendar) string {
	h.etagSeq++
	etag := fmt.Sprintf("etag-%d", h.etagSeq)
	h.objects[href] = harnessObject{Data: cal, ETag: etag}
	return etag
}

// recordAndGuard records every request, enforces If-Match on DELETE and
// carries a REPORT's calendar-data request to the backend.
//
// go-webdav's Backend.DeleteCalendarObject receives no options, so a
// conditional DELETE can only be honoured in front of the handler. And the
// handler passes no calendar-data request to QueryCalendarObjects at all,
// and drops every component name when it decodes one for a multiget, so
// the harness reads it from the REPORT body itself.
func (h *calDAVHarness) recordAndGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ifMatch := r.Header.Get("If-Match")
		h.mu.Lock()
		h.requests = append(h.requests, harnessRequest{Method: r.Method, Path: r.URL.Path, IfMatch: ifMatch})
		h.mu.Unlock()

		if r.Method == http.MethodDelete && ifMatch != "" {
			o, exists := h.object(r.URL.Path)
			if !harnessEntityTagMatches(ifMatch, o.ETag, exists) {
				http.Error(w, "harness: If-Match does not match", http.StatusPreconditionFailed)
				return
			}
		}

		if r.Method == "REPORT" {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			var report harnessReportXML
			if err := xml.Unmarshal(body, &report); err != nil {
				http.Error(w, "harness: malformed REPORT body: "+err.Error(), http.StatusBadRequest)
				return
			}
			if comp := report.Prop.CalendarData.Comp; comp != nil {
				req := comp.request()
				if h.icloudPartialRetrieval && hasNestedAllProp(req) {
					serveICloudPartialRetrieval(next, w, r)
					return
				}
				r = r.WithContext(context.WithValue(r.Context(), harnessCompRequestKey{}, req))
			}
		}
		next.ServeHTTP(w, r)
	})
}

// harnessEntityTagMatches compares an If-Match / If-None-Match value the
// way a real server does: "*" matches any existing object; anything else
// must be a quoted entity tag equal to the stored one. go-webdav's client
// sends the header verbatim and returns ETags unquoted, so a driver that
// passes CalendarObject.ETag straight through sends an unquoted value,
// which matches nothing.
func harnessEntityTagMatches(header, etag string, exists bool) bool {
	if !exists {
		return false
	}
	if header == "*" {
		return true
	}
	unquoted, err := strconv.Unquote(header)
	return err == nil && unquoted == etag
}

// harnessCompRequestKey carries a REPORT's calendar-data request in the
// request context from recordAndGuard to the backend.
type harnessCompRequestKey struct{}

const nsCalDAV = "urn:ietf:params:xml:ns:caldav"

// harnessReportXML is the part of a calendar-query or calendar-multiget
// body the harness needs: DAV:prop/C:calendar-data/C:comp.
type harnessReportXML struct {
	Prop struct {
		CalendarData struct {
			Comp *harnessCompXML `xml:"urn:ietf:params:xml:ns:caldav comp"`
		} `xml:"urn:ietf:params:xml:ns:caldav calendar-data"`
	} `xml:"DAV: prop"`
}

type harnessCompXML struct {
	Name    string    `xml:"name,attr"`
	AllProp *struct{} `xml:"urn:ietf:params:xml:ns:caldav allprop"`
	Props   []struct {
		Name string `xml:"name,attr"`
	} `xml:"urn:ietf:params:xml:ns:caldav prop"`
	AllComp *struct{}        `xml:"urn:ietf:params:xml:ns:caldav allcomp"`
	Comps   []harnessCompXML `xml:"urn:ietf:params:xml:ns:caldav comp"`
}

func (c *harnessCompXML) request() *caldav.CalendarCompRequest {
	req := &caldav.CalendarCompRequest{Name: c.Name, AllProps: c.AllProp != nil, AllComps: c.AllComp != nil}
	for _, p := range c.Props {
		req.Props = append(req.Props, p.Name)
	}
	for i := range c.Comps {
		req.Comps = append(req.Comps, *c.Comps[i].request())
	}
	return req
}

// harnessView returns the object as the REPORT in ctx asked for it: only
// the requested sub-components (recursively) and, when a prop list is
// given, only those properties. Outside a REPORT, or for an empty or
// allcomp request, it is the whole object, as for GET. The stored object
// is never modified.
func harnessView(ctx context.Context, cal *ical.Calendar) *ical.Calendar {
	req, _ := ctx.Value(harnessCompRequestKey{}).(*caldav.CalendarCompRequest)
	if req == nil {
		return cal
	}
	return &ical.Calendar{Component: harnessPrune(cal.Component, req)}
}

// serveICloudPartialRetrieval answers a REPORT the way live iCloud answers
// a calendar-data request of VCALENDAR(allprop) > VEVENT(allprop): every
// VEVENT comes back with none of its own properties (no UID, DTSTART,
// SUMMARY) while its sub-components such as VALARM are kept. Observed
// against the real iCloud CalDAV server on 2026-11-26; RFC 4791 §9.6 would
// return the properties. Nothing was observed about other components, so
// they are left whole.
//
// The stripping is done on the wire because go-webdav's handler encodes
// with go-ical, which refuses a VEVENT without UID and DTSTAMP; the client's
// decoder does not check, which is how the stripped VEVENT reached the
// driver live.
func serveICloudPartialRetrieval(next http.Handler, w http.ResponseWriter, r *http.Request) {
	rec := httptest.NewRecorder()
	next.ServeHTTP(rec, r) // no component request in ctx: the whole object
	body, err := stripVEventPropsInMultistatus(rec.Body.Bytes())
	if err != nil {
		http.Error(w, "harness: "+err.Error(), http.StatusInternalServerError)
		return
	}
	for k, v := range rec.Header() {
		if k != "Content-Length" {
			w.Header()[k] = v
		}
	}
	w.WriteHeader(rec.Code)
	w.Write(body)
}

var harnessCalendarDataXML = regexp.MustCompile(`(?s)(<[^>/]*calendar-data[^>]*>)(.*?)(</[^>]*calendar-data>)`)

// stripVEventPropsInMultistatus rewrites every calendar-data element of a
// multistatus body, dropping the property lines of each VEVENT.
func stripVEventPropsInMultistatus(body []byte) ([]byte, error) {
	var failed error
	out := harnessCalendarDataXML.ReplaceAllFunc(body, func(m []byte) []byte {
		parts := harnessCalendarDataXML.FindSubmatch(m)
		var text struct {
			Data string `xml:",chardata"`
		}
		if err := xml.Unmarshal(append(append([]byte("<x>"), parts[2]...), "</x>"...), &text); err != nil {
			failed = fmt.Errorf("decode calendar-data: %w", err)
			return m
		}
		var buf bytes.Buffer
		buf.Write(parts[1])
		if err := xml.EscapeText(&buf, []byte(stripVEventProps(text.Data))); err != nil {
			failed = err
			return m
		}
		buf.Write(parts[3])
		return buf.Bytes()
	})
	return out, failed
}

// stripVEventProps drops every content line that belongs to a VEVENT
// itself (folded continuations included), keeping its BEGIN/END and the
// whole of any component nested in it.
func stripVEventProps(ics string) string {
	var kept []string
	depth := 0 // 1 directly inside a VEVENT, >1 inside a component nested in it
	for _, line := range strings.Split(ics, "\r\n") {
		switch {
		case depth == 0 && line == "BEGIN:VEVENT":
			depth = 1
		case depth > 0 && strings.HasPrefix(line, "BEGIN:"):
			depth++
		case depth > 0 && strings.HasPrefix(line, "END:"):
			depth--
		case depth == 1:
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\r\n")
}

// hasNestedAllProp reports whether any component request below the root
// carries allprop, e.g. VCALENDAR(allprop) > VEVENT(allprop).
func hasNestedAllProp(req *caldav.CalendarCompRequest) bool {
	for i := range req.Comps {
		if req.Comps[i].AllProps || hasNestedAllProp(&req.Comps[i]) {
			return true
		}
	}
	return false
}

func harnessPrune(c *ical.Component, req *caldav.CalendarCompRequest) *ical.Component {
	out := &ical.Component{Name: c.Name, Props: ical.Props{}}
	if req.AllProps || len(req.Props) == 0 {
		for name, props := range c.Props {
			out.Props[name] = props
		}
	} else {
		for _, name := range req.Props {
			if props, ok := c.Props[name]; ok {
				out.Props[name] = props
			}
		}
	}
	if req.AllComps {
		out.Children = c.Children
		return out
	}
	for _, child := range c.Children {
		for i := range req.Comps {
			if req.Comps[i].Name == child.Name {
				out.Children = append(out.Children, harnessPrune(child, &req.Comps[i]))
				break
			}
		}
	}
	return out
}

func harnessCollectionPath(p string) string {
	if strings.HasSuffix(p, "/") {
		return p
	}
	return p + "/"
}

func harnessNotFound(what string) error {
	return webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("harness: %s not found", what))
}

// --- caldav.Backend ---

func (h *calDAVHarness) CurrentUserPrincipal(context.Context) (string, error) {
	return harnessPrincipal, nil
}

func (h *calDAVHarness) CalendarHomeSetPath(context.Context) (string, error) {
	return harnessHomeSet, nil
}

func (h *calDAVHarness) CreateCalendar(context.Context, *caldav.Calendar) error {
	return webdav.NewHTTPError(http.StatusForbidden, fmt.Errorf("harness: MKCOL not supported; use addCalendar"))
}

func (h *calDAVHarness) ListCalendars(context.Context) ([]caldav.Calendar, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]caldav.Calendar, 0, len(h.calendars))
	for _, c := range h.calendars {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func (h *calDAVHarness) GetCalendar(_ context.Context, p string) (*caldav.Calendar, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.calendars[harnessCollectionPath(p)]
	if !ok {
		return nil, harnessNotFound("calendar " + p)
	}
	return &c, nil
}

// GetCalendarObject ignores its CalendarCompRequest: go-webdav decodes a
// multiget's request without component names, so harnessView reads the
// REPORT's own request from ctx instead.
func (h *calDAVHarness) GetCalendarObject(ctx context.Context, p string, _ *caldav.CalendarCompRequest) (*caldav.CalendarObject, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	o, ok := h.objects[p]
	if !ok {
		return nil, harnessNotFound("object " + p)
	}
	return &caldav.CalendarObject{Path: p, ETag: o.ETag, Data: harnessView(ctx, o.Data)}, nil
}

// ListCalendarObjects returns every object under the collection path, so
// a query on the home set spans all calendars, as the driver relies on
// when no calendar ID is given.
func (h *calDAVHarness) ListCalendarObjects(_ context.Context, p string, _ *caldav.CalendarCompRequest) ([]caldav.CalendarObject, error) {
	prefix := harnessCollectionPath(p)
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []caldav.CalendarObject
	for href, o := range h.objects {
		if strings.HasPrefix(href, prefix) {
			out = append(out, caldav.CalendarObject{Path: href, ETag: o.ETag, Data: o.Data})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func (h *calDAVHarness) QueryCalendarObjects(ctx context.Context, p string, query *caldav.CalendarQuery) ([]caldav.CalendarObject, error) {
	all, err := h.ListCalendarObjects(ctx, p, nil)
	if err != nil {
		return nil, err
	}
	// Filter on the whole object, then trim the answer to what was asked.
	matched, err := caldav.Filter(query, all)
	if err != nil {
		return nil, err
	}
	for i := range matched {
		matched[i].Data = harnessView(ctx, matched[i].Data)
	}
	return matched, nil
}

func (h *calDAVHarness) PutCalendarObject(_ context.Context, p string, cal *ical.Calendar, opts *caldav.PutCalendarObjectOptions) (*caldav.CalendarObject, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	existing, exists := h.objects[p]
	if opts != nil {
		if opts.IfMatch.IsSet() && !harnessEntityTagMatches(string(opts.IfMatch), existing.ETag, exists) {
			return nil, webdav.NewHTTPError(http.StatusPreconditionFailed, fmt.Errorf("harness: If-Match does not match"))
		}
		if opts.IfNoneMatch.IsSet() && harnessEntityTagMatches(string(opts.IfNoneMatch), existing.ETag, exists) {
			return nil, webdav.NewHTTPError(http.StatusPreconditionFailed, fmt.Errorf("harness: If-None-Match matches"))
		}
	}
	etag := h.store(p, cal)
	return &caldav.CalendarObject{Path: p, ETag: etag, Data: cal}, nil
}

func (h *calDAVHarness) DeleteCalendarObject(_ context.Context, p string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.objects[p]; !ok {
		return harnessNotFound("object " + p)
	}
	delete(h.objects, p)
	return nil
}

// --- smoke test ---

func TestCalDAVHarness_Smoke(t *testing.T) {
	h := newCalDAVHarness(t)
	cal := h.addCalendar("personal", "Personal")
	h.seedObject(cal+"ABC-random.ics", `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//harness//EN
BEGIN:VEVENT
UID:uid-1
DTSTAMP:20261101T080000Z
DTSTART:20261126T090000Z
DTEND:20261126T100000Z
SUMMARY:Standup
END:VEVENT
END:VCALENDAR
`)
	d := h.driver()
	ctx := context.Background()

	cals, err := d.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	if len(cals) != 1 || cals[0].ID != cal || cals[0].Name != "Personal" {
		t.Fatalf("ListCalendars = %+v, want one calendar %s named Personal", cals, cal)
	}

	list, err := d.ListEvents(ctx, calendar.EventQuery{
		CalendarID: cal,
		Start:      harnessTime("2026-11-26T00:00:00Z"),
		End:        harnessTime("2026-11-27T00:00:00Z"),
	})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	events := list.Events
	if len(events) != 1 || events[0].UID != "uid-1" || events[0].Summary != "Standup" {
		t.Fatalf("ListEvents = %+v, want uid-1 Standup", events)
	}
	if events[0].ETag != "etag-1" {
		t.Errorf("ETag = %q, want etag-1", events[0].ETag)
	}

	if got := h.recordedWith("REPORT"); len(got) != 1 || got[0].Path != cal {
		t.Errorf("REPORT requests = %+v, want one on %s", got, cal)
	}
}

// harnessTime parses an RFC 3339 literal; a typo in a test is a panic.
func harnessTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// TestCalDAVHarness_ConditionalWrites pins the If-Match behaviour later
// ETag tests depend on. go-webdav's client sends the ConditionalMatch
// verbatim while CalendarObject.ETag comes back unquoted, so the caller
// must quote it; an unquoted value is a 400, not a match.
func TestCalDAVHarness_ConditionalWrites(t *testing.T) {
	h := newCalDAVHarness(t)
	cal := h.addCalendar("personal", "Personal")
	href := cal + "uid-1.ics"
	etag := h.seedObject(href, `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//harness//EN
BEGIN:VEVENT
UID:uid-1
DTSTAMP:20261101T080000Z
DTSTART:20261126T090000Z
SUMMARY:Standup
END:VEVENT
END:VCALENDAR
`)
	d := h.driver()
	ctx := context.Background()
	stored, _ := h.object(href)
	quoted := func(e string) webdav.ConditionalMatch { return webdav.ConditionalMatch(fmt.Sprintf("%q", e)) }

	_, err := d.client.PutCalendarObject(ctx, href, stored.Data, &caldav.PutCalendarObjectOptions{IfMatch: quoted("stale")})
	if err == nil || !strings.Contains(err.Error(), "412") {
		t.Fatalf("PUT with stale If-Match: err = %v, want 412", err)
	}
	_, err = d.client.PutCalendarObject(ctx, href, stored.Data, &caldav.PutCalendarObjectOptions{IfMatch: webdav.ConditionalMatch(etag)})
	if err == nil || !strings.Contains(err.Error(), "412") {
		t.Fatalf("PUT with unquoted current If-Match: err = %v, want 412", err)
	}
	put, err := d.client.PutCalendarObject(ctx, href, stored.Data, &caldav.PutCalendarObjectOptions{IfMatch: quoted(etag)})
	if err != nil {
		t.Fatalf("PUT with current If-Match: %v", err)
	}
	if put.ETag == etag {
		t.Errorf("ETag unchanged after write: %q", put.ETag)
	}

	if err := d.client.RemoveAll(ctx, href, &webdav.RemoveAllOptions{IfMatch: quoted(etag)}); err == nil || !strings.Contains(err.Error(), "412") {
		t.Fatalf("DELETE with stale If-Match: err = %v, want 412", err)
	}
	if err := d.client.RemoveAll(ctx, href, &webdav.RemoveAllOptions{IfMatch: webdav.ConditionalMatch(put.ETag)}); err == nil || !strings.Contains(err.Error(), "412") {
		t.Fatalf("DELETE with unquoted current If-Match: err = %v, want 412", err)
	}
	if err := d.client.RemoveAll(ctx, href, &webdav.RemoveAllOptions{IfMatch: quoted(put.ETag)}); err != nil {
		t.Fatalf("DELETE with current If-Match: %v", err)
	}
	if _, ok := h.object(href); ok {
		t.Errorf("object still stored after DELETE")
	}

	dels := h.recordedWith(http.MethodDelete)
	if len(dels) != 3 || dels[2].IfMatch != fmt.Sprintf("%q", put.ETag) {
		t.Errorf("DELETE requests = %+v", dels)
	}
}

// TestCalDAVHarness_ReportTrimsToRequestedComponents pins that a REPORT
// asking for VEVENT properties only answers without VTIMEZONE and VALARM,
// while GET and the store keep the whole object, so later tests can tell
// a driver that writes back what it queried from one that keeps the rest.
func TestCalDAVHarness_ReportTrimsToRequestedComponents(t *testing.T) {
	h := newCalDAVHarness(t)
	cal := h.addCalendar("personal", "Personal")
	href := cal + "uid-1.ics"
	h.seedObject(href, `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//harness//EN
BEGIN:VTIMEZONE
TZID:Europe/Berlin
BEGIN:STANDARD
DTSTART:19701025T030000
TZOFFSETFROM:+0200
TZOFFSETTO:+0100
END:STANDARD
END:VTIMEZONE
BEGIN:VEVENT
UID:uid-1
DTSTAMP:20261101T080000Z
DTSTART:20261126T090000Z
SUMMARY:Standup
BEGIN:VALARM
ACTION:DISPLAY
TRIGGER:-PT15M
END:VALARM
END:VEVENT
END:VCALENDAR
`)
	d := h.driver()
	ctx := context.Background()
	names := func(c *ical.Component) []string {
		var out []string
		for _, ch := range c.Children {
			out = append(out, ch.Name)
		}
		return out
	}

	objs, err := d.client.QueryCalendar(ctx, cal, &caldav.CalendarQuery{
		CompRequest: caldav.CalendarCompRequest{
			Name: "VCALENDAR", AllProps: true,
			Comps: []caldav.CalendarCompRequest{{Name: "VEVENT", AllProps: true}},
		},
		CompFilter: caldav.CompFilter{Name: "VCALENDAR", Comps: []caldav.CompFilter{{Name: "VEVENT"}}},
	})
	if err != nil {
		t.Fatalf("QueryCalendar: %v", err)
	}
	if len(objs) != 1 {
		t.Fatalf("QueryCalendar returned %d objects, want 1", len(objs))
	}
	if got := names(objs[0].Data.Component); len(got) != 1 || got[0] != "VEVENT" {
		t.Errorf("REPORT top-level components = %v, want [VEVENT]", got)
	}
	if got := names(objs[0].Data.Children[0]); len(got) != 0 {
		t.Errorf("REPORT VEVENT sub-components = %v, want none", got)
	}

	obj, err := d.client.GetCalendarObject(ctx, href)
	if err != nil {
		t.Fatalf("GetCalendarObject: %v", err)
	}
	if got := names(obj.Data.Component); len(got) != 2 {
		t.Errorf("GET top-level components = %v, want VTIMEZONE and VEVENT", got)
	}
	stored, _ := h.object(href)
	if got := names(stored.Data.Children[1]); len(got) != 1 || got[0] != "VALARM" {
		t.Errorf("stored VEVENT sub-components = %v, want [VALARM]", got)
	}
}
