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
//
// Outside MarkRead, the scan covers these ways of writing a non-peek fetch
// section or a \Seen flag:
//   - a section literal, named or with its type elided inside a slice, array
//     or map literal ([]*imap.FetchItemBodySection{{}}), without Peek: true;
//   - a zero-valued section: new(imap.FetchItemBodySection), a var
//     declaration without a value, make([]imap.FetchItemBodySection, n);
//   - an assignment to a field named Peek of anything but true;
//   - imap.FlagSeen, or a string literal spelling \Seen in any case, other
//     than as a SearchCriteria filter (\Deleted is flagged everywhere);
//   - a mailbox opened by a two-argument .Select call whose options are not
//     the literal &imap.SelectOptions{ReadOnly: true} (EXAMINE).
//
// What a syntactic scan cannot see: a section or flag built from values the
// code does not spell (a string assembled at runtime, a flag read from
// config, a section returned by another package or reached through
// reflection), a zero section inside a struct field of a locally declared
// type, or Peek cleared through a pointer to the field (p := &s.Peek). The
// guarantee does not rest on the scan alone: the read paths open their
// mailbox with EXAMINE, so the server refuses to set \Seen there, and
// TestIMAPHarness_ReadPathsExamine and TestGetMessage_LeavesSeenUnset in
// providers/icloud check that at runtime.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path"
	"path/filepath"
	"regexp"
	"slices"
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
	case *ast.CallExpr:
		s.checkCall(n)
	case *ast.ValueSpec:
		s.checkZeroSectionVar(n)
	case *ast.AssignStmt:
		s.checkPeekAssign(n)
	case *ast.BasicLit:
		s.checkFlagString(n)
	case *ast.CompositeLit:
		s.checkElidedSections(n)
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

// sectionType reports whether e names imap.FetchItemBodySection or
// imap.FetchItemBinarySection, returning the name.
func (s *surfaceScanner) sectionType(e ast.Expr) (string, bool) {
	p, name, ok := s.pkgSelector(e)
	if !ok || p != imapImportPath || (name != "FetchItemBodySection" && name != "FetchItemBinarySection") {
		return "", false
	}
	return name, true
}

// sectionElem reports whether e is a fetch section type or a pointer to one,
// the element types a section slice, array or map can hold.
func (s *surfaceScanner) sectionElem(e ast.Expr) (string, bool) {
	if star, ok := e.(*ast.StarExpr); ok {
		e = star.X
	}
	return s.sectionType(e)
}

// checkElidedSections checks the elements of a slice, array or map literal
// whose element type is elided ([]*imap.FetchItemBodySection{{}}): those
// inner literals have no Type of their own, so checkPeek would never see
// them. Nested containers ([][]T{{{}}}) are followed down.
func (s *surfaceScanner) checkElidedSections(lit *ast.CompositeLit) {
	var elem ast.Expr
	switch t := lit.Type.(type) {
	case *ast.ArrayType:
		elem = t.Elt
	case *ast.MapType:
		elem = t.Value
	default:
		return
	}
	s.checkElidedElems(lit.Elts, elem)
}

func (s *surfaceScanner) checkElidedElems(elts []ast.Expr, elem ast.Expr) {
	for _, elt := range elts {
		if kv, ok := elt.(*ast.KeyValueExpr); ok {
			elt = kv.Value // keyed slice/array element or map value
		}
		inner, ok := elt.(*ast.CompositeLit)
		if !ok || inner.Type != nil {
			continue // a named literal is checked on its own visit
		}
		if name, ok := s.sectionElem(elem); ok {
			s.checkPeek(inner, name)
			continue
		}
		switch t := elem.(type) {
		case *ast.ArrayType:
			s.checkElidedElems(inner.Elts, t.Elt)
		case *ast.MapType:
			s.checkElidedElems(inner.Elts, t.Value)
		}
	}
}

// checkCall flags new and make calls that produce zero-valued fetch
// sections, whose Peek is false, and a mailbox opened read-write.
func (s *surfaceScanner) checkCall(call *ast.CallExpr) {
	if s.inMarkRead() {
		return
	}
	if fn, ok := call.Fun.(*ast.Ident); ok && len(call.Args) > 0 {
		switch fn.Name {
		case "new":
			if name, ok := s.sectionType(call.Args[0]); ok {
				s.report(call, "new(imap.%s) outside %s (a zero section has Peek: false and sets \\Seen)", name, markReadMethod)
			}
		case "make":
			if arr, ok := call.Args[0].(*ast.ArrayType); ok {
				if name, ok := s.sectionType(arr.Elt); ok {
					s.report(call, "make([]imap.%s) outside %s (zero sections have Peek: false and set \\Seen)", name, markReadMethod)
				}
			}
		}
		return
	}
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Select" && len(call.Args) == 2 {
		if !s.isExamineOptions(call.Args[1]) {
			s.report(call, ".Select outside %s without &imap.SelectOptions{ReadOnly: true} (a read path must EXAMINE)", markReadMethod)
		}
	}
}

