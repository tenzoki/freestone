package files

import (
	"bufio"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// Rule represents a single allow (+) or deny (-) rule.
type Rule struct {
	Allow bool
	Path  string // absolute, cleaned
}

// RuleSet holds an ordered list of rules parsed from a rules file.
type RuleSet struct {
	rules []Rule
}

// ParseRules reads a rules file and returns a RuleSet.
// Each non-empty, non-comment line must start with '+' or '-' followed by
// a space and an absolute path. Any other line is a syntax error.
func ParseRules(r io.Reader) (*RuleSet, error) {
	var rules []Rule
	scanner := bufio.NewScanner(r)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if len(line) < 3 {
			return nil, fmt.Errorf("rules: line %d: too short: %q", lineNum, line)
		}

		prefix := line[0]
		if prefix != '+' && prefix != '-' {
			return nil, fmt.Errorf("rules: line %d: must start with '+' or '-': %q", lineNum, line)
		}
		if line[1] != ' ' {
			return nil, fmt.Errorf("rules: line %d: expected space after '%c': %q", lineNum, prefix, line)
		}

		path := strings.TrimSpace(line[2:])
		if !filepath.IsAbs(path) {
			return nil, fmt.Errorf("rules: line %d: path must be absolute: %q", lineNum, path)
		}

		rules = append(rules, Rule{
			Allow: prefix == '+',
			Path:  filepath.Clean(path),
		})
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("rules: read error: %w", err)
	}

	return &RuleSet{rules: rules}, nil
}

// Allowed checks whether absPath is permitted by the ruleset.
// The path is cleaned before matching. Rules are evaluated in order;
// the last matching rule wins. Default (no match) is deny.
func (rs *RuleSet) Allowed(absPath string) bool {
	cleaned := filepath.Clean(absPath)
	if !filepath.IsAbs(cleaned) {
		return false
	}

	allowed := false
	matched := false

	for _, rule := range rs.rules {
		if pathPrefixMatch(rule.Path, cleaned) {
			allowed = rule.Allow
			matched = true
		}
	}

	if !matched {
		return false
	}
	return allowed
}

// pathPrefixMatch returns true if target is equal to prefix or is a child
// path of prefix (by path components, not just string prefix).
func pathPrefixMatch(prefix, target string) bool {
	if prefix == target {
		return true
	}
	// Ensure the prefix is a path-component prefix, not just a string prefix.
	// "/Users/kai" should match "/Users/kai/file" but not "/Users/kaizen".
	pfx := prefix
	if !strings.HasSuffix(pfx, "/") {
		pfx += "/"
	}
	return strings.HasPrefix(target, pfx)
}
