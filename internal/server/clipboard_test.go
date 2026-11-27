package server

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func makeTestPNG(t *testing.T, w, h int, c color.NRGBA) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func makeTestJPEG(t *testing.T, w, h int, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func makeDIB24(w, h int, pixels [][]color.NRGBA) []byte {
	stride := ((w*24 + 31) / 32) * 4
	buf := make([]byte, 40+stride*h)
	binary.LittleEndian.PutUint32(buf[0:4], 40)
	binary.LittleEndian.PutUint32(buf[4:8], uint32(int32(w)))
	binary.LittleEndian.PutUint32(buf[8:12], uint32(int32(h))) // bottom-up
	binary.LittleEndian.PutUint16(buf[12:14], 1)
	binary.LittleEndian.PutUint16(buf[14:16], 24)
	binary.LittleEndian.PutUint32(buf[16:20], biRGB)
	for y := 0; y < h; y++ {
		row := buf[40+(h-1-y)*stride:]
		for x := 0; x < w; x++ {
			c := pixels[y][x]
			row[x*3+0] = c.B
			row[x*3+1] = c.G
			row[x*3+2] = c.R
		}
	}
	return buf
}

func TestClipboardStoreDedupAndEviction(t *testing.T) {
	st := newClipboardStore()
	st.maxUnselected = 3

	redPNG := makeTestPNG(t, 4, 4, color.NRGBA{R: 255, G: 0, B: 0, A: 255})
	redDIB := makeDIB24(4, 4, func() [][]color.NRGBA {
		rows := make([][]color.NRGBA, 4)
		for y := range rows {
			rows[y] = make([]color.NRGBA, 4)
			for x := range rows[y] {
				rows[y][x] = color.NRGBA{R: 255, G: 0, B: 0, A: 255}
			}
		}
		return rows
	}())

	first, added, err := st.addImage(redPNG, "clipboard", "", false)
	if err != nil || !added {
		t.Fatalf("first add: added=%v err=%v", added, err)
	}
	// Identical pixels arriving as CF_DIB right after PNG must deduplicate.
	dup, added, err := st.addDIB(redDIB)
	if err != nil || added || dup.ID != first.ID {
		t.Fatalf("dib dedup failed: id=%s added=%v err=%v", dup.ID, added, err)
	}

	// Pin the first image so automatic eviction protects it.
	sel := true
	note := "reference"
	if _, err := st.update(first.ID, &sel, &note); err != nil {
		t.Fatal(err)
	}

	// Add 4 more distinct unselected images (maxUnselected=3).
	var ids []string
	for i := 1; i <= 4; i++ {
		it, added, err := st.addImage(makeTestPNG(t, 4, 4, color.NRGBA{R: uint8(i * 40), G: 10, B: 20, A: 255}), "clipboard", "", false)
		if err != nil || !added {
			t.Fatalf("add %d: added=%v err=%v", i, added, err)
		}
		ids = append(ids, it.ID)
	}

	list := st.list()
	// Expect 3 unselected (ids[3], ids[2], ids[1]) + 1 selected (first) = 4 items; ids[0] evicted.
	if len(list.Items) != 4 {
		t.Fatalf("got %d items, want 4: %+v", len(list.Items), list.Items)
	}
	foundFirst := false
	for _, it := range list.Items {
		if it.ID == ids[0] {
			t.Fatalf("oldest unselected %s should have been evicted", ids[0])
		}
		if it.ID == first.ID {
			foundFirst = true
			if !it.Selected || it.Note != "reference" {
				t.Fatalf("pinned item lost state: %+v", it)
			}
		}
	}
	if !foundFirst {
		t.Fatal("selected item was evicted")
	}

	// Pause skips automatic clipboard captures, but still allows explicit uploads.
	st.setPaused(true)
	if _, added, err := st.addImage(makeTestPNG(t, 4, 4, color.NRGBA{R: 1, G: 2, B: 3, A: 255}), "clipboard", "", false); err != nil || added {
		t.Fatalf("paused clipboard capture should be ignored: added=%v err=%v", added, err)
	}
	up, added, err := st.addImage(makeTestJPEG(t, 8, 6, color.RGBA{R: 10, G: 200, B: 30, A: 255}), "upload", "uploaded jpg", true)
	if err != nil || !added || !up.Selected || up.Width != 8 || up.Height != 6 {
		t.Fatalf("upload while paused failed: item=%+v added=%v err=%v", up, added, err)
	}
	_, pngData, ok := st.get(up.ID)
	if !ok {
		t.Fatal("uploaded item missing")
	}
	if _, err := png.Decode(bytes.NewReader(pngData)); err != nil {
		t.Fatalf("uploaded jpeg not normalized to valid png: %v", err)
	}

	// Clear unselected keeps only selected items (first + up).
	st.clear(true)
	if count, selCount, paused := st.summary(); count != 2 || selCount != 2 || !paused {
		t.Fatalf("after clear(unselected): count=%d sel=%d paused=%v", count, selCount, paused)
	}
	st.unselectAll()
	if _, selCount, _ := st.summary(); selCount != 0 {
		t.Fatalf("after unselectAll: sel=%d", selCount)
	}
	if !st.delete(first.ID) || st.delete(first.ID) {
		t.Fatal("delete return value mismatch")
	}
	st.clear(false)
	if count, _, _ := st.summary(); count != 0 {
		t.Fatalf("after clear(all): count=%d", count)
	}
}

func TestDecodeDIBVariants(t *testing.T) {
	// 1. 32-bit bottom-up BI_RGB with all-zero alpha → must become opaque (A=255).
	dib32ZeroAlpha := make([]byte, 40+2*1*4)
	binary.LittleEndian.PutUint32(dib32ZeroAlpha[0:4], 40)
	binary.LittleEndian.PutUint32(dib32ZeroAlpha[4:8], 2)
	binary.LittleEndian.PutUint32(dib32ZeroAlpha[8:12], 1)
	binary.LittleEndian.PutUint16(dib32ZeroAlpha[12:14], 1)
	binary.LittleEndian.PutUint16(dib32ZeroAlpha[14:16], 32)
	binary.LittleEndian.PutUint32(dib32ZeroAlpha[16:20], biRGB)
	// Pixel 0: B=10, G=20, R=30, A=0; Pixel 1: B=40, G=50, R=60, A=0.
	copy(dib32ZeroAlpha[40:], []byte{10, 20, 30, 0, 40, 50, 60, 0})
	img, err := decodeDIB(dib32ZeroAlpha)
	if err != nil {
		t.Fatalf("32-bit zero-alpha dib: %v", err)
	}
	if got := img.NRGBAAt(0, 0); got != (color.NRGBA{R: 30, G: 20, B: 10, A: 255}) {
		t.Fatalf("pixel 0 = %+v, want {30 20 10 255}", got)
	}

	// 2. 32-bit top-down BITMAPV5HEADER with real alpha channel preserved.
	dibV5 := make([]byte, 124+2*4)
	binary.LittleEndian.PutUint32(dibV5[0:4], 124)
	binary.LittleEndian.PutUint32(dibV5[4:8], 1)
	neg2 := int32(-2)
	binary.LittleEndian.PutUint32(dibV5[8:12], uint32(neg2)) // top-down, height 2
	binary.LittleEndian.PutUint16(dibV5[12:14], 1)
	binary.LittleEndian.PutUint16(dibV5[14:16], 32)
	binary.LittleEndian.PutUint32(dibV5[16:20], biBitfields)
	binary.LittleEndian.PutUint32(dibV5[40:44], 0x00FF0000)
	binary.LittleEndian.PutUint32(dibV5[44:48], 0x0000FF00)
	binary.LittleEndian.PutUint32(dibV5[48:52], 0x000000FF)
	binary.LittleEndian.PutUint32(dibV5[52:56], 0xFF000000)
	// Row 0 (top): RGBA(100,150,200,128); Row 1 (bottom): RGBA(1,2,3,0)
	binary.LittleEndian.PutUint32(dibV5[124:128], 0x806496C8)
	binary.LittleEndian.PutUint32(dibV5[128:132], 0x00010203)
	imgV5, err := decodeDIB(dibV5)
	if err != nil {
		t.Fatalf("v5 dib: %v", err)
	}
	if got := imgV5.NRGBAAt(0, 0); got != (color.NRGBA{R: 100, G: 150, B: 200, A: 128}) {
		t.Fatalf("v5 top pixel = %+v", got)
	}
	if got := imgV5.NRGBAAt(0, 1); got != (color.NRGBA{R: 1, G: 2, B: 3, A: 0}) {
		t.Fatalf("v5 bottom pixel = %+v", got)
	}

	// 3. 16-bit RGB565 BI_BITFIELDS (40B header + 12B masks).
	dib16 := make([]byte, 40+12+4) // 1x1 padded to 4-byte stride
	binary.LittleEndian.PutUint32(dib16[0:4], 40)
	binary.LittleEndian.PutUint32(dib16[4:8], 1)
	binary.LittleEndian.PutUint32(dib16[8:12], 1)
	binary.LittleEndian.PutUint16(dib16[12:14], 1)
	binary.LittleEndian.PutUint16(dib16[14:16], 16)
	binary.LittleEndian.PutUint32(dib16[16:20], biBitfields)
	binary.LittleEndian.PutUint32(dib16[40:44], 0xF800)
	binary.LittleEndian.PutUint32(dib16[44:48], 0x07E0)
	binary.LittleEndian.PutUint32(dib16[48:52], 0x001F)
	binary.LittleEndian.PutUint16(dib16[52:54], 0xF800) // pure red
	img16, err := decodeDIB(dib16)
	if err != nil {
		t.Fatalf("16-bit dib: %v", err)
	}
	if got := img16.NRGBAAt(0, 0); got != (color.NRGBA{R: 255, G: 0, B: 0, A: 255}) {
		t.Fatalf("16-bit pixel = %+v, want red", got)
	}

	// 4. Truncated data must fail cleanly.
	if _, err := decodeDIB([]byte{1, 2, 3}); err == nil {
		t.Fatal("truncated dib accepted")
	}
}

func TestClipboardResizeAndCompressToggle(t *testing.T) {
	// Small image fits within maxEdge and is untouched.
	small := image.NewNRGBA(image.Rect(0, 0, 1200, 800))
	if out, resized := resizeImage(small, 2048); resized || out.Bounds().Dx() != 1200 {
		t.Fatalf("small image should not resize: resized=%v bounds=%v", resized, out.Bounds())
	}

	// Landscape 3000x1500 → 2048x1024; solid color preserved accurately.
	bigPNG := makeTestPNG(t, 3000, 1500, color.NRGBA{R: 42, G: 128, B: 210, A: 255})

	st := newClipboardStore()
	if !st.list().Compress || st.list().MaxEdge != defaultClipboardMaxEdge {
		t.Fatalf("expected compress enabled by default: %+v", st.list())
	}
	scaled, added, err := st.addImage(bigPNG, "clipboard", "", false)
	if err != nil || !added {
		t.Fatalf("add big png: added=%v err=%v", added, err)
	}
	if scaled.Width != 2048 || scaled.Height != 1024 || scaled.OriginalWidth != 3000 || scaled.OriginalHeight != 1500 {
		t.Fatalf("unexpected scaled metadata: %+v", scaled)
	}
	_, pngBytes, ok := st.get(scaled.ID)
	if !ok {
		t.Fatal("scaled item missing")
	}
	decoded, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatalf("decode scaled png: %v", err)
	}
	r, g, b, a := decoded.At(100, 100).RGBA()
	if uint8(r>>8) != 42 || uint8(g>>8) != 128 || uint8(b>>8) != 210 || uint8(a>>8) != 255 {
		t.Fatalf("color shifted after resize: rgba=(%d,%d,%d,%d)", r>>8, g>>8, b>>8, a>>8)
	}

	// Turning compress off and re-capturing the exact same 3000x1500 image must
	// store the full-resolution original instead of deduplicating into the scaled one.
	st.setCompress(false)
	full, added, err := st.addImage(bigPNG, "clipboard", "", false)
	if err != nil || !added {
		t.Fatalf("add uncompressed big png: added=%v err=%v", added, err)
	}
	if full.ID == scaled.ID || full.Width != 3000 || full.Height != 1500 || full.OriginalWidth != 0 || full.OriginalHeight != 0 {
		t.Fatalf("unexpected full-res metadata: %+v", full)
	}
}
