package seriko

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strings"
)

// Transparency, which is the part a naive importer gets wrong and produces
// a rectangle.
//
// SERIKO images predate widespread alpha. Two conventions carry it:
//
//   - A `.pna` beside the `.png`: a greyscale image of the same size whose
//     brightness is the alpha channel. Where a shell ships one it is
//     authoritative, and ignoring it gives you an opaque box.
//   - Otherwise the pixel at (0,0) is the transparent colour, and every
//     pixel matching it exactly is cut out. It is why so many shells have
//     a lurid magenta or green corner.
//
// A PNG that already carries alpha is left alone: modern shells exist, and
// second-guessing them would punch holes in art that was drawn correctly.

// LoadSurface decodes a surface image and applies whichever transparency
// convention it uses. pna may be nil.
func LoadSurface(img, pna []byte) (*image.NRGBA, error) {
	src, err := png.Decode(bytes.NewReader(img))
	if err != nil {
		return nil, fmt.Errorf("decoding surface image: %w", err)
	}
	b := src.Bounds()
	out := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(out, out.Bounds(), src, b.Min, draw.Src)

	if len(pna) > 0 {
		if err := applyPNA(out, pna); err != nil {
			return nil, err
		}
		return out, nil
	}
	if hasAlpha(out) {
		return out, nil
	}
	colorKey(out)
	return out, nil
}

// applyPNA multiplies in a .pna alpha mask.
func applyPNA(dst *image.NRGBA, pna []byte) error {
	mask, err := png.Decode(bytes.NewReader(pna))
	if err != nil {
		return fmt.Errorf("decoding the .pna alpha mask: %w", err)
	}
	mb := mask.Bounds()
	db := dst.Bounds()
	if mb.Dx() != db.Dx() || mb.Dy() != db.Dy() {
		return fmt.Errorf("the .pna mask is %dx%d but the surface is %dx%d",
			mb.Dx(), mb.Dy(), db.Dx(), db.Dy())
	}
	for y := 0; y < db.Dy(); y++ {
		for x := 0; x < db.Dx(); x++ {
			r, g, bl, _ := mask.At(mb.Min.X+x, mb.Min.Y+y).RGBA()
			// Greyscale by construction; averaging is harmless if it is not
			// and avoids trusting one channel of a mask drawn in colour.
			a := uint8(((r + g + bl) / 3) >> 8)
			i := dst.PixOffset(x, y)
			dst.Pix[i+3] = a
		}
	}
	return nil
}

// hasAlpha reports whether an image already carries real transparency.
func hasAlpha(img *image.NRGBA) bool {
	for i := 3; i < len(img.Pix); i += 4 {
		if img.Pix[i] != 0xff {
			return true
		}
	}
	return false
}

// colorKey cuts out every pixel matching the one at the top-left corner.
func colorKey(img *image.NRGBA) {
	if len(img.Pix) < 4 {
		return
	}
	kr, kg, kb := img.Pix[0], img.Pix[1], img.Pix[2]
	for i := 0; i < len(img.Pix); i += 4 {
		if img.Pix[i] == kr && img.Pix[i+1] == kg && img.Pix[i+2] == kb {
			img.Pix[i+3] = 0
		}
	}
}

// composite draws one element over a base using the SERIKO drawing method
// that comes closest in this format. The exotic methods are approximated
// rather than refused: a shell that uses overlaymultiply for a shadow
// should import with a slightly wrong shadow, not fail to import.
func composite(dst *image.NRGBA, src image.Image, at image.Point, method string) {
	r := image.Rectangle{Min: at, Max: at.Add(src.Bounds().Size())}
	switch method {
	case "base", "replace", "asis":
		// Replace, transparency and all.
		draw.Draw(dst, r, src, src.Bounds().Min, draw.Src)
	case "reduce":
		// Crop the base to where the new layer is opaque.
		reduce(dst, src, at)
	default:
		// overlay, interpolate, overlayfast, overlaymultiply, add, bind.
		draw.Draw(dst, r, src, src.Bounds().Min, draw.Over)
	}
}

// reduce keeps the base only where src is opaque.
func reduce(dst *image.NRGBA, src image.Image, at image.Point) {
	b := dst.Bounds()
	sb := src.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			sx, sy := x-at.X+sb.Min.X, y-at.Y+sb.Min.Y
			var a uint32
			if image.Pt(sx, sy).In(sb) {
				_, _, _, a = src.At(sx, sy).RGBA()
			}
			if a == 0 {
				i := dst.PixOffset(x, y)
				dst.Pix[i+3] = 0
			}
		}
	}
}

// blank is a fully transparent image of a given size, which is how a
// variant says "nothing here": SERIKO clears an animation frame with a
// surface ID of -1, and a haunt layer needs something to wear instead.
func blank(w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	// NRGBA zeroes to fully transparent already; being explicit costs
	// nothing and documents the intent.
	draw.Draw(img, img.Bounds(), image.NewUniform(color.NRGBA{}), image.Point{}, draw.Src)
	return img
}

// encodePNG is how converted art leaves this package.
func encodePNG(img *image.NRGBA) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// safeName tidies a SERIKO name into something usable as a TOML key, a
// file name and a pose selector.
func safeName(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '_' || r == '-':
			b.WriteRune(r)
		case r == ' ' || r == '.' || r == '/':
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}
