package server

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/draw"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"strings"
	"sync"
	"time"

	"lukechampine.com/blake3"

	"fdsync/internal/protocol"
)

const (
	defaultClipboardMax     = 20        // max unselected images kept in memory
	maxClipboardSelected    = 50        // hard cap on selected/pinned images
	maxClipboardImageBytes  = 25 << 20  // 25 MiB per compressed image
	maxClipboardRawDIBBytes = 128 << 20 // 128 MiB for uncompressed Windows DIBs (e.g. dual-4K)
	defaultClipboardMaxEdge = 2048      // longest edge when proportional resize is enabled
	maxClipboardPixels      = 64_000_000
	maxClipboardNoteRunes   = 120
)

type clipboardEntry struct {
	protocol.ClipboardItem
	png       []byte
	pixelHash string
}

// clipboardStore keeps recent Windows clipboard screenshots (and panel-uploaded
// images) in memory only. Selected items are protected from automatic eviction
// when newer screenshots arrive.
type clipboardStore struct {
	mu            sync.RWMutex
	maxUnselected int
	maxSelected   int
	maxEdge       int
	compress      bool
	paused        bool
	seq           uint64
	items         []*clipboardEntry // newest first
}

func newClipboardStore() *clipboardStore {
	return &clipboardStore{
		maxUnselected: defaultClipboardMax,
		maxSelected:   maxClipboardSelected,
		maxEdge:       defaultClipboardMaxEdge,
		compress:      true,
	}
}

func (c *clipboardStore) isPaused() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.paused
}

func (c *clipboardStore) setPaused(paused bool) {
	c.mu.Lock()
	c.paused = paused
	c.mu.Unlock()
}

func (c *clipboardStore) compressSettings() (bool, int) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.compress, c.maxEdge
}

func (c *clipboardStore) setCompress(on bool) {
	c.mu.Lock()
	c.compress = on
	c.mu.Unlock()
}

func encodePNG(img image.Image, best bool) ([]byte, error) {
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.DefaultCompression}
	if best {
		enc.CompressionLevel = png.BestCompression
	}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("encode png: %w", err)
	}
	return buf.Bytes(), nil
}