// isExamineOptions reports whether e is the literal
// &imap.SelectOptions{ReadOnly: true}, the only read-only spelling accepted.
func (s *surfaceScanner) isExamineOptions(e ast.Expr) bool {
	addr, ok := e.(*ast.UnaryExpr)
	if !ok || addr.Op != token.AND {
		return false
	}
	lit, ok := addr.X.(*ast.CompositeLit)
	if !ok {
		return false
	}
	if p, name, ok := s.pkgSelector(lit.Type); !ok || p != imapImportPath || name != "SelectOptions" {
		return false
	}
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "ReadOnly" {
			val, ok := kv.Value.(*ast.Ident)
			return ok && val.Name == "true"
		}
	}
	return false
}

// checkZeroSectionVar flags var s imap.FetchItemBodySection (and arrays of
// sections) declared without a value: the zero value has Peek: false.
func (s *surfaceScanner) checkZeroSectionVar(spec *ast.ValueSpec) {
	if len(spec.Values) > 0 || spec.Type == nil || s.inMarkRead() {
		return
	}
	t := spec.Type
	if arr, ok := t.(*ast.ArrayType); ok && arr.Len != nil {
		t = arr.Elt // [n]T holds n zero sections; []T is nil and holds none
	}
	if name, ok := s.sectionType(t); ok {
		s.report(spec, "zero-valued imap.%s declared outside %s (Peek: false sets \\Seen)", name, markReadMethod)
	}
}

// checkPeekAssign requires every assignment to a field named Peek outside
// MarkRead to assign the literal true.
func (s *surfaceScanner) checkPeekAssign(as *ast.AssignStmt) {
	if s.inMarkRead() {
		return
	}
	for i, lhs := range as.Lhs {
		sel, ok := lhs.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Peek" {
			continue
		}
		if len(as.Rhs) == len(as.Lhs) {
			if val, ok := as.Rhs[i].(*ast.Ident); ok && val.Name == "true" {
				continue
			}
		}
		s.report(lhs, ".Peek assigned something other than true outside %s (a non-peek fetch sets \\Seen)", markReadMethod)
	}
}

