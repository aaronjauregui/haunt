package files

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"
)

// decodePNG decodes the output of prepareImage, failing the test if it is
// not a PNG.
func decodePNG(t *testing.T, b []byte) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("result is not a PNG: %v", err)
	}
	return img
}

func prepare(t *testing.T, b []byte) ([]byte, error) {
	t.Helper()
	return prepareImage(bytes.NewReader(b), int64(len(b)))
}

// pngHeader is a PNG signature and IHDR chunk claiming w×h pixels, with no
// image data after it: DecodeConfig accepts it, Decode would not.
func pngHeader(w, h uint32) []byte {
	var buf bytes.Buffer
	buf.WriteString("\x89PNG\r\n\x1a\n")
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8], ihdr[9] = 8, 2 // 8-bit RGB
	chunk := append([]byte("IHDR"), ihdr...)
	binary.Write(&buf, binary.BigEndian, uint32(len(ihdr)))
	buf.Write(chunk)
	binary.Write(&buf, binary.BigEndian, crc32.ChecksumIEEE(chunk))
	return buf.Bytes()
}

func TestAHugePictureIsRefusedFromItsHeader(t *testing.T) {
	_, err := prepare(t, pngHeader(10000, 10000))
	if !errors.Is(err, errImageTooManyPix) {
		t.Fatalf("err = %v, want %v", err, errImageTooManyPix)
	}
}

func TestAFileOverTheCapIsRefusedUnread(t *testing.T) {
	r := &countingReader{}
	_, err := prepareImage(r, imageMaxBytes+1)
	if !errors.Is(err, errImageTooBig) {
		t.Fatalf("err = %v, want %v", err, errImageTooBig)
	}
	if r.n != 0 {
		t.Errorf("read %d bytes of a file over the cap", r.n)
	}
}

func TestAFileThatGrowsPastTheCapIsRefused(t *testing.T) {
	data := make([]byte, imageMaxBytes+10)
	copy(data, pngHeader(10, 10))
	_, err := prepareImage(bytes.NewReader(data), 100)
	if !errors.Is(err, errImageTooBig) {
		t.Fatalf("err = %v, want %v", err, errImageTooBig)
	}
}

type countingReader struct{ n int }

func (c *countingReader) Read(p []byte) (int, error) {
	c.n += len(p)
	return len(p), nil
}

func TestABigJPEGIsScaledToTheLongEdge(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 4000, 3000))
	for y := 0; y < 3000; y += 7 {
		for x := 0; x < 4000; x++ {
			src.Set(x, y, color.RGBA{uint8(x), uint8(y), 90, 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, src, &jpeg.Options{Quality: 60}); err != nil {
		t.Fatal(err)
	}
	out, err := prepare(t, buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	b := decodePNG(t, out).Bounds()
	if b.Dx() != 1568 || b.Dy() != 1176 {
		t.Errorf("size = %dx%d, want 1568x1176", b.Dx(), b.Dy())
	}
}

func TestATallPictureIsScaledByItsHeight(t *testing.T) {
	var buf bytes.Buffer
	png.Encode(&buf, image.NewGray(image.Rect(0, 0, 1000, 3136)))
	out, err := prepare(t, buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	b := decodePNG(t, out).Bounds()
	if b.Dx() != 500 || b.Dy() != 1568 {
		t.Errorf("size = %dx%d, want 500x1568", b.Dx(), b.Dy())
	}
}

func TestASmallPictureIsNotUpscaled(t *testing.T) {
	var buf bytes.Buffer
	png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 300, 200)))
	out, err := prepare(t, buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	b := decodePNG(t, out).Bounds()
	if b.Dx() != 300 || b.Dy() != 200 {
		t.Errorf("size = %dx%d, want 300x200", b.Dx(), b.Dy())
	}
}

func TestAnAnimatedGIFUsesItsFirstFrame(t *testing.T) {
	pal := color.Palette{color.RGBA{255, 0, 0, 255}, color.RGBA{0, 0, 255, 255}}
	frame := func(i uint8) *image.Paletted {
		f := image.NewPaletted(image.Rect(0, 0, 8, 8), pal)
		for p := range f.Pix {
			f.Pix[p] = i
		}
		return f
	}
	var buf bytes.Buffer
	anim := &gif.GIF{Image: []*image.Paletted{frame(0), frame(1)}, Delay: []int{10, 10}}
	if err := gif.EncodeAll(&buf, anim); err != nil {
		t.Fatal(err)
	}
	out, err := prepare(t, buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, _ := decodePNG(t, out).At(4, 4).RGBA()
	if r>>8 != 255 || g != 0 || b != 0 {
		t.Errorf("pixel = %d,%d,%d, want the first frame's red", r>>8, g>>8, b>>8)
	}
}

func TestAWebPIsRead(t *testing.T) {
	// A 3x2 lossless WebP, made with Pillow.
	data, _ := hex.DecodeString("524946461e000000574542505650384c110000002f0240000007508f2217a5ff8188e87f0000")
	out, err := prepare(t, data)
	if err != nil {
		t.Fatal(err)
	}
	if b := decodePNG(t, out).Bounds(); b.Dx() != 3 || b.Dy() != 2 {
		t.Errorf("size = %dx%d, want 3x2", b.Dx(), b.Dy())
	}
}

func TestATruncatedJPEGIsAnError(t *testing.T) {
	var buf bytes.Buffer
	jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 200, 200)), nil)
	data := buf.Bytes()[:buf.Len()/2]
	if _, err := prepare(t, data); !errors.Is(err, errImageUnreadable) {
		t.Fatalf("err = %v, want %v", err, errImageUnreadable)
	}
}

func TestSomethingElseIsNotAPicture(t *testing.T) {
	for name, data := range map[string][]byte{
		"text":  []byte("just some notes\n"),
		"bmp":   append([]byte("BM"), make([]byte, 64)...),
		"empty": nil,
	} {
		if _, err := prepare(t, data); !errors.Is(err, errImageUnsupported) {
			t.Errorf("%s: err = %v, want %v", name, err, errImageUnsupported)
		}
	}
}
