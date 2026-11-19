package vv

import "testing"

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b Vector
		want Ordering
	}{
		{nil, nil, Equal},
		{Vector{"s": 1}, Vector{"s": 1}, Equal},
		{Vector{"s": 2}, Vector{"s": 1}, Greater},
		{Vector{"s": 1}, Vector{"s": 1, "c": 1}, Lesser},
		{Vector{"s": 2, "c": 1}, Vector{"s": 1, "c": 2}, Concurrent},
		{Vector{"s": 1}, Vector{"c": 1}, Concurrent},
		{nil, Vector{"c": 1}, Lesser},
		{Vector{"c": 0}, nil, Equal},
	}
	for _, c := range cases {
		if got := c.a.Compare(c.b); got != c.want {
			t.Errorf("%v vs %v = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestBumpAndMerge(t *testing.T) {
	a := Vector{}.Bump("s")
	b := a.Bump("c")
	if b.Compare(a) != Greater || a.Compare(b) != Lesser {
		t.Fatal("bump should descend")
	}
	x, y := b.Bump("s"), b.Bump("c")
	if x.Compare(y) != Concurrent {
		t.Fatal("independent bumps should be concurrent")
	}
	m := x.Merge(y)
	if m.Compare(x) != Greater || m.Compare(y) != Greater {
		t.Fatal("merge should include both")
	}
	if len(a) != 1 {
		t.Fatal("Bump must not modify the receiver")
	}
}
