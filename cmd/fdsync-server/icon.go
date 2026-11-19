package main

import (
	"bytes"
	"encoding/binary"
	"image/color"
)

// Tray icons, drawn here so the exe needs no image files: a rounded square
// with two arrows (running), pause bars (paused), with a red badge when
// there are conflicts. Several sizes in one .ico, as Windows picks by DPI.

type iconKind int

const (
	iconRunning iconKind = iota
	iconPaused
)

var (
	blue  = color.RGBA{0x25, 0x63, 0xeb, 0xff}
	amber = color.RGBA{0xd9, 0x77, 0x06, 0xff}
	red   = color.RGBA{0xdc, 0x26, 0x26, 0xff}
	white = color.RGBA{0xff, 0xff, 0xff, 0xff}
)

type pt struct{ x, y float64 }

func inTri(p, a, b, c pt) bool {
	s := func(p1, p2, p3 pt) float64 { return (p1.x-p3.x)*(p2.y-p3.y) - (p2.x-p3.x)*(p1.y-p3.y) }
	d1, d2, d3 := s(p, a, b), s(p, b, c), s(p, c, a)
	neg := d1 < 0 || d2 < 0 || d3 < 0
	pos := d1 > 0 || d2 > 0 || d3 > 0
	return !(neg && pos)
}

func inRect(p pt, x0, y0, x1, y1 float64) bool {
	return p.x >= x0 && p.x <= x1 && p.y >= y0 && p.y <= y1
}

func inRounded(p pt, r float64) bool {
	cx := min(max(p.x, r), 1-r)
	cy := min(max(p.y, r), 1-r)
	dx, dy := p.x-cx, p.y-cy
	return dx*dx+dy*dy <= r*r
}

// shade says what covers a point (in 0..1 coordinates): 0 nothing, 1 the
// background, 2 the white glyph, 3 the badge.
func shade(p pt, kind iconKind, badge bool) int {
	if badge {
		dx, dy := p.x-0.78, p.y-0.22
		if dx*dx+dy*dy <= 0.22*0.22 {
			return 3
		}
	}
	if !inRounded(p, 0.22) {
		return 0
	}
	switch kind {
	case iconPaused:
		if inRect(p, 0.30, 0.25, 0.43, 0.75) || inRect(p, 0.57, 0.25, 0.70, 0.75) {
			return 2
		}
	default:
		if inRect(p, 0.20, 0.30, 0.60, 0.40) || inTri(p, pt{0.56, 0.18}, pt{0.82, 0.35}, pt{0.56, 0.52}) ||
			inRect(p, 0.40, 0.60, 0.80, 0.70) || inTri(p, pt{0.44, 0.48}, pt{0.18, 0.65}, pt{0.44, 0.82}) {
			return 2
		}
	}
	return 1
}

// render draws one size with 4×4 supersampling, as BGRA rows top-down.
func render(n int, kind iconKind, badge bool) []byte {
	bg := blue
	if kind == iconPaused {
		bg = amber
	}
	cols := [4]color.RGBA{{}, bg, white, red}
	out := make([]byte, n*n*4)
	const ss = 4
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			var r, g, b, a float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					p := pt{(float64(x) + (float64(sx)+0.5)/ss) / float64(n), (float64(y) + (float64(sy)+0.5)/ss) / float64(n)}
					c := cols[shade(p, kind, badge)]
					fa := float64(c.A) / 255
					r += float64(c.R) * fa
					g += float64(c.G) * fa
					b += float64(c.B) * fa
					a += fa
				}
			}
			i := (y*n + x) * 4
			if a > 0 {
				out[i+0] = byte(b / a)
				out[i+1] = byte(g / a)
				out[i+2] = byte(r / a)
			}
			out[i+3] = byte(a / (ss * ss) * 255)
		}
	}
	return out
}

// trayIcon returns an .ico with 16, 20, 24, 32 and 48 px images (32-bit DIBs).
func trayIcon(kind iconKind, badge bool) []byte {
	sizes := []int{16, 20, 24, 32, 48}
	var imgs [][]byte
	for _, n := range sizes {
		px := render(n, kind, badge)
		var b bytes.Buffer
		// BITMAPINFOHEADER; the height counts the XOR and AND bitmaps.
		binary.Write(&b, binary.LittleEndian, struct {
			Size                       uint32
			W, H                       int32
			Planes, Bits               uint16
			Compression, SizeImage     uint32
			XPPM, YPPM, Used, Imported int32
		}{40, int32(n), int32(2 * n), 1, 32, 0, 0, 0, 0, 0, 0})
		for y := n - 1; y >= 0; y-- { // bottom-up
			b.Write(px[y*n*4 : (y+1)*n*4])
		}
		stride := (n + 31) / 32 * 4
		b.Write(make([]byte, stride*n)) // AND mask: all 0, alpha does the work
		imgs = append(imgs, b.Bytes())
	}
	var out bytes.Buffer
	binary.Write(&out, binary.LittleEndian, [3]uint16{0, 1, uint16(len(sizes))})
	off := 6 + 16*len(sizes)
	for i, n := range sizes {
		binary.Write(&out, binary.LittleEndian, struct {
			W, H, Colors, Reserved byte
			Planes, Bits           uint16
			Len, Offset            uint32
		}{byte(n), byte(n), 0, 0, 1, 32, uint32(len(imgs[i])), uint32(off)})
		off += len(imgs[i])
	}
	for _, img := range imgs {
		out.Write(img)
	}
	return out.Bytes()
}
