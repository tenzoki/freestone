package files

import (
	"strings"
	"testing"
)

func parseRulesString(t *testing.T, s string) *RuleSet {
	t.Helper()
	rs, err := ParseRules(strings.NewReader(s))
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	return rs
}

func TestParseRules_SimpleAllow(t *testing.T) {
	rs := parseRulesString(t, "+ /Users/kai/Documents\n")
	if len(rs.rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rs.rules))
	}
	if !rs.rules[0].Allow || rs.rules[0].Path != "/Users/kai/Documents" {
		t.Errorf("unexpected rule: %+v", rs.rules[0])
	}
}

func TestParseRules_SimpleDeny(t *testing.T) {
	rs := parseRulesString(t, "- /Users/kai/.secret\n")
	if len(rs.rules) != 1 || rs.rules[0].Allow {
		t.Errorf("expected deny rule, got: %+v", rs.rules[0])
	}
}

func TestParseRules_CommentsAndBlanks(t *testing.T) {
	rs := parseRulesString(t, `
# This is a comment
+ /tmp

# Another comment

- /tmp/secret
`)
	if len(rs.rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(rs.rules))
	}
}

func TestParseRules_SyntaxErrorNoSign(t *testing.T) {
	_, err := ParseRules(strings.NewReader("invalid line\n"))
	if err == nil {
		t.Fatal("expected syntax error")
	}
	if !strings.Contains(err.Error(), "must start with") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestParseRules_SyntaxErrorNoSpace(t *testing.T) {
	_, err := ParseRules(strings.NewReader("+/tmp\n"))
	if err == nil {
		t.Fatal("expected syntax error for missing space")
	}
	if !strings.Contains(err.Error(), "expected space") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestParseRules_RelativePath(t *testing.T) {
	_, err := ParseRules(strings.NewReader("+ relative/path\n"))
	if err == nil {
		t.Fatal("expected error for relative path")
	}
	if !strings.Contains(err.Error(), "absolute") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestAllowed_SimpleAllow(t *testing.T) {
	rs := parseRulesString(t, "+ /Users/kai/Documents\n")
	if !rs.Allowed("/Users/kai/Documents") {
		t.Error("expected allowed for exact match")
	}
	if !rs.Allowed("/Users/kai/Documents/file.txt") {
		t.Error("expected allowed for child path")
	}
}

func TestAllowed_SimpleDeny(t *testing.T) {
	rs := parseRulesString(t, "- /Users/kai/.secret\n")
	if rs.Allowed("/Users/kai/.secret") {
		t.Error("expected denied")
	}
	if rs.Allowed("/Users/kai/.secret/key") {
		t.Error("expected denied for child")
	}
}

func TestAllowed_DefaultDeny(t *testing.T) {
	rs := parseRulesString(t, "+ /Users/kai/Documents\n")
	if rs.Allowed("/etc/passwd") {
		t.Error("expected default deny for unmatched path")
	}
}

func TestAllowed_NestedDenyInsideAllow(t *testing.T) {
	rs := parseRulesString(t, `
+ /Users/kai/Documents
- /Users/kai/Documents/.git
`)
	if !rs.Allowed("/Users/kai/Documents/file.txt") {
		t.Error("expected allowed")
	}
	if rs.Allowed("/Users/kai/Documents/.git/config") {
		t.Error("expected denied for nested .git")
	}
	if rs.Allowed("/Users/kai/Documents/.git") {
		t.Error("expected denied for exact .git")
	}
}

func TestAllowed_NestedAllowInsideDeny(t *testing.T) {
	rs := parseRulesString(t, `
+ /Users/kai
- /Users/kai/private
+ /Users/kai/private/public
`)
	if !rs.Allowed("/Users/kai/public.txt") {
		t.Error("expected allowed")
	}
	if rs.Allowed("/Users/kai/private/secret.txt") {
		t.Error("expected denied")
	}
	if !rs.Allowed("/Users/kai/private/public/readme.txt") {
		t.Error("expected allowed for nested override")
	}
}

func TestAllowed_PathTraversal(t *testing.T) {
	rs := parseRulesString(t, "+ /Users/kai/safe\n")
	// ".." traversal should be cleaned and not escape
	if rs.Allowed("/Users/kai/safe/../unsafe") {
		t.Error("expected denied after .. traversal")
	}
	if !rs.Allowed("/Users/kai/safe/sub/../file.txt") {
		t.Error("expected allowed after cleaning")
	}
}

func TestAllowed_NoPartialComponentMatch(t *testing.T) {
	rs := parseRulesString(t, "+ /Users/kai\n")
	// "/Users/kaizen" should NOT match "/Users/kai"
	if rs.Allowed("/Users/kaizen") {
		t.Error("expected denied: partial component should not match")
	}
}

func TestAllowed_EmptyRuleset(t *testing.T) {
	rs := parseRulesString(t, "# empty\n")
	if rs.Allowed("/anything") {
		t.Error("expected default deny with empty ruleset")
	}
}

func TestAllowed_RelativeInputDenied(t *testing.T) {
	rs := parseRulesString(t, "+ /tmp\n")
	if rs.Allowed("relative/path") {
		t.Error("expected denied for relative input path")
	}
}

func TestAllowed_LastMatchWins(t *testing.T) {
	rs := parseRulesString(t, `
- /tmp
+ /tmp
`)
	if !rs.Allowed("/tmp/file") {
		t.Error("expected allowed: last matching rule should win")
	}
}