// addImage decodes a PNG, JPEG or GIF byte slice, optionally resizes large
// images proportionally, normalizes to PNG, and stores it in the ring buffer.
func (c *clipboardStore) addImage(raw []byte, source, note string, selectIt bool) (protocol.ClipboardItem, bool, error) {
	if len(raw) == 0 {
		return protocol.ClipboardItem{}, false, errors.New("empty image")
	}
	if len(raw) > maxClipboardImageBytes {
		return protocol.ClipboardItem{}, false, fmt.Errorf("image exceeds %d MB limit", maxClipboardImageBytes>>20)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return protocol.ClipboardItem{}, false, fmt.Errorf("unsupported or invalid image: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > maxClipboardPixels {
		return protocol.ClipboardItem{}, false, errors.New("image dimensions out of range")
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return protocol.ClipboardItem{}, false, fmt.Errorf("decode image: %w", err)
	}

	compress, maxEdge := c.compressSettings()
	var origW, origH int
	resized := false
	if compress {
		if scaled, ok := resizeImage(img, maxEdge); ok {
			origW, origH = cfg.Width, cfg.Height
			img = scaled
			resized = true
		}
	}

	var pngBytes []byte
	if format == "png" && !resized {
		pngBytes = append([]byte(nil), raw...)
		// For large unresized PNGs under compress mode, keep a tighter lossless re-encode if smaller.
		if compress && len(raw) > 512<<10 {
			if tighter, err := encodePNG(img, true); err == nil && len(tighter) < len(pngBytes) {
				pngBytes = tighter
			}
		}
	} else {
		pngBytes, err = encodePNG(img, compress)
		if err != nil {
			return protocol.ClipboardItem{}, false, err
		}
	}
	return c.addDecoded(img, pngBytes, origW, origH, source, note, selectIt)
}

// addDIB decodes a Windows CF_DIB / CF_DIBV5 payload and stores it as PNG.
func (c *clipboardStore) addDIB(dib []byte) (protocol.ClipboardItem, bool, error) {
	if len(dib) > maxClipboardRawDIBBytes {
		return protocol.ClipboardItem{}, false, fmt.Errorf("dib exceeds %d MB limit", maxClipboardRawDIBBytes>>20)
	}
	var img image.Image
	decoded, err := decodeDIB(dib)
	if err != nil {
		return protocol.ClipboardItem{}, false, err
	}
	img = decoded
	origBounds := img.Bounds()
	compress, maxEdge := c.compressSettings()
	var origW, origH int
	if compress {
		if scaled, ok := resizeImage(img, maxEdge); ok {
			origW, origH = origBounds.Dx(), origBounds.Dy()
			img = scaled
		}
	}
	pngBytes, err := encodePNG(img, compress)
	if err != nil {
		return protocol.ClipboardItem{}, false, err
	}
	return c.addDecoded(img, pngBytes, origW, origH, "clipboard", "", false)
}

func (c *clipboardStore) addDecoded(img image.Image, pngBytes []byte, origW, origH int, source, note string, selectIt bool) (protocol.ClipboardItem, bool, error) {
	if len(pngBytes) > maxClipboardImageBytes {
		return protocol.ClipboardItem{}, false, fmt.Errorf("encoded image exceeds %d MB limit", maxClipboardImageBytes>>20)
	}
	note = trimNote(note)
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	ph := hashImagePixels(img)
	pngHash := blake3.Sum256(pngBytes)
	shortHash := hex.EncodeToString(pngHash[:16])

	c.mu.Lock()
	defer c.mu.Unlock()

	if source == "clipboard" && c.paused {
		return protocol.ClipboardItem{}, false, nil
	}

	// Deduplicate by normalized pixel hash so Windows apps that fire two
	// WM_CLIPBOARDUPDATE events (e.g. CF_DIB then PNG) never create duplicates.
	for i, existing := range c.items {
		if existing.pixelHash != ph {
			continue
		}
		if i == 0 {
			if source == "upload" {
				if selectIt && !existing.Selected {
					if c.selectedCountLocked() < c.maxSelected {
						existing.Selected = true
					}
				}
				if note != "" {
					existing.Note = note
				}
			}
			return existing.ClipboardItem, false, nil
		}
		// Move the existing item to the front and refresh its timestamp.
		existing.CapturedAt = time.Now()
		if selectIt && !existing.Selected && c.selectedCountLocked() < c.maxSelected {
			existing.Selected = true
		}
		if note != "" {
			existing.Note = note
		}
		copy(c.items[1:i+1], c.items[:i])
		c.items[0] = existing
		return existing.ClipboardItem, true, nil
	}

	if selectIt && c.selectedCountLocked() >= c.maxSelected {
		return protocol.ClipboardItem{}, false, fmt.Errorf("at most %d images can be selected at once", c.maxSelected)
	}

	c.seq++
	entry := &clipboardEntry{
		ClipboardItem: protocol.ClipboardItem{
			ID:             fmt.Sprintf("clip-%04d", c.seq),
			CapturedAt:     time.Now(),
			Width:          w,
			Height:         h,
			OriginalWidth:  origW,
			OriginalHeight: origH,
			Size:           int64(len(pngBytes)),
			Hash:           shortHash,
			Selected:       selectIt,
			Source:         source,
			Note:           note,
		},
		png:       pngBytes,
		pixelHash: ph,
	}
	c.items = append([]*clipboardEntry{entry}, c.items...)
	c.evictLocked()
	return entry.ClipboardItem, true, nil
}

func (c *clipboardStore) selectedCountLocked() int {
	n := 0
	for _, it := range c.items {
		if it.Selected {
			n++
		}
	}
	return n
}

// evictLocked drops the oldest unselected entries once unselected items exceed
// maxUnselected. Selected/pinned entries are never evicted automatically.
func (c *clipboardStore) evictLocked() {
	unselected := 0
	for _, it := range c.items {
		if !it.Selected {
			unselected++
		}
	}
	for i := len(c.items) - 1; i >= 0 && unselected > c.maxUnselected; i-- {
		if !c.items[i].Selected {
			c.items = append(c.items[:i], c.items[i+1:]...)
			unselected--
		}
	}
}

func (c *clipboardStore) list() protocol.ClipboardList {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]protocol.ClipboardItem, len(c.items))
	for i, it := range c.items {
		out[i] = it.ClipboardItem
	}
	return protocol.ClipboardList{
		Paused:   c.paused,
		Compress: c.compress,
		MaxEdge:  c.maxEdge,
		Max:      c.maxUnselected,
		Items:    out,
	}
}

