package mail

// The mail write-surface guarantee: freestone saves drafts and marks messages
// read, and does nothing else that changes a mailbox. It never sends, deletes,
// trashes, moves or expunges, and it sets \Seen (IMAP) or removes UNREAD
// (Gmail) only inside a MarkRead method. An IMAP body fetch without the PEEK
// modifier sets \Seen implicitly (RFC 9051 6.4.5), so every body or binary
// section requested outside MarkRead must set Peek: true.
//
// The Gmail scope freestone needs for drafts and mark-read (gmail.modify) also
// permits sending and trashing, so the OAuth grant is not a guard. This test is.
// It parses every non-test .go file under providers/ with go/parser and walks
// the AST, so identifiers in comments and string literals cannot trip it, and
// a renamed import cannot hide a forbidden constant.
//
// The scan is syntactic: it matches selector names, not resolved types. That is
// deliberately conservative. A selector named Move or Send on any value is
// flagged, whether or not it is the go-imap or Gmail method; selectors on an
// imported package (io.Copy, slices.Delete) are exempt because those are
// package functions, not mailbox operations.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	imapImportPath  = "github.com/emersion/go-imap/v2"
	gmailImportPath = "google.golang.org/api/gmail/v1"

	// markReadMethod is the only method allowed to change read state.
	markReadMethod = "MarkRead"
)

// forbiddenSelectors are method (or service field) names the provider code
// must never reference, on any receiver. The value says why.
var forbiddenSelectors = map[string]string{
	// go-imap/v2 imapclient.Client
	"Move":               "IMAP MOVE relocates messages",
	"Copy":               "IMAP COPY is the first half of a move to Trash or Junk",
	"Expunge":            "IMAP EXPUNGE permanently removes messages",
	"UIDExpunge":         "IMAP UID EXPUNGE permanently removes messages",
	"UnselectAndExpunge": "IMAP CLOSE expunges the selected mailbox",
	"Rename":             "IMAP RENAME relocates a whole mailbox",
	// go-imap/v2 Client.Delete (mailbox) and Gmail Messages/Drafts/Labels.Delete
	"Delete": "deletes a mailbox, message, draft or label",
	// gmail/v1 Users.Messages and Users.Drafts
	"Send":        "Gmail Messages.Send / Drafts.Send sends mail",
	"Trash":       "Gmail Messages.Trash moves a message to Trash",
	"BatchDelete": "Gmail Messages.BatchDelete permanently deletes messages",
	"BatchModify": "Gmail Messages.BatchModify changes labels (including UNREAD, TRASH) in bulk",
	"Import":      "Gmail Messages.Import places a message in the mailbox",
	"Insert":      "Gmail Messages.Insert places a message in the mailbox",
	// gmail/v1 Users services that act on mail outside the draft/mark-read surface
	"Threads":  "Gmail Users.Threads modifies, trashes or deletes whole conversations",
	"Settings": "Gmail Users.Settings reaches filters, forwarding and send-as, each able to send or delete mail",
}

// markReadOnlySelectors are the read-state writers. They may appear only
// inside a MarkRead method.
var markReadOnlySelectors = map[string]string{
	"Store":  "IMAP STORE changes message flags",
	"Modify": "Gmail Messages.Modify changes message labels",
}

// searchFlagFields are the imap.SearchCriteria fields where imap.FlagSeen is
// a read-only filter, not a write.
var searchFlagFields = map[string]bool{"Flag": true, "NotFlag": true}

type violation struct {
	pos  token.Position
	what string
}

func (v violation) String() string { return fmt.Sprintf("%s: %s", v.pos, v.what) }

// fileScan is the result of scanning one file.
type fileScan struct {
	violations []violation
	// markReadWrites counts Store/Modify selectors inside MarkRead, so the
	// real-tree test can prove the scan actually saw the write path.
	markReadWrites int
}

type surfaceScanner struct {
	fset    *token.FileSet
	imports map[string]string // local package name -> import path
	stack   []ast.Node
	result  fileScan
}

func scanWriteSurface(fset *token.FileSet, file *ast.File) fileScan {
	s := &surfaceScanner{fset: fset, imports: importNames(file)}
	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil {
			s.stack = s.stack[:len(s.stack)-1]
			return true
		}
		s.stack = append(s.stack, n)
		s.visit(n)
		return true
	})
	return s.result
}

