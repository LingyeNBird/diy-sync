package rules

import (
	"encoding/json"
	"os"
	"testing"
)

// The same cases run against the panel's matcher: web/scripts/check-rules.ts.
type group struct {
	Name  string   `json:"name"`
	Rules []string `json:"rules"`
	Cases []struct {
		Path string `json:"path"`
		Dir  bool   `json:"dir"`
		In   bool   `json:"in"`
		Rule int    `json:"rule"`
	} `json:"cases"`
}

func TestSharedCases(t *testing.T) {
	raw, err := os.ReadFile("testdata/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var groups []group
	if err := json.Unmarshal(raw, &groups); err != nil {
		t.Fatal(err)
	}
	for _, g := range groups {
		s, err := Compile(g.Rules)
		if err != nil {
			t.Fatalf("%s: %v", g.Name, err)
		}
		for _, c := range g.Cases {
			got := s.InScope(c.Path, c.Dir)
			rule := -1
			if r := s.Deciding(c.Path, c.Dir); r != nil {
				rule = r.Index
			}
			if got != c.In || rule != c.Rule {
				t.Errorf("%s: %q dir=%v: in=%v rule=%d, want in=%v rule=%d", g.Name, c.Path, c.Dir, got, rule, c.In, c.Rule)
			}
		}
	}
}

func TestNilSetIncludesEverything(t *testing.T) {
	var s *Set
	if !s.InScope("any/thing", false) {
		t.Fatal("nil set should include everything")
	}
}

func TestScopeExclusions(t *testing.T) {
	s, err := NewScope([]string{"a/"}, []string{"a/link", "a/x/y"})
	if err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]bool{"a/f": true, "a/link": false, "a/link/inner": false, "a/x/y/z": false, "a/x/yy": true, "b/f": false} {
		if got := s.InScope(p, false); got != want {
			t.Errorf("%s: %v, want %v", p, got, want)
		}
	}
	var nilScope *Scope
	if !nilScope.InScope("any", false) {
		t.Fatal("nil scope includes everything")
	}
}
