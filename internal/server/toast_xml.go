package server

import (
	"bytes"
	"encoding/xml"
	"image"
	"image/color"
	"image/png"
	"net/url"
	"strings"
)

const (
	toastAppID          = "fdsync"
	toastActivatorCLSID = "{7B63D4E9-8C21-4F7A-9B05-2E9E5D1A6F4C}"
	toastOpenPrefix     = "open:"
)

// toastFileURI converts an absolute Windows (or POSIX) local file path into a
// RFC 8089 file:/// URI with forward slashes and per-segment UTF-8 percent
// encoding so Windows Toast XML loads paths containing spaces or CJK names.
func toastFileURI(path string) string {
	if path == "" {
		return ""
	}
	s := strings.ReplaceAll(path, "\\", "/")
	var b strings.Builder
	b.WriteString("file:///")
	parts := strings.Split(strings.TrimPrefix(s, "/"), "/")
	for i, part := range parts {
		if i > 0 {
			b.WriteByte('/')
		}
		// Keep Windows drive letters like "C:" unescaped.
		if i == 0 && len(part) == 2 && part[1] == ':' &&
			((part[0] >= 'A' && part[0] <= 'Z') || (part[0] >= 'a' && part[0] <= 'z')) {
			b.WriteString(part)
			continue
		}
		b.WriteString(url.PathEscape(part))
	}
	return b.String()
}

// buildToastXML builds a Windows 10/11 ToastGeneric XML payload with optional
// full-bleed hero image preview and an interactive action button.
func buildToastXML(title, body, heroImageURI, launchArgs, openBtnLabel string) string {
	var b strings.Builder
	b.WriteString(`<toast activationType="foreground" launch="`)
	writeXMLAttr(&b, launchArgs)
	b.WriteString(`" duration="short"><visual><binding template="ToastGeneric">`)
	if heroImageURI != "" {
		b.WriteString(`<image placement="hero" src="`)
		writeXMLAttr(&b, heroImageURI)
		b.WriteString(`"/>`)
	}
	if title != "" {
		b.WriteString(`<text hint-maxLines="1">`)
		writeXMLText(&b, title)
		b.WriteString(`</text>`)
	}
	if body != "" {
		b.WriteString(`<text>`)
		writeXMLText(&b, body)
		b.WriteString(`</text>`)
	}
	b.WriteString(`</binding></visual>`)
	if openBtnLabel != "" && launchArgs != "" {
		b.WriteString(`<actions><action content="`)
		writeXMLAttr(&b, openBtnLabel)
		b.WriteString(`" activationType="foreground" arguments="`)
		writeXMLAttr(&b, launchArgs)
		b.WriteString(`"/></actions>`)
	}
	b.WriteString(`</toast>`)
	return b.String()
}

func writeXMLText(b *strings.Builder, s string) {
	_ = xml.EscapeText(b, []byte(s))
}

func writeXMLAttr(b *strings.Builder, s string) {
	// xml.EscapeText escapes &, <, >, ', and " (&#34;), safe for double-quoted attributes.
	_ = xml.EscapeText(b, []byte(s))
}

// parseToastOpenID extracts a strictly validated incoming clip ID from a toast
// activation argument like "open:clip-0001".
func parseToastOpenID(args string) string {
	if !strings.HasPrefix(args, toastOpenPrefix) {
		return ""
	}
	id := strings.TrimPrefix(args, toastOpenPrefix)
	if id == "" || len(id) > 64 {
		return ""
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return ""
		}
	}
	return id
}

// renderToastAppIconPNG renders the fdsync blue rounded-square dual-arrow app
// icon as a crisp PNG for Windows toast notification branding (IconUri).
func renderToastAppIconPNG(n int) []byte {
	type pt struct{ x, y float64 }
	inTri := func(p, a, b, c pt) bool {
		s := func(p1, p2, p3 pt) float64 { return (p1.x-p3.x)*(p2.y-p3.y) - (p2.x-p3.x)*(p1.y-p3.y) }
		d1, d2, d3 := s(p, a, b), s(p, b, c), s(p, c, a)
		return !((d1 < 0 || d2 < 0 || d3 < 0) && (d1 > 0 || d2 > 0 || d3 > 0))
	}
	inRect := func(p pt, x0, y0, x1, y1 float64) bool {
		return p.x >= x0 && p.x <= x1 && p.y >= y0 && p.y <= y1
	}
	inRounded := func(p pt, r float64) bool {
		cx := min(max(p.x, r), 1-r)
		cy := min(max(p.y, r), 1-r)
		dx, dy := p.x-cx, p.y-cy
		return dx*dx+dy*dy <= r*r
	}
	cols := [3]color.RGBA{
		{},
		{0x25, 0x63, 0xeb, 0xff},
		{0xff, 0xff, 0xff, 0xff},
	}
	img := image.NewNRGBA(image.Rect(0, 0, n, n))
	const ss = 4
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			var r, g, b, a float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					p := pt{(float64(x) + (float64(sx)+0.5)/ss) / float64(n), (float64(y) + (float64(sy)+0.5)/ss) / float64(n)}
					ci := 0
					if inRounded(p, 0.22) {
						ci = 1
						if inRect(p, 0.20, 0.30, 0.60, 0.40) || inTri(p, pt{0.56, 0.18}, pt{0.82, 0.35}, pt{0.56, 0.52}) ||
							inRect(p, 0.40, 0.60, 0.80, 0.70) || inTri(p, pt{0.44, 0.48}, pt{0.18, 0.65}, pt{0.44, 0.82}) {
							ci = 2
						}
					}
					c := cols[ci]
					fa := float64(c.A) / 255
					r += float64(c.R) * fa
					g += float64(c.G) * fa
					b += float64(c.B) * fa
					a += fa
				}
			}
			var px color.NRGBA
			if a > 0 {
				px.R = byte(r / a)
				px.G = byte(g / a)
				px.B = byte(b / a)
			}
			px.A = byte(a / (ss * ss) * 255)
			img.SetNRGBA(x, y, px)
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}
