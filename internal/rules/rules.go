// Package rules matches paths against a sync folder's scope rules.
//
// It must give exactly the same results as the panel's matcher
// (web/src/lib/rules.ts). Both are checked against testdata/cases.json.
//
// Semantics (docs/requirements.md §4.1):
//   - One pattern per line. Blank lines and lines starting with '#' are ignored.
//   - A leading '!' turns an include rule into an exclude rule.
//   - A trailing '/' makes the pattern match folders only (and so their subtree).
//   - A pattern without any other '/' matches a name at any depth ("*.log").
//     A pattern containing '/' is anchored at the sync-folder root.
//   - Wildcards: '*' (within one name), '**' (any number of folders), '?', '[abc]', '[!abc]'.
//   - A path is in scope when the LAST rule matching it or one of its parent
//     folders is an include rule.
package rules

import (
	"fmt"
	"regexp"
	"strings"
)

// Rule is one compiled rule line.
type Rule struct {
	Index   int    // line index in the rule list
	Source  string // the trimmed line
	Negate  bool
	DirOnly bool
	re      *regexp.Regexp
}

// Set is a compiled, ordered rule list. A nil *Set puts everything in scope.
type Set struct {
	rules []Rule
}

func globToRegexp(glob string) string {
	var b strings.Builder
	for i := 0; i < len(glob); i++ {
		c := glob[i]
		switch {
		case c == '*':
			if i+1 < len(glob) && glob[i+1] == '*' {
				atSegStart := i == 0 || glob[i-1] == '/'
				if i+2 < len(glob) && glob[i+2] == '/' && atSegStart {
					b.WriteString("(?:.*/)?")
					i += 2
				} else {
					b.WriteString(".*")
					i++
				}
			} else {
				b.WriteString("[^/]*")
			}
		case c == '?':
			b.WriteString("[^/]")
		case c == '[':
			end := strings.IndexByte(glob[i+1:], ']')
			if end == -1 {
				b.WriteString(`\[`)
				continue
			}
			cls := glob[i+1 : i+1+end]
			if strings.HasPrefix(cls, "!") {
				cls = "^" + cls[1:]
			}
			b.WriteString("[" + strings.ReplaceAll(cls, `\`, `\\`) + "]")
			i += end + 1
		case strings.IndexByte(`\^$.|+(){}`, c) >= 0:
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// Compile compiles rule lines. Invalid patterns are reported with their line number.
func Compile(lines []string) (*Set, error) {
	s := &Set{}
	for index, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		src := line
		negate := false
		if strings.HasPrefix(line, "!") {
			negate = true
			line = line[1:]
		}
		dirOnly := false
		if strings.HasSuffix(line, "/") {
			dirOnly = true
			line = strings.TrimRight(line, "/")
		}
		anchored := strings.Contains(line, "/")
		line = strings.TrimLeft(line, "/")
		if line == "" {
			continue
		}
		body := globToRegexp(line)
		expr := "^" + body + "$"
		if !anchored {
			expr = "^(?:.*/)?" + body + "$"
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			return nil, fmt.Errorf("rule line %d %q: %w", index+1, src, err)
		}
		s.rules = append(s.rules, Rule{Index: index, Source: src, Negate: negate, DirOnly: dirOnly, re: re})
	}
	return s, nil
}

// Deciding returns the rule that decides path (slash-separated, relative to
// the sync folder), or nil when no rule matches.
func (s *Set) Deciding(path string, isDir bool) *Rule {
	if s == nil {
		return nil
	}
	parts := strings.Split(path, "/")
	var decided *Rule
	for i := range s.rules {
		r := &s.rules[i]
		for n := 1; n <= len(parts); n++ {
			dir := n < len(parts) || isDir
			if r.DirOnly && !dir {
				continue
			}
			if r.re.MatchString(strings.Join(parts[:n], "/")) {
				decided = r
				break
			}
		}
	}
	return decided
}

// InScope reports whether path takes part in sync. A nil Set includes everything.
func (s *Set) InScope(path string, isDir bool) bool {
	if s == nil {
		return true
	}
	r := s.Deciding(path, isDir)
	return r != nil && !r.Negate
}

// Scope is a folder's full scope: its rules, minus excluded paths (manual
// exclusions and auto-exclusions such as symlinks). An excluded path and
// everything under it is out of scope, whatever the rules say.
// A nil *Scope includes everything.
type Scope struct {
	Rules    *Set
	excluded map[string]bool
}

// NewScope builds a scope from rule lines and excluded paths.
func NewScope(lines, excluded []string) (*Scope, error) {
	rs, err := Compile(lines)
	if err != nil {
		return nil, err
	}
	s := &Scope{Rules: rs, excluded: map[string]bool{}}
	for _, p := range excluded {
		s.excluded[p] = true
	}
	return s, nil
}

// Excluded reports whether path or one of its parents is excluded.
func (s *Scope) Excluded(path string) bool {
	if s == nil || len(s.excluded) == 0 {
		return false
	}
	for p := path; ; {
		if s.excluded[p] {
			return true
		}
		i := strings.LastIndexByte(p, '/')
		if i < 0 {
			return false
		}
		p = p[:i]
	}
}

// InScope reports whether path takes part in sync.
func (s *Scope) InScope(path string, isDir bool) bool {
	if s == nil {
		return true
	}
	return !s.Excluded(path) && s.Rules.InScope(path, isDir)
}
