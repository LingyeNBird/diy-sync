package server

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"math/bits"
)

const (
	biRGB       = 0
	biBitfields = 3
)

// decodeDIB converts a Windows CF_DIB / CF_DIBV5 memory block into an NRGBA
// image. It supports BITMAPINFOHEADER (40B), BITMAPV4HEADER (108B) and
// BITMAPV5HEADER (124B), top-down and bottom-up rows, 8/16/24/32-bit pixels,
// BI_RGB and BI_BITFIELDS, and fixes Windows 32-bit bitmaps that leave every
// alpha byte as zero.
func decodeDIB(data []byte) (*image.NRGBA, error) {
	if len(data) < 40 {
		return nil, errors.New("dib header too short")
	}
	hdrSize := int(binary.LittleEndian.Uint32(data[0:4]))
	if hdrSize < 40 || hdrSize > len(data) {
		return nil, fmt.Errorf("invalid dib header size %d", hdrSize)
	}
	w := int(int32(binary.LittleEndian.Uint32(data[4:8])))
	hRaw := int(int32(binary.LittleEndian.Uint32(data[8:12])))
	planes := binary.LittleEndian.Uint16(data[12:14])
	bitCount := int(binary.LittleEndian.Uint16(data[14:16]))
	compression := binary.LittleEndian.Uint32(data[16:20])
	clrUsed := int(binary.LittleEndian.Uint32(data[32:36]))

	if planes != 1 {
		return nil, fmt.Errorf("unsupported dib planes %d", planes)
	}
	topDown := hRaw < 0
	h := hRaw
	if topDown {
		h = -h
	}
	if w <= 0 || h <= 0 || int64(w)*int64(h) > maxClipboardPixels {
		return nil, fmt.Errorf("invalid dib dimensions %dx%d", w, h)
	}
	if compression != biRGB && compression != biBitfields {
		return nil, fmt.Errorf("unsupported dib compression %d", compression)
	}

	var rMask, gMask, bMask, aMask uint32
	extraMasks := 0
	if compression == biBitfields {
		if hdrSize >= 56 {
			rMask = binary.LittleEndian.Uint32(data[40:44])
			gMask = binary.LittleEndian.Uint32(data[44:48])
			bMask = binary.LittleEndian.Uint32(data[48:52])
			aMask = binary.LittleEndian.Uint32(data[52:56])
		} else {
			if hdrSize+12 > len(data) {
				return nil, errors.New("dib bitfields truncated")
			}
			rMask = binary.LittleEndian.Uint32(data[hdrSize : hdrSize+4])
			gMask = binary.LittleEndian.Uint32(data[hdrSize+4 : hdrSize+8])
			bMask = binary.LittleEndian.Uint32(data[hdrSize+8 : hdrSize+12])
			extraMasks = 12
		}
	} else {
		switch bitCount {
		case 16:
			rMask, gMask, bMask = 0x7C00, 0x03E0, 0x001F
		case 32:
			rMask, gMask, bMask = 0x00FF0000, 0x0000FF00, 0x000000FF
			if hdrSize >= 56 {
				aMask = binary.LittleEndian.Uint32(data[52:56])
			} else {
				aMask = 0xFF000000
			}
		}
	}

	paletteCount := 0
	if bitCount <= 8 {
		if clrUsed > 0 {
			paletteCount = clrUsed
		} else {
			paletteCount = 1 << bitCount
		}
	} else if clrUsed > 0 {
		// Some Windows apps set biClrUsed=3 on a 40-byte BI_BITFIELDS header to
		// count the 3 color masks we already accounted for in extraMasks.
		if !(hdrSize == 40 && compression == biBitfields && clrUsed == 3 && hdrSize+extraMasks+clrUsed*4+((w*bitCount+31)/32)*4*h > len(data)) {
			paletteCount = clrUsed
		}
	}
	if paletteCount > 256 && bitCount <= 8 {
		return nil, errors.New("dib palette too large")
	}
	paletteOff := hdrSize + extraMasks
	pixelOff := paletteOff + paletteCount*4
	if pixelOff > len(data) {
		return nil, errors.New("dib pixel offset out of bounds")
	}

	stride := ((w*bitCount + 31) / 32) * 4
	if pixelOff+stride*h > len(data) {
		return nil, errors.New("dib pixel data truncated")
	}

	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	switch bitCount {
	case 32:
		hasNonZeroAlpha := false
		for y := 0; y < h; y++ {
			srcY := h - 1 - y
			if topDown {
				srcY = y
			}
			srcRow := data[pixelOff+srcY*stride:]
			dstRow := out.Pix[y*out.Stride:]
			if compression == biRGB && rMask == 0x00FF0000 && gMask == 0x0000FF00 && bMask == 0x000000FF {
				for x := 0; x < w; x++ {
					s := x * 4
					d := x * 4
					a := uint8(0xFF)
					if aMask != 0 {
						a = srcRow[s+3]
						if a != 0 {
							hasNonZeroAlpha = true
						}
					}
					dstRow[d+0] = srcRow[s+2]
					dstRow[d+1] = srcRow[s+1]
					dstRow[d+2] = srcRow[s+0]
					dstRow[d+3] = a
				}
			} else {
				for x := 0; x < w; x++ {
					v := binary.LittleEndian.Uint32(srcRow[x*4 : x*4+4])
					d := x * 4
					dstRow[d+0] = extractMasked(v, rMask)
					dstRow[d+1] = extractMasked(v, gMask)
					dstRow[d+2] = extractMasked(v, bMask)
					if aMask != 0 {
						a := extractMasked(v, aMask)
						if a != 0 {
							hasNonZeroAlpha = true
						}
						dstRow[d+3] = a
					} else {
						dstRow[d+3] = 0xFF
					}
				}
			}
		}
		if aMask != 0 && !hasNonZeroAlpha {
			for i := 3; i < len(out.Pix); i += 4 {
				out.Pix[i] = 0xFF
			}
		}
	case 24:
		for y := 0; y < h; y++ {
			srcY := h - 1 - y
			if topDown {
				srcY = y
			}
			srcRow := data[pixelOff+srcY*stride:]
			dstRow := out.Pix[y*out.Stride:]
			for x := 0; x < w; x++ {
				s := x * 3
				d := x * 4
				dstRow[d+0] = srcRow[s+2]
				dstRow[d+1] = srcRow[s+1]
				dstRow[d+2] = srcRow[s+0]
				dstRow[d+3] = 0xFF
			}
		}
	case 16:
		for y := 0; y < h; y++ {
			srcY := h - 1 - y
			if topDown {
				srcY = y
			}
			srcRow := data[pixelOff+srcY*stride:]
			dstRow := out.Pix[y*out.Stride:]
			for x := 0; x < w; x++ {
				v := uint32(binary.LittleEndian.Uint16(srcRow[x*2 : x*2+2]))
				d := x * 4
				dstRow[d+0] = extractMasked(v, rMask)
				dstRow[d+1] = extractMasked(v, gMask)
				dstRow[d+2] = extractMasked(v, bMask)
				dstRow[d+3] = 0xFF
			}
		}
	case 8:
		palette := data[paletteOff:pixelOff]
		for y := 0; y < h; y++ {
			srcY := h - 1 - y
			if topDown {
				srcY = y
			}
			srcRow := data[pixelOff+srcY*stride:]
			dstRow := out.Pix[y*out.Stride:]
			for x := 0; x < w; x++ {
				idx := int(srcRow[x])
				if idx >= paletteCount {
					idx = 0
				}
				p := idx * 4
				d := x * 4
				dstRow[d+0] = palette[p+2]
				dstRow[d+1] = palette[p+1]
				dstRow[d+2] = palette[p+0]
				dstRow[d+3] = 0xFF
			}
		}
	default:
		return nil, fmt.Errorf("unsupported dib bit depth %d", bitCount)
	}
	return out, nil
}