// importNames maps each import's local name to its path. Without an alias the
// name is the last path element, skipping a major-version suffix and a "go-"
// prefix (go-imap/v2 -> imap, gmail/v1 -> gmail). A wrong guess here can only
// cause a false positive (a package call treated as a method), never hide one.
var majorVersion = regexp.MustCompile(`^v[0-9]+$`)

func importNames(file *ast.File) map[string]string {
	names := make(map[string]string, len(file.Imports))
	for _, imp := range file.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue // unreachable for a file go/parser accepted
		}
		var name string
		if imp.Name != nil {
			name = imp.Name.Name
		} else {
			elems := strings.Split(p, "/")
			name = elems[len(elems)-1]
			if majorVersion.MatchString(name) && len(elems) > 1 {
				name = elems[len(elems)-2]
			}
			name = strings.TrimPrefix(name, "go-")
		}
		if name == "_" || name == "." {
			continue
		}
		names[name] = p
	}
	return names
}

func (s *surfaceScanner) report(n ast.Node, format string, args ...any) {
	s.result.violations = append(s.result.violations, violation{
		pos:  s.fset.Position(n.Pos()),
		what: fmt.Sprintf(format, args...),
	})
}

// inMarkRead reports whether the node being visited lies inside a method
// named MarkRead (function literals inside it count).
func (s *surfaceScanner) inMarkRead() bool {
	for _, n := range s.stack {
		if fd, ok := n.(*ast.FuncDecl); ok {
			return fd.Recv != nil && fd.Name.Name == markReadMethod
		}
	}
	return false
}

// pkgSelector returns the import path and selected name when sel is a
// package-qualified identifier such as imap.FlagSeen.
func (s *surfaceScanner) pkgSelector(e ast.Expr) (pkgPath, name string, ok bool) {
	sel, isSel := e.(*ast.SelectorExpr)
	if !isSel {
		return "", "", false
	}
	id, isIdent := sel.X.(*ast.Ident)
	if !isIdent {
		return "", "", false
	}
	p, isPkg := s.imports[id.Name]
	if !isPkg {
		return "", "", false
	}
	return p, sel.Sel.Name, true
}

func (s *surfaceScanner) visit(n ast.Node) {
	switch n := n.(type) {
	case *ast.SelectorExpr:
		if p, name, ok := s.pkgSelector(n); ok {
			if p == imapImportPath {
				s.checkIMAPConstant(n, name)
			}
			return
		}
		name := n.Sel.Name
		if why, bad := forbiddenSelectors[name]; bad {
			s.report(n, "forbidden selector .%s (%s)", name, why)
		}
		if why, restricted := markReadOnlySelectors[name]; restricted {
			if s.inMarkRead() {
				s.result.markReadWrites++
			} else {
				s.report(n, ".%s outside %s (%s)", name, markReadMethod, why)
			}
		}
	case *ast.CompositeLit:
		p, name, ok := s.pkgSelector(n.Type)
		if !ok {
			return
		}
		switch {
		case p == imapImportPath && name == "StoreFlags":
			s.checkStoreFlags(n)
		case p == imapImportPath && (name == "FetchItemBodySection" || name == "FetchItemBinarySection"):
			s.checkPeek(n, name)
		case p == gmailImportPath && name == "ModifyMessageRequest":
			s.checkModifyRequest(n)
		case p == gmailImportPath && (name == "ModifyThreadRequest" || name == "BatchModifyMessagesRequest" || name == "BatchDeleteMessagesRequest"):
			s.report(n, "gmail.%s literal (label or delete operation beyond MarkRead)", name)
		}
	}
}

// checkIMAPConstant enforces the flag rules on imap.* identifiers.
func (s *surfaceScanner) checkIMAPConstant(n *ast.SelectorExpr, name string) {
	switch name {
	case "FlagDeleted":
		s.report(n, "imap.FlagDeleted (marks a message for expunge)")
	case "StoreFlagsDel", "StoreFlagsSet":
		s.report(n, "imap.%s (only adding \\Seen is allowed)", name)
	case "FlagSeen":
		if !s.inMarkRead() && !s.inSearchCriterion() {
			s.report(n, "imap.FlagSeen outside %s and outside a search criterion", markReadMethod)
		}
	default:
		if name != "Flag" && strings.HasPrefix(name, "Flag") && s.inMarkRead() { // "Flag" itself is the type
			s.report(n, "imap.%s in %s (only \\Seen may be set)", name, markReadMethod)
		}
	}
}

