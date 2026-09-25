package files

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"

	"golang.org/x/image/draw"
	"golang.org/x/image/webp"
)

// Limits on a picture read from a shared folder. The file cap stops a huge
// file being read into memory at all; the pixel cap is checked against the
// header before anything is decoded, so a small file that claims enormous
// dimensions (a decompression bomb) is refused cheaply; the long edge is
// what the vision model is sent, which is as much as it can use.
const (
	imageMaxBytes    = 20 << 20
	imageMaxPixels   = 40_000_000
	imageLongEdge    = 1568
	imageSniffWindow = 512 // what http.DetectContentType looks at
)

var (
	errImageTooBig      = errors.New("that picture is too big to read (over 20 MB)")
	errImageTooManyPix  = errors.New("that picture is too large to look at (over 40 megapixels)")
	errImageUnsupported = errors.New("that picture is not a PNG, JPEG, GIF or WebP")
	errImageUnreadable  = errors.New("that picture could not be read (it may be damaged)")
)

// imageFormat is one picture format prepareImage accepts.
type imageFormat struct {
	decodeConfig func(io.Reader) (image.Config, error)
	decode       func(io.Reader) (image.Image, error)
}

// imageFormats maps a sniffed content type to its decoder. The decoders are
// named here rather than found through image.Decode's registry, so a format
// is only ever read when the sniff says it is that format. GIF decodes its
// first frame.
var imageFormats = map[string]imageFormat{
	"image/png":  {png.DecodeConfig, png.Decode},
	"image/jpeg": {jpeg.DecodeConfig, jpeg.Decode},
	"image/gif":  {gif.DecodeConfig, gif.Decode},
	"image/webp": {webp.DecodeConfig, webp.Decode},
}

// prepareImage turns a picture file of size bytes into a PNG fit for the
// vision model: it sniffs the format, refuses a file over imageMaxBytes or
// a picture over imageMaxPixels before decoding it, scales the long edge
// down to imageLongEdge (never up) and encodes the result as PNG. Errors
// are worded for the model and carry no path.
func prepareImage(r io.Reader, size int64) ([]byte, error) {
	if size > imageMaxBytes {
		return nil, errImageTooBig
	}
	data, err := io.ReadAll(io.LimitReader(r, imageMaxBytes+1))
	if err != nil {
		return nil, errImageUnreadable
	}
	if len(data) > imageMaxBytes {
		return nil, errImageTooBig
	}

	format, ok := imageFormats[http.DetectContentType(data[:min(len(data), imageSniffWindow)])]
	if !ok {
		return nil, errImageUnsupported
	}
	cfg, err := format.decodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, errImageUnreadable
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, errImageUnreadable
	}
	if int64(cfg.Width)*int64(cfg.Height) > imageMaxPixels {
		return nil, errImageTooManyPix
	}

	img, err := format.decode(bytes.NewReader(data))
	if err != nil {
		return nil, errImageUnreadable
	}
	img = fitLongEdge(img, imageLongEdge)

	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, fmt.Errorf("that picture could not be prepared: %w", err)
	}
	return out.Bytes(), nil
}

// fitLongEdge scales img so its longer side is at most edge pixels, keeping
// the aspect ratio. A picture already small enough is returned as it is.
func fitLongEdge(img image.Image, edge int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= edge && h <= edge {
		return img
	}
	nw, nh := edge, max(1, h*edge/w)
	if h > w {
		nw, nh = max(1, w*edge/h), edge
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	return dst
}
