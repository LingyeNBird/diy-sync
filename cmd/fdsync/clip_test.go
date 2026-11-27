package main

import (
	"testing"
	"time"

	"fdsync/internal/protocol"
)

func TestSelectClipboardItems(t *testing.T) {
	now := time.Now()
	items := []protocol.ClipboardItem{
		{ID: "clip-0004", CapturedAt: now, Selected: false},
		{ID: "clip-0003", CapturedAt: now.Add(-10 * time.Second), Selected: true},
		{ID: "clip-0002", CapturedAt: now.Add(-20 * time.Second), Selected: false},
		{ID: "clip-0001", CapturedAt: now.Add(-30 * time.Second), Selected: true},
	}

	// Default with selected items: returns selected items and warns about newer unselected.
	sel, err := selectClipboardItems(items, clipSelectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if sel.Mode != "selected" || len(sel.Items) != 2 || sel.Items[0].ID != "clip-0003" || sel.Items[1].ID != "clip-0001" || sel.NewerUnselected != 1 {
		t.Fatalf("default selected = %+v", sel)
	}

	// --latest 2 ignores selection and picks the newest 2.
	sel, err = selectClipboardItems(items, clipSelectOptions{Latest: 2})
	if err != nil || sel.Mode != "latest" || len(sel.Items) != 2 || sel.Items[0].ID != "clip-0004" || sel.Items[1].ID != "clip-0003" {
		t.Fatalf("latest 2 = %+v err=%v", sel, err)
	}

	// Default when nothing is selected: falls back to latest 1.
	unselected := []protocol.ClipboardItem{
		{ID: "clip-0002", CapturedAt: now},
		{ID: "clip-0001", CapturedAt: now.Add(-time.Minute)},
	}
	sel, err = selectClipboardItems(unselected, clipSelectOptions{})
	if err != nil || sel.Mode != "latest" || len(sel.Items) != 1 || sel.Items[0].ID != "clip-0002" {
		t.Fatalf("default fallback = %+v err=%v", sel, err)
	}

	// --selected when nothing is selected returns empty.
	sel, err = selectClipboardItems(unselected, clipSelectOptions{Selected: true})
	if err != nil || sel.Mode != "selected" || len(sel.Items) != 0 {
		t.Fatalf("strict selected = %+v err=%v", sel, err)
	}

	// Explicit IDs preserve requested order and fail on unknown IDs.
	sel, err = selectClipboardItems(items, clipSelectOptions{IDs: []string{"clip-0001", "clip-0004"}})
	if err != nil || sel.Mode != "ids" || len(sel.Items) != 2 || sel.Items[0].ID != "clip-0001" || sel.Items[1].ID != "clip-0004" {
		t.Fatalf("explicit ids = %+v err=%v", sel, err)
	}
	if _, err := selectClipboardItems(items, clipSelectOptions{IDs: []string{"clip-9999"}}); err == nil {
		t.Fatal("expected error for unknown ID")
	}
}
