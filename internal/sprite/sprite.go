// Package sprite loads shell artwork and derives the data the renderer
// needs from it: premultiplied pixel buffers and alpha-based input regions.
package sprite

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
)

// Sprite is a decoded, ready-to-render image.
type Sprite struct {
	Img *image.NRGBA
}

// Load reads a PNG file into a Sprite.
func Load(path string) (*Sprite, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("decoding %s: %w", path, err)
	}
	nrgba := image.NewNRGBA(img.Bounds())
	draw.Draw(nrgba, nrgba.Bounds(), img, img.Bounds().Min, draw.Src)
	return &Sprite{Img: nrgba}, nil
}

// Size returns the sprite dimensions in pixels.
func (s *Sprite) Size() (w, h int) {
	b := s.Img.Bounds()
	return b.Dx(), b.Dy()
}

// WriteBGRA fills dst with premultiplied BGRA pixels (wl_shm ARGB8888 on a
// little-endian machine). dst must be at least 4*w*h bytes.
func (s *Sprite) WriteBGRA(dst []byte) {
	b := s.Img.Bounds()
	w, h := b.Dx(), b.Dy()
	for y := 0; y < h; y++ {
		row := s.Img.Pix[y*s.Img.Stride : y*s.Img.Stride+w*4]
		out := dst[y*w*4 : (y+1)*w*4]
		for x := 0; x < w; x++ {
			r, g, bl, a := row[x*4], row[x*4+1], row[x*4+2], row[x*4+3]
			// Premultiply.
			out[x*4] = uint8(uint32(bl) * uint32(a) / 255)
			out[x*4+1] = uint8(uint32(g) * uint32(a) / 255)
			out[x*4+2] = uint8(uint32(r) * uint32(a) / 255)
			out[x*4+3] = a
		}
	}
}

// OpaqueRects returns per-row runs of pixels whose alpha is >= threshold, in
// sprite-local coordinates. These become the surface's input region so
// clicks on transparent pixels pass through to windows beneath.
func (s *Sprite) OpaqueRects(threshold uint8) []image.Rectangle {
	b := s.Img.Bounds()
	w, h := b.Dx(), b.Dy()
	var rects []image.Rectangle
	for y := 0; y < h; y++ {
		start := -1
		for x := 0; x < w; x++ {
			a := s.Img.Pix[y*s.Img.Stride+x*4+3]
			if a >= threshold {
				if start < 0 {
					start = x
				}
			} else if start >= 0 {
				rects = append(rects, image.Rect(start, y, x, y+1))
				start = -1
			}
		}
		if start >= 0 {
			rects = append(rects, image.Rect(start, y, w, y+1))
		}
	}
	return mergeVertical(rects)
}

// mergeVertical coalesces identical runs on adjacent rows into taller
// rectangles to keep the region small.
func mergeVertical(rects []image.Rectangle) []image.Rectangle {
	var out []image.Rectangle
	// open maps [minX,maxX) of a run on the previous row to its index in out.
	open := map[[2]int]int{}
	row := -1
	var next map[[2]int]int
	for _, r := range rects {
		if r.Min.Y != row {
			if r.Min.Y == row+1 {
				open = next
			} else {
				open = map[[2]int]int{}
			}
			next = map[[2]int]int{}
			row = r.Min.Y
		}
		key := [2]int{r.Min.X, r.Max.X}
		if i, ok := open[key]; ok && out[i].Max.Y == r.Min.Y {
			out[i].Max.Y = r.Max.Y
			next[key] = i
		} else {
			out = append(out, r)
			next[key] = len(out) - 1
		}
	}
	return out
}

// Builtin returns a generated placeholder sprite: a round ghost blob with
// eyes on a transparent background. Used when no shell art is supplied.
func Builtin(size int) *Sprite {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	cx, cy := float64(size)/2, float64(size)/2
	r := float64(size) * 0.45
	body := color.NRGBA{R: 240, G: 240, B: 255, A: 255}
	eye := color.NRGBA{R: 30, G: 30, B: 40, A: 255}
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx, dy := float64(x)+0.5-cx, float64(y)+0.5-cy
			if dx*dx+dy*dy <= r*r {
				img.SetNRGBA(x, y, body)
			}
		}
	}
	er := r * 0.12
	for _, ex := range []float64{cx - r*0.35, cx + r*0.35} {
		ey := cy - r*0.2
		for y := 0; y < size; y++ {
			for x := 0; x < size; x++ {
				dx, dy := float64(x)+0.5-ex, float64(y)+0.5-ey
				if dx*dx+dy*dy <= er*er {
					img.SetNRGBA(x, y, eye)
				}
			}
		}
	}
	return &Sprite{Img: img}
}