// inSearchCriterion reports whether the node being visited is the value of a
// SearchCriteria Flag/NotFlag field, by assignment or by keyed literal.
func (s *surfaceScanner) inSearchCriterion() bool {
	for i := len(s.stack) - 1; i >= 0; i-- {
		switch n := s.stack[i].(type) {
		case *ast.AssignStmt:
			for _, lhs := range n.Lhs {
				if sel, ok := lhs.(*ast.SelectorExpr); ok && searchFlagFields[sel.Sel.Name] {
					return true
				}
			}
			return false
		case *ast.KeyValueExpr:
			if key, ok := n.Key.(*ast.Ident); ok && searchFlagFields[key.Name] {
				return true
			}
			return false
		}
	}
	return false
}

// checkPeek requires a body or binary fetch section outside MarkRead to carry
// Peek: true, written as the literal identifier true. Anything else (missing,
// false, a variable) is a violation, because a non-peek fetch marks the
// message read.
func (s *surfaceScanner) checkPeek(lit *ast.CompositeLit, name string) {
	if s.inMarkRead() {
		return
	}
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, isIdent := kv.Key.(*ast.Ident)
		if !isIdent || key.Name != "Peek" {
			continue
		}
		if val, isIdent := kv.Value.(*ast.Ident); isIdent && val.Name == "true" {
			return
		}
		break
	}
	s.report(lit, "imap.%s literal without Peek: true outside %s (a non-peek fetch sets \\Seen)", name, markReadMethod)
}

// checkStoreFlags allows exactly {Op: imap.StoreFlagsAdd, Flags: {imap.FlagSeen}},
// optionally Silent, and only inside MarkRead.
func (s *surfaceScanner) checkStoreFlags(lit *ast.CompositeLit) {
	if !s.inMarkRead() {
		s.report(lit, "imap.StoreFlags literal outside %s", markReadMethod)
		return
	}
	sawOp, sawFlags := false, false
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			s.report(elt, "imap.StoreFlags literal must use keyed fields")
			continue
		}
		key, _ := kv.Key.(*ast.Ident)
		switch {
		case key != nil && key.Name == "Silent":
		case key != nil && key.Name == "Op":
			sawOp = true
			if p, name, ok := s.pkgSelector(kv.Value); !ok || p != imapImportPath || name != "StoreFlagsAdd" {
				s.report(kv.Value, "imap.StoreFlags.Op must be imap.StoreFlagsAdd")
			}
		case key != nil && key.Name == "Flags":
			sawFlags = true
			flags, ok := kv.Value.(*ast.CompositeLit)
			if !ok || len(flags.Elts) != 1 {
				s.report(kv.Value, "imap.StoreFlags.Flags must be exactly {imap.FlagSeen}")
				continue
			}
			if p, name, ok := s.pkgSelector(flags.Elts[0]); !ok || p != imapImportPath || name != "FlagSeen" {
				s.report(flags.Elts[0], "imap.StoreFlags.Flags must be exactly {imap.FlagSeen}")
			}
		default:
			s.report(kv, "imap.StoreFlags field not allowed")
		}
	}
	if !sawOp || !sawFlags {
		s.report(lit, "imap.StoreFlags literal must set Op and Flags explicitly")
	}
}

// checkModifyRequest allows exactly {RemoveLabelIds: {"UNREAD"}}, only inside MarkRead.
func (s *surfaceScanner) checkModifyRequest(lit *ast.CompositeLit) {
	if !s.inMarkRead() {
		s.report(lit, "gmail.ModifyMessageRequest literal outside %s", markReadMethod)
		return
	}
	ok := len(lit.Elts) == 1
	var kv *ast.KeyValueExpr
	if ok {
		kv, ok = lit.Elts[0].(*ast.KeyValueExpr)
	}
	if ok {
		key, isIdent := kv.Key.(*ast.Ident)
		ok = isIdent && key.Name == "RemoveLabelIds"
		if ok {
			ids, isLit := kv.Value.(*ast.CompositeLit)
			ok = isLit && len(ids.Elts) == 1
			if ok {
				str, isStr := ids.Elts[0].(*ast.BasicLit)
				ok = isStr && str.Kind == token.STRING && str.Value == `"UNREAD"`
			}
		}
	}
	if !ok {
		s.report(lit, `gmail.ModifyMessageRequest must be exactly {RemoveLabelIds: []string{"UNREAD"}}`)
	}
}

func formatViolations(vs []violation) string {
	lines := make([]string, len(vs))
	for i, v := range vs {
		lines[i] = "  " + v.String()
	}
	return strings.Join(lines, "\n")
}

