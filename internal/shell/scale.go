package shell

import (
	"image"
	"math"
)

// Scaling happens once, at load: art is authored at one size and shown at
// another, and everything downstream of the loader works in shell pixels
// without knowing the difference. Two things make that harder than calling
// an image library.
//
// Layers must stay aligned. A layer's art occupies [at×s, (at+size)×s) on
// the scaled canvas, which rarely starts on a whole pixel. Rounding at and
// scaling the image on its own moves every layer by its own rounding error,
// up to a pixel, and on a face that is eyes drifting off their sockets. So
// the scaled image starts at floor(at×s), covers through ceil((at+size)×s),
// and is resampled with the fraction accounted for: the art lands where it
// would have landed had the whole shell been drawn as one picture.
//
// Edges must not halo. Colour is resampled premultiplied by alpha, so the
// colour of transparent pixels (which is arbitrary, and often black) never
// bleeds into the visible edge as a ring.

// scaleEpsilon absorbs the rounding error of scales like 1/3, whose product
// with a whole number that should land on a pixel boundary lands a hair
// either side of it.
const scaleEpsilon = 1e-9

// floorScaled is floor(v×s), robust to that error.
func floorScaled(v int, s float64) int {
	return int(math.Floor(float64(v)*s + scaleEpsilon))
}

// ceilScaled is ceil(v×s), robust to that error.
func ceilScaled(v int, s float64) int {
	return int(math.Ceil(float64(v)*s - scaleEpsilon))
}

// resample draws art placed at at (art pixels) at scale s, returning the
// scaled image and its position in shell pixels. Scale 1 returns art
// itself, untouched, so an unscaled shell is exactly the art on disk. Art
// that covers nothing stays that way.
//
// Downscaling averages area: each output pixel is the mean of the source
// pixels under it, weighted by how much of each it covers, which is the
// only way a 1-pixel line survives a quarter scale as a faint line rather
// than vanishing or doubling. Upscaling is bilinear, with the pixels on
// the art's edge weighted by how much of them the art covers, so the edge
// falls exactly where it should.
func resample(art *image.NRGBA, at [2]int, s float64) (*image.NRGBA, [2]int) {
	if s == 1 {
		return art, at
	}
	nat := [2]int{floorScaled(at[0], s), floorScaled(at[1], s)}
	b := art.Bounds()
	if b.Empty() {
		return art, nat
	}
	w, h := b.Dx(), b.Dy()
	ow := ceilScaled(at[0]+w, s) - nat[0]
	oh := ceilScaled(at[1]+h, s) - nat[1]
	cols := axisTaps(at[0], w, nat[0], ow, s)
	rows := axisTaps(at[1], h, nat[1], oh, s)

	// Premultiplied source, alpha 0..255 and colour scaled by alpha/255.
	src := make([]float64, 4*w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := art.NRGBAAt(b.Min.X+x, b.Min.Y+y)
			a := float64(c.A)
			i := 4 * (y*w + x)
			src[i] = float64(c.R) * a / 255
			src[i+1] = float64(c.G) * a / 255
			src[i+2] = float64(c.B) * a / 255
			src[i+3] = a
		}
	}

	// Separable: across each row, then down each column.
	mid := make([]float64, 4*ow*h)
	for y := 0; y < h; y++ {
		for ox, taps := range cols {
			o := 4 * (y*ow + ox)
			for _, t := range taps {
				i := 4 * (y*w + t.at)
				for c := 0; c < 4; c++ {
					mid[o+c] += src[i+c] * t.weight
				}
			}
		}
	}
	out := image.NewNRGBA(image.Rect(0, 0, ow, oh))
	for oy, taps := range rows {
		for ox := 0; ox < ow; ox++ {
			var px [4]float64
			for _, t := range taps {
				i := 4 * (t.at*ow + ox)
				for c := 0; c < 4; c++ {
					px[c] += mid[i+c] * t.weight
				}
			}
			a := math.Round(px[3])
			if a <= 0 {
				continue
			}
			o := out.PixOffset(ox, oy)
			for c := 0; c < 3; c++ {
				out.Pix[o+c] = clampByte(px[c] * 255 / px[3])
			}
			out.Pix[o+3] = clampByte(a)
		}
	}
	return out, nat
}

// tap is one source pixel's weight in an output pixel, along one axis.
type tap struct {
	at     int
	weight float64
}

// axisTaps works out, along one axis, which source pixels feed each of on
// output pixels and by how much. The art spans source pixels [0, n) placed
// at at; output pixel o spans [nat+o, nat+o+1) on the scaled canvas, which
// is [lo, hi) in source pixels.
func axisTaps(at, n, nat, on int, s float64) [][]tap {
	out := make([][]tap, on)
	for o := range out {
		lo := float64(nat+o)/s - float64(at)
		hi := float64(nat+o+1)/s - float64(at)
		if s < 1 {
			// Area: each source pixel counts by how much of the output
			// pixel's footprint it fills. Where the footprint hangs off the
			// art the weights sum to less than one, and the pixel is that
			// much transparent.
			for k := max(0, int(math.Floor(lo))); k < min(n, int(math.Ceil(hi))); k++ {
				if cover := math.Min(hi, float64(k+1)) - math.Max(lo, float64(k)); cover > 0 {
					out[o] = append(out[o], tap{k, cover * s})
				}
			}
			continue
		}
		// Bilinear at the output pixel's centre, clamped to the art, then
		// scaled by how much of the output pixel the art covers.
		cover := math.Min(1, (math.Min(hi, float64(n))-math.Max(lo, 0))*s)
		if cover <= 0 {
			continue
		}
		u := math.Max(0, math.Min(float64(n-1), (lo+hi)/2-0.5))
		k := int(math.Floor(u))
		f := u - float64(k)
		out[o] = append(out[o], tap{k, (1 - f) * cover})
		if f > 0 && k+1 < n {
			out[o] = append(out[o], tap{k + 1, f * cover})
		}
	}
	return out
}

func clampByte(v float64) uint8 {
	return uint8(math.Max(0, math.Min(255, math.Round(v))))
}

// rescale converts a shell loaded at art resolution to shell pixels: every
// variant, every layer's position and pivot, the hit rectangles and the
// clips' offsets. Masks and the base are left at art resolution; they are
// for checking and painting the art, not for showing it.
func (s *Shell) rescale(k float64) {
	for _, l := range s.layers {
		nat := [2]int{floorScaled(l.At[0], k), floorScaled(l.At[1], k)}
		for name, art := range l.variants {
			l.variants[name], _ = resample(art, l.At, k)
		}
		// The pivot stays on the same point of the drawing, which the
		// whole-pixel origin has moved away from by a fraction.
		for i := range 2 {
			l.pivot[i] = float64(l.At[i]+l.Pivot[i])*k - float64(nat[i])
		}
		l.At = nat
	}
	for i, h := range s.hits {
		if h.Size[0] <= 0 || h.Size[1] <= 0 {
			// Switched off; kept off.
			continue
		}
		var at, far [2]int
		for j := range 2 {
			at[j] = floorScaled(h.At[j], k)
			far[j] = ceilScaled(h.At[j]+h.Size[j], k)
		}
		s.hits[i].At = at
		s.hits[i].Size = [2]int{far[0] - at[0], far[1] - at[1]}
	}
	for _, c := range s.clips {
		for _, key := range c.Keys {
			for layer, t := range key.Move {
				t.Offset[0] *= k
				t.Offset[1] *= k
				key.Move[layer] = t
			}
		}
	}
}