func extractMasked(v, mask uint32) uint8 {
	if mask == 0 {
		return 0
	}
	shift := bits.TrailingZeros32(mask)
	width := bits.OnesCount32(mask)
	raw := (v & mask) >> shift
	if width >= 8 {
		return uint8(raw >> (width - 8))
	}
	maxVal := uint32((1 << width) - 1)
	if maxVal == 0 {
		return 0
	}
	return uint8((raw*255 + maxVal/2) / maxVal)
}

// encodeDIB encodes img into a 32-bit bottom-up BI_RGB CF_DIB payload suitable
// for Windows SetClipboardData(CF_DIB).
func encodeDIB(src image.Image) []byte {
	nrgba := toNRGBA(src)
	w, h := nrgba.Bounds().Dx(), nrgba.Bounds().Dy()
	stride := w * 4
	out := make([]byte, 40+stride*h)
	binary.LittleEndian.PutUint32(out[0:4], 40)
	binary.LittleEndian.PutUint32(out[4:8], uint32(int32(w)))
	binary.LittleEndian.PutUint32(out[8:12], uint32(int32(h))) // bottom-up
	binary.LittleEndian.PutUint16(out[12:14], 1)
	binary.LittleEndian.PutUint16(out[14:16], 32)
	binary.LittleEndian.PutUint32(out[16:20], biRGB)
	binary.LittleEndian.PutUint32(out[20:24], uint32(stride*h))
	for y := 0; y < h; y++ {
		srcRow := nrgba.Pix[y*nrgba.Stride:]
		dstRow := out[40+(h-1-y)*stride:]
		for x := 0; x < w; x++ {
			s := x * 4
			dstRow[s+0] = srcRow[s+2] // B
			dstRow[s+1] = srcRow[s+1] // G
			dstRow[s+2] = srcRow[s+0] // R
			dstRow[s+3] = srcRow[s+3] // A
		}
	}
	return out
}