// TestMailWriteSurfaceGuarantee scans the real provider code.
func TestMailWriteSurfaceGuarantee(t *testing.T) {
	fset := token.NewFileSet()
	scanned := map[string]fileScan{}
	err := filepath.WalkDir("providers", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		scanned[filepath.ToSlash(p)] = scanWriteSurface(fset, file)
		return nil
	})
	if err != nil {
		t.Fatalf("scan providers/: %v", err)
	}

	// The scan must have seen both drivers and their MarkRead write, or a
	// passing result would mean nothing.
	for _, driver := range []string{"providers/icloud/driver.go", "providers/google/driver.go"} {
		res, ok := scanned[driver]
		if !ok {
			t.Fatalf("scan did not reach %s (scanned: %v)", driver, keys(scanned))
		}
		if res.markReadWrites != 1 {
			t.Errorf("%s: want exactly 1 read-state write inside %s, found %d", driver, markReadMethod, res.markReadWrites)
		}
	}

	var all []violation
	for _, p := range keys(scanned) {
		all = append(all, scanned[p].violations...)
	}
	if len(all) > 0 {
		t.Fatalf("mail provider code breaks the write-surface guarantee (drafts and mark-read only):\n%s", formatViolations(all))
	}
}

func keys(m map[string]fileScan) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out) // stable failure output
	return out
}

