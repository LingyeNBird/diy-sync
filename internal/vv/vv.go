// Package vv implements version vectors: which device changed a file, and
// how often. Comparing two vectors tells whether one version descends from
// the other or whether both sides changed the file independently.
package vv

import (
	"maps"
	"time"
)

// Vector maps a device ID to that device's change counter.
type Vector map[string]uint64

// Ordering is the result of comparing two vectors.
type Ordering int

const (
	Equal      Ordering = iota
	Greater             // the receiver includes every change of the other, and more
	Lesser              // the other includes every change of the receiver, and more
	Concurrent          // each has changes the other lacks
)

func (o Ordering) String() string {
	return [...]string{"equal", "greater", "lesser", "concurrent"}[o]
}

// Compare compares v with o.
func (v Vector) Compare(o Vector) Ordering {
	gt, lt := false, false
	for id, n := range v {
		if m := o[id]; n > m {
			gt = true
		} else if n < m {
			lt = true
		}
	}
	for id, m := range o {
		if _, ok := v[id]; !ok && m > 0 {
			lt = true
		}
	}
	switch {
	case gt && lt:
		return Concurrent
	case gt:
		return Greater
	case lt:
		return Lesser
	}
	return Equal
}

// Merge returns a new vector holding the larger counter of each device.
func (v Vector) Merge(o Vector) Vector {
	out := maps.Clone(v)
	if out == nil {
		out = Vector{}
	}
	for id, m := range o {
		if m > out[id] {
			out[id] = m
		}
	}
	return out
}

// Bump returns a copy of v with a new change by device id. The counter is at
// least the current Unix time, so it keeps growing even if a device's index
// is lost and rebuilt (the same rule Syncthing uses).
func (v Vector) Bump(id string) Vector {
	out := maps.Clone(v)
	if out == nil {
		out = Vector{}
	}
	next := out[id] + 1
	if now := uint64(time.Now().Unix()); now > next {
		next = now
	}
	out[id] = next
	return out
}
