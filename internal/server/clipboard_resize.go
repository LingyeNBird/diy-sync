package server

import (
	"image"
	"image/draw"
	"math"
)

// resizeImage proportionally scales src down so its longest edge is at most
// maxEdge, using a two-pass separable Mitchell-Netravali (B=1/3, C=1/3) filter
// with scale-adapted support. This preserves sharp UI text without ringing or
// moire. If src already fits within maxEdge, it is returned unchanged.
func resizeImage(src image.Image, maxEdge int) (image.Image, bool) {
	if maxEdge <= 0 {
		return src, false
	}
	b := src.Bounds()
	srcW, srcH := b.Dx(), b.Dy()
	if srcW <= maxEdge && srcH <= maxEdge {
		return src, false
	}
	var dstW, dstH int
	if srcW >= srcH {
		dstW = maxEdge
		dstH = max(1, int(math.Round(float64(srcH)*float64(maxEdge)/float64(srcW))))
	} else {
		dstH = maxEdge
		dstW = max(1, int(math.Round(float64(srcW)*float64(maxEdge)/float64(srcH))))
	}

	nrgba := toNRGBA(src)
	tmp := resizeHorizontal(nrgba, dstW)
	dst := resizeVertical(tmp, dstH)
	return dst, true
}

func toNRGBA(src image.Image) *image.NRGBA {
	b := src.Bounds()
	if img, ok := src.(*image.NRGBA); ok && b.Min.X == 0 && b.Min.Y == 0 {
		return img
	}
	dst := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Src)
	return dst
}

// mitchell evaluates the Mitchell-Netravali cubic kernel (B=1/3, C=1/3).
func mitchell(x float64) float64 {
	if x < 0 {
		x = -x
	}
	if x < 1 {
		return ((7*x-12)*x*x + 16.0/3.0) / 6.0
	}
	if x < 2 {
		return (((-7.0/3.0)*x+12)*x-20)*x/6.0 + 32.0/18.0
	}
	return 0
}

type tapList struct {
	start   int
	weights []float64
}

func buildTaps(srcN, dstN int) []tapList {
	scale := float64(dstN) / float64(srcN)
	invScale := 1.0 / scale
	radius := 2.0 * invScale // scaled support for anti-aliased downscaling
	taps := make([]tapList, dstN)
	for i := 0; i < dstN; i++ {
		center := (float64(i)+0.5)*invScale - 0.5
		left := max(0, int(math.Floor(center-radius)))
		right := min(srcN-1, int(math.Ceil(center+radius)))
		w := make([]float64, right-left+1)
		sum := 0.0
		for j := left; j <= right; j++ {
			wt := mitchell((float64(j) - center) * scale)
			w[j-left] = wt
			sum += wt
		}
		if sum != 0 {
			inv := 1.0 / sum
			for k := range w {
				w[k] *= inv
			}
		}
		taps[i] = tapList{start: left, weights: w}
	}
	return taps
}

func clampByte(v float64) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return uint8(v + 0.5)
}

func resizeHorizontal(src *image.NRGBA, dstW int) *image.NRGBA {
	srcW, h := src.Bounds().Dx(), src.Bounds().Dy()
	dst := image.NewNRGBA(image.Rect(0, 0, dstW, h))
	taps := buildTaps(srcW, dstW)
	for y := 0; y < h; y++ {
		srcRow := src.Pix[y*src.Stride:]
		dstRow := dst.Pix[y*dst.Stride:]
		for x := 0; x < dstW; x++ {
			t := taps[x]
			var r, g, b, a float64
			for k, wt := range t.weights {
				if wt == 0 {
					continue
				}
				off := (t.start + k) * 4
				sa := float64(srcRow[off+3])
				wa := wt * sa
				r += float64(srcRow[off+0]) * wa
				g += float64(srcRow[off+1]) * wa
				b += float64(srcRow[off+2]) * wa
				a += wa
			}
			d := x * 4
			if a > 1e-6 {
				invA := 1.0 / a
				dstRow[d+0] = clampByte(r * invA)
				dstRow[d+1] = clampByte(g * invA)
				dstRow[d+2] = clampByte(b * invA)
				dstRow[d+3] = clampByte(a)
			} else {
				dstRow[d+0], dstRow[d+1], dstRow[d+2], dstRow[d+3] = 0, 0, 0, 0
			}
		}
	}
	return dst
}

func resizeVertical(src *image.NRGBA, dstH int) *image.NRGBA {
	w, srcH := src.Bounds().Dx(), src.Bounds().Dy()
	dst := image.NewNRGBA(image.Rect(0, 0, w, dstH))
	taps := buildTaps(srcH, dstH)
	for y := 0; y < dstH; y++ {
		t := taps[y]
		dstRow := dst.Pix[y*dst.Stride:]
		for x := 0; x < w; x++ {
			var r, g, b, a float64
			colOff := x * 4
			for k, wt := range t.weights {
				if wt == 0 {
					continue
				}
				off := (t.start+k)*src.Stride + colOff
				sa := float64(src.Pix[off+3])
				wa := wt * sa
				r += float64(src.Pix[off+0]) * wa
				g += float64(src.Pix[off+1]) * wa
				b += float64(src.Pix[off+2]) * wa
				a += wa
			}
			d := colOff
			if a > 1e-6 {
				invA := 1.0 / a
				dstRow[d+0] = clampByte(r * invA)
				dstRow[d+1] = clampByte(g * invA)
				dstRow[d+2] = clampByte(b * invA)
				dstRow[d+3] = clampByte(a)
			} else {
				dstRow[d+0], dstRow[d+1], dstRow[d+2], dstRow[d+3] = 0, 0, 0, 0
			}
		}
	}
	return dst
}