// TestMailWriteSurfaceScanner proves the scanner fails when it should, over
// fixture sources, so the real-tree test cannot pass by matching nothing.
func TestMailWriteSurfaceScanner(t *testing.T) {
	const header = `package fixture

import (
	"io"
	"slices"

	"github.com/emersion/go-imap/v2"
	"google.golang.org/api/gmail/v1"
)

var _ = io.Copy
var _ = slices.Delete[[]int]
var _ *gmail.Service
`
	cases := []struct {
		name string
		body string
		want string // substring some violation must contain; "" means clean
	}{
		{"mark read via STORE +\\Seen is allowed", `
func (d *D) MarkRead() {
	c.Store(uids, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagSeen}}, nil)
}`, ""},
		{"mark read via Gmail UNREAD removal is allowed", `
func (d *D) MarkRead() {
	d.svc.Users.Messages.Modify("me", id, &gmail.ModifyMessageRequest{RemoveLabelIds: []string{"UNREAD"}})
}`, ""},
		{"\\Seen as a search filter is allowed", `
func f() {
	criteria.NotFlag = append(criteria.NotFlag, imap.FlagSeen)
	_ = &imap.SearchCriteria{Flag: []imap.Flag{imap.FlagSeen}}
}`, ""},
		{"comments and strings do not trip the scan", `
// c.Move, c.Expunge(), Messages.Send and imap.FlagDeleted are never called.
func f() { _ = "d.svc.Users.Messages.Send(me) c.Delete(INBOX)" }`, ""},
		{"package functions named like forbidden methods are exempt", `
func f() { io.Copy(w, r); slices.Delete(s, 0, 1); slices.Insert(s, 0, 1) }`, ""},
		{"draft append is allowed", `
func f() { c.Append("Drafts", n, nil); d.svc.Users.Drafts.Create("me", dr) }`, ""},
		{"body fetch with Peek is allowed", `
func (d *D) GetMessage() { _ = &imap.FetchItemBodySection{Peek: true} }`, ""},
		{"binary fetch with Peek is allowed", `
func (d *D) GetMessage() { _ = &imap.FetchItemBinarySection{Part: []int{1}, Peek: true} }`, ""},
		{"non-peek body fetch inside MarkRead is allowed", `
func (d *D) MarkRead() { _ = &imap.FetchItemBodySection{} }`, ""},

		{"body fetch without Peek", `
func (d *D) GetMessage() { _ = &imap.FetchItemBodySection{} }`, "FetchItemBodySection literal without Peek: true"},
		{"body fetch with Peek: false", `
func (d *D) GetMessage() { _ = &imap.FetchItemBodySection{Peek: false} }`, "FetchItemBodySection literal without Peek: true"},
		{"body fetch with Peek from a variable", `
func (d *D) GetMessage() { _ = &imap.FetchItemBodySection{Peek: peek} }`, "FetchItemBodySection literal without Peek: true"},
		{"binary fetch without Peek", `
func f() { _ = &imap.FetchItemBinarySection{Part: []int{1}} }`, "FetchItemBinarySection literal without Peek: true"},
		{"aliased import does not hide a non-peek fetch", `
func f() { _ = &goimap.FetchItemBodySection{} }`, "FetchItemBodySection literal without Peek: true"},
		{"IMAP move", `func f() { c.Move(uids, "Trash") }`, ".Move"},
		{"IMAP copy", `func f() { c.Copy(uids, "Trash") }`, ".Copy"},
		{"IMAP expunge", `func f() { c.Expunge() }`, ".Expunge"},
		{"IMAP UID expunge", `func f() { c.UIDExpunge(uids) }`, ".UIDExpunge"},
		{"IMAP close-and-expunge", `func f() { c.UnselectAndExpunge() }`, ".UnselectAndExpunge"},
		{"IMAP mailbox delete", `func f() { c.Delete("Old") }`, ".Delete"},
		{"method value is caught too", `func f() { g := c.Move; _ = g }`, ".Move"},
		{"Gmail send", `func f() { d.svc.Users.Messages.Send("me", m) }`, ".Send"},
		{"Gmail draft send", `func f() { d.svc.Users.Drafts.Send("me", dr) }`, ".Send"},
		{"Gmail trash", `func f() { d.svc.Users.Messages.Trash("me", id) }`, ".Trash"},
		{"Gmail delete", `func f() { d.svc.Users.Messages.Delete("me", id) }`, ".Delete"},
		{"Gmail batch modify", `func f() { d.svc.Users.Messages.BatchModify("me", r) }`, ".BatchModify"},
		{"Gmail threads", `func f() { d.svc.Users.Threads.List("me") }`, ".Threads"},
		{"Gmail settings", `func f() { d.svc.Users.Settings.Filters.List("me") }`, ".Settings"},
		{"\\Deleted flag", `func f() { _ = imap.FlagDeleted }`, "imap.FlagDeleted"},
		{"aliased import does not hide \\Deleted", `
func f() { _ = goimap.FlagDeleted }`, "imap.FlagDeleted"},
		{"STORE outside MarkRead", `func (d *D) GetMessage() { c.Store(uids, sf, nil) }`, ".Store outside MarkRead"},
		{"Gmail modify outside MarkRead", `func (d *D) GetMessage() { d.svc.Users.Messages.Modify("me", id, r) }`, ".Modify outside MarkRead"},
		{"MarkRead must be a method", `func MarkRead() { c.Store(uids, sf, nil) }`, ".Store outside MarkRead"},
		{"\\Seen set on append", `
func f() { c.Append("Drafts", n, &imap.AppendOptions{Flags: []imap.Flag{imap.FlagSeen}}) }`, "imap.FlagSeen outside MarkRead"},
		{"MarkRead removing \\Seen", `
func (d *D) MarkRead() { c.Store(uids, &imap.StoreFlags{Op: imap.StoreFlagsDel, Flags: []imap.Flag{imap.FlagSeen}}, nil) }`, "imap.StoreFlagsDel"},
		{"MarkRead setting another flag", `
func (d *D) MarkRead() { c.Store(uids, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagFlagged}}, nil) }`, "imap.FlagFlagged in MarkRead"},
		{"MarkRead adding UNREAD", `
func (d *D) MarkRead() { d.svc.Users.Messages.Modify("me", id, &gmail.ModifyMessageRequest{AddLabelIds: []string{"UNREAD"}}) }`, "ModifyMessageRequest must be exactly"},
		{"MarkRead removing another label", `
func (d *D) MarkRead() { d.svc.Users.Messages.Modify("me", id, &gmail.ModifyMessageRequest{RemoveLabelIds: []string{"INBOX"}}) }`, "ModifyMessageRequest must be exactly"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := header + tc.body + "\n"
			if strings.Contains(tc.body, "goimap.") {
				src = strings.Replace(src, `"github.com/emersion/go-imap/v2"`, `goimap "github.com/emersion/go-imap/v2"`, 1)
			}
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path.Join("fixture", "src.go"), src, parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("parse fixture: %v\n%s", err, src)
			}
			got := scanWriteSurface(fset, file).violations
			if tc.want == "" {
				if len(got) != 0 {
					t.Fatalf("want no violations, got:\n%s", formatViolations(got))
				}
				return
			}
			for _, v := range got {
				if strings.Contains(v.what, tc.want) {
					return
				}
			}
			t.Fatalf("want a violation containing %q, got:\n%s", tc.want, formatViolations(got))
		})
	}
}
