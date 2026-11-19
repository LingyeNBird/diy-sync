package main

import (
	"encoding/binary"
	"testing"
)

func TestTrayIcon(t *testing.T) {
	ico := trayIcon(iconRunning, true)
	if binary.LittleEndian.Uint16(ico[2:]) != 1 || binary.LittleEndian.Uint16(ico[4:]) != 5 {
		t.Fatal("bad ico header")
	}
	// The last entry's image ends exactly at the end of the file.
	e := ico[6+16*4:]
	if n, off := binary.LittleEndian.Uint32(e[8:]), binary.LittleEndian.Uint32(e[12:]); int(n+off) != len(ico) {
		t.Fatalf("entry %d+%d, file %d", n, off, len(ico))
	}
	px := render(32, iconPaused, false)
	if px[3] != 0 || px[(16*32+16)*4+3] != 255 {
		t.Fatal("corner should be clear, centre opaque")
	}
}
