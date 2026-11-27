package server

import (
	"bytes"
	"encoding/xml"
	"image/png"
	"strings"
	"testing"
)

func TestToastFileURI(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{
			in:   `C:\Users\Alice\AppData\Local\Temp\fdsync-incoming\clip-0001.png`,
			want: `file:///C:/Users/Alice/AppData/Local/Temp/fdsync-incoming/clip-0001.png`,
		},
		{
			in:   `C:\Users\张 三\AppData\Local\Temp\fdsync-incoming\clip-0002.png`,
			want: `file:///C:/Users/%E5%BC%A0%20%E4%B8%89/AppData/Local/Temp/fdsync-incoming/clip-0002.png`,
		},
		{
			in:   `/tmp/fdsync-incoming/a&b.png`,
			want: `file:///tmp/fdsync-incoming/a&b.png`,
		},
	}
	for _, tc := range cases {
		if got := toastFileURI(tc.in); got != tc.want {
			t.Errorf("toastFileURI(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBuildToastXMLValidAndEscaped(t *testing.T) {
	heroURI := toastFileURI(`C:\Users\A & B\Temp\clip-0001.png`)
	payload := buildToastXML(
		`收到来自 WSL 的图片 <test>`,
		`a&b "quoted" ]]> (1376x768)`,
		heroURI,
		`open:clip-0001`,
		`打开图片`,
	)
	if !strings.Contains(payload, `placement="hero"`) {
		t.Fatalf("expected hero image tag in XML: %s", payload)
	}
	if !strings.Contains(payload, `arguments="open:clip-0001"`) {
		t.Fatalf("expected open action button in XML: %s", payload)
	}
	// Ensure the generated XML parses cleanly with standard encoding/xml.
	dec := xml.NewDecoder(strings.NewReader(payload))
	for {
		if _, err := dec.Token(); err != nil {
			if err.Error() == "EOF" {
				break
			}
			t.Fatalf("invalid toast XML: %v\n%s", err, payload)
		}
	}

	// Text-only toast should omit hero image and actions.
	textXML := buildToastXML("Text copied", "12 chars", "", "", "")
	if strings.Contains(textXML, "<image") || strings.Contains(textXML, "<actions") {
		t.Fatalf("text-only toast should not include image/actions: %s", textXML)
	}
}

func TestParseToastOpenID(t *testing.T) {
	if got := parseToastOpenID("open:clip-0001"); got != "clip-0001" {
		t.Fatalf("got %q, want clip-0001", got)
	}
	for _, bad := range []string{"", "open:", "open:../secret", `open:a\b`, "open:a/b", "other:clip-0001"} {
		if got := parseToastOpenID(bad); got != "" {
			t.Errorf("parseToastOpenID(%q) = %q, want empty", bad, got)
		}
	}
}

func TestRenderToastAppIconPNG(t *testing.T) {
	data := renderToastAppIconPNG(64)
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode rendered icon PNG: %v", err)
	}
	if img.Bounds().Dx() != 64 || img.Bounds().Dy() != 64 {
		t.Fatalf("unexpected icon dimensions: %v", img.Bounds())
	}
}