func (c *clipboardStore) summary() (count, selected int, paused bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, it := range c.items {
		if it.Selected {
			selected++
		}
	}
	return len(c.items), selected, c.paused
}

func (c *clipboardStore) get(id string) (protocol.ClipboardItem, []byte, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, it := range c.items {
		if it.ID == id {
			return it.ClipboardItem, it.png, true
		}
	}
	return protocol.ClipboardItem{}, nil, false
}

func (c *clipboardStore) update(id string, selected *bool, note *string) (protocol.ClipboardItem, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, it := range c.items {
		if it.ID != id {
			continue
		}
		if selected != nil && *selected != it.Selected {
			if *selected && c.selectedCountLocked() >= c.maxSelected {
				return protocol.ClipboardItem{}, fmt.Errorf("at most %d images can be selected at once", c.maxSelected)
			}
			it.Selected = *selected
		}
		if note != nil {
			it.Note = trimNote(*note)
		}
		return it.ClipboardItem, nil
	}
	return protocol.ClipboardItem{}, errClipNotFound
}

func (c *clipboardStore) delete(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, it := range c.items {
		if it.ID == id {
			c.items = append(c.items[:i], c.items[i+1:]...)
			return true
		}
	}
	return false
}

func (c *clipboardStore) clear(onlyUnselected bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !onlyUnselected {
		c.items = nil
		return
	}
	kept := c.items[:0]
	for _, it := range c.items {
		if it.Selected {
			kept = append(kept, it)
		}
	}
	c.items = kept
}

func (c *clipboardStore) unselectAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, it := range c.items {
		it.Selected = false
	}
}

var errClipNotFound = errors.New("no such clipboard image")

func trimNote(s string) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > maxClipboardNoteRunes {
		s = string(r[:maxClipboardNoteRunes])
	}
	return s
}

func hashImagePixels(img image.Image) string {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	nrgba, ok := img.(*image.NRGBA)
	if !ok || nrgba.Rect.Min.X != 0 || nrgba.Rect.Min.Y != 0 {
		dst := image.NewNRGBA(image.Rect(0, 0, w, h))
		draw.Draw(dst, dst.Bounds(), img, b.Min, draw.Src)
		nrgba = dst
	}
	hasher := blake3.New(32, nil)
	var dim [8]byte
	binary.LittleEndian.PutUint32(dim[0:4], uint32(w))
	binary.LittleEndian.PutUint32(dim[4:8], uint32(h))
	_, _ = hasher.Write(dim[:])
	rowBytes := w * 4
	if nrgba.Stride == rowBytes {
		_, _ = hasher.Write(nrgba.Pix[:h*rowBytes])
	} else {
		for y := 0; y < h; y++ {
			off := y * nrgba.Stride
			_, _ = hasher.Write(nrgba.Pix[off : off+rowBytes])
		}
	}
	return hex.EncodeToString(hasher.Sum(nil))
}