// checkFlagString applies the imap.FlagSeen and imap.FlagDeleted rules to
// flags spelled as string literals ("\\Seen"). IMAP flags are
// case-insensitive, so the comparison is too.
func (s *surfaceScanner) checkFlagString(lit *ast.BasicLit) {
	if lit.Kind != token.STRING {
		return
	}
	v, err := strconv.Unquote(lit.Value)
	if err != nil {
		return // unreachable for a file go/parser accepted
	}
	switch {
	case strings.EqualFold(v, `\Deleted`):
		s.report(lit, "flag string %s (marks a message for expunge)", lit.Value)
	case strings.EqualFold(v, `\Seen`):
		if !s.inMarkRead() && !s.inSearchCriterion() {
			s.report(lit, "flag string %s outside %s and outside a search criterion", lit.Value, markReadMethod)
		}
	}
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
		{"elided body section with Peek is allowed", `
func (d *D) GetMessage() { _ = &imap.FetchOptions{BodySection: []*imap.FetchItemBodySection{{Peek: true}}} }`, ""},
		{"Peek assigned true is allowed", `
func (d *D) GetMessage() { s := &imap.FetchItemBodySection{Peek: true}; s.Peek = true }`, ""},
		{"EXAMINE is allowed", `
func (d *D) GetMessage() { c.Select("INBOX", &imap.SelectOptions{ReadOnly: true}) }`, ""},
		{"SELECT inside MarkRead is allowed", `
func (d *D) MarkRead() { c.Select("INBOX", nil) }`, ""},
		{"\\Seen string as a search filter is allowed", `
func f() { criteria.NotFlag = append(criteria.NotFlag, imap.Flag("\\Seen")) }`, ""},

		{"elided body section without Peek", `
func (d *D) GetMessage() { _ = &imap.FetchOptions{BodySection: []*imap.FetchItemBodySection{{}}} }`, "FetchItemBodySection literal without Peek: true"},
		{"elided binary section in an array without Peek", `
func f() { _ = [1]imap.FetchItemBinarySection{{Part: []int{1}}} }`, "FetchItemBinarySection literal without Peek: true"},
		{"elided section as a map value without Peek", `
func f() { _ = map[string]*imap.FetchItemBodySection{"all": {}} }`, "FetchItemBodySection literal without Peek: true"},
		{"elided section in a nested slice without Peek", `
func f() { _ = [][]*imap.FetchItemBodySection{{{Peek: false}}} }`, "FetchItemBodySection literal without Peek: true"},
		{"new body section", `
func (d *D) GetMessage() { s := new(imap.FetchItemBodySection); _ = s }`, "new(imap.FetchItemBodySection)"},
		{"make of body sections", `
func f() { _ = make([]imap.FetchItemBodySection, 1) }`, "make([]imap.FetchItemBodySection)"},
		{"zero-valued section var", `
func (d *D) GetMessage() { var s imap.FetchItemBodySection; _ = &s }`, "zero-valued imap.FetchItemBodySection"},
		{"zero-valued section array var", `
func f() { var s [2]imap.FetchItemBinarySection; _ = s }`, "zero-valued imap.FetchItemBinarySection"},
		{"Peek cleared after a peek literal", `
func (d *D) GetMessage() { s := &imap.FetchItemBodySection{Peek: true}; s.Peek = false }`, ".Peek assigned something other than true"},
		{"Peek assigned from a variable", `
func f() { s.Peek = peek }`, ".Peek assigned something other than true"},
		{"\\Seen as a string on append", `
func f() { c.Append("Drafts", n, &imap.AppendOptions{Flags: []imap.Flag{"\\Seen"}}) }`, "flag string"},
		{"\\Seen as a raw string in another case", "\nfunc f() { _ = []imap.Flag{`\\SEEN`} }", "flag string"},
		{"\\Deleted as a string", `
func f() { _ = imap.Flag("\\Deleted") }`, "flag string"},
		{"SELECT outside MarkRead", `
func (d *D) GetMessage() { c.Select("INBOX", nil) }`, ".Select outside MarkRead"},
		{"SELECT with ReadOnly from a variable", `
func (d *D) GetMessage() { c.Select("INBOX", &imap.SelectOptions{ReadOnly: ro}) }`, ".Select outside MarkRead"},
		{"SELECT with options from a variable", `
func (d *D) GetMessage() { c.Select("INBOX", opts) }`, ".Select outside MarkRead"},
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

// providerImportPrefixes are the provider client libraries the shared codec
// must not import. rfc5322 is outside the write-surface scan above (go-message
// itself has a Header.Copy that the scan would flag), so this check is what
// keeps mailbox operations out of it: a package that cannot reach an IMAP
// client or the Gmail service cannot change a mailbox.
var providerImportPrefixes = []string{
	"github.com/emersion/go-imap",
	"google.golang.org/api/gmail",
}

// TestRFC5322CodecImportsNoProvider scans every .go file under rfc5322/,
// tests included, since a test helper could carry a client in as well.
func TestRFC5322CodecImportsNoProvider(t *testing.T) {
	fset := token.NewFileSet()
	var scanned []string
	var bad []string
	err := filepath.WalkDir("rfc5322", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") {
			return nil
		}
		file, err := parser.ParseFile(fset, p, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		scanned = append(scanned, filepath.ToSlash(p))
		bad = append(bad, providerImports(fset, file)...)
		return nil
	})
	if err != nil {
		t.Fatalf("scan rfc5322/: %v", err)
	}
	for _, want := range []string{"rfc5322/decode.go", "rfc5322/compose.go"} {
		if !slices.Contains(scanned, want) {
			t.Fatalf("scan did not reach %s (scanned: %v)", want, scanned)
		}
	}
	if len(bad) > 0 {
		t.Fatalf("the shared mail codec imports a provider client:\n  %s", strings.Join(bad, "\n  "))
	}
}

func providerImports(fset *token.FileSet, file *ast.File) []string {
	var bad []string
	for _, imp := range file.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue // unreachable for a file go/parser accepted
		}
		for _, prefix := range providerImportPrefixes {
			if p == prefix || strings.HasPrefix(p, prefix+"/") {
				bad = append(bad, fmt.Sprintf("%s: imports %s", fset.Position(imp.Pos()), p))
			}
		}
	}
	return bad
}

// TestRFC5322ImportCheck proves providerImports flags what it should.
func TestRFC5322ImportCheck(t *testing.T) {
	cases := map[string]bool{
		`"github.com/emersion/go-message/mail"`:       false,
		`"google.golang.org/api/option"`:              false,
		`"github.com/emersion/go-imap/v2"`:            true,
		`"github.com/emersion/go-imap/v2/imapclient"`: true,
		`imap "github.com/emersion/go-imap"`:          true,
		`"google.golang.org/api/gmail/v1"`:            true,
		`"github.com/emersion/go-imapextra"`:          false,
	}
	for imp, wantBad := range cases {
		src := "package fixture\n\nimport " + imp + "\n"
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "fixture.go", src, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", imp, err)
		}
		if got := len(providerImports(fset, file)) > 0; got != wantBad {
			t.Errorf("import %s: flagged = %v, want %v", imp, got, wantBad)
		}
	}
}
