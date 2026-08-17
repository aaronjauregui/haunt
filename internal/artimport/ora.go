// Package artimport turns a layered drawing into a Haunt shell: cropped
// PNGs and the `[shell]` TOML that arranges them.
//
// This exists because authoring is the binding constraint on the project.
// The driver can do more than anyone can currently draw for it, and the
// wall in the way is not the art — it is hand-writing a layer stack in TOML
// and hand-cropping every PNG to the pixels it covers. Both of those are
// already implied by the drawing: an artist who has a file with an `eyes`
// group containing `open` and `closed` has said everything the shell format
// needs to hear.
//
// OpenRaster (.ora) is the input because it is a ZIP of PNGs with an XML
// manifest — readable with nothing but the standard library, which suits a
// project that avoids cgo — and because Krita, GIMP, MyPaint and Pencil2D
// all write it. PSD is the obvious other candidate and is deliberately not
// here: it is a large binary format with several compression schemes, and
// parsing it properly is a project rather than a file. The seam for it is
// Stack: give it one from anywhere and the rest of this package applies.
package artimport

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"image"
	"image/png"
	"io"
	"strings"
)

// Stack is a layered drawing: a canvas and the layers on it, bottom first.
type Stack struct {
	W, H   int
	Layers []Art
}

// Art is one piece of drawn material with its place on the canvas.
type Art struct {
	// Name is the layer's name in the drawing, as the artist typed it.
	Name string
	// Group is the enclosing group's name, empty for a top-level layer.
	// Groups are how variants are expressed: an `eyes` group holding
	// `open` and `closed` is a layer with two variants.
	Group string
	// X, Y place the art on the canvas.
	X, Y int
	// Visible reflects the layer's visibility in the drawing. Within a
	// group it picks the default variant; at the top level, a hidden layer
	// is taken to be a sketch or reference and left out.
	Visible bool
	Img     image.Image
}

// oraImage is stack.xml. Layers are listed top first, which is the reverse
// of the order they are drawn in.
type oraImage struct {
	XMLName xml.Name `xml:"image"`
	W       int      `xml:"w,attr"`
	H       int      `xml:"h,attr"`
	Stack   oraStack `xml:"stack"`
}

type oraStack struct {
	Name string `xml:"name,attr"`
	// Children are layers and nested stacks in one list, because document
	// order is drawing order and splitting them by kind would lose it.
	Children []oraNode `xml:",any"`
}

// oraNode is a layer or a stack; XMLName says which.
type oraNode struct {
	XMLName  xml.Name
	Name     string    `xml:"name,attr"`
	Src      string    `xml:"src,attr"`
	X        int       `xml:"x,attr"`
	Y        int       `xml:"y,attr"`
	Vis      string    `xml:"visibility,attr"`
	Children []oraNode `xml:",any"`
}

// ReadORA opens an OpenRaster file and returns its layers, bottom first.
func ReadORA(path string) (*Stack, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	defer zr.Close()

	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
	}
	if mt, ok := files["mimetype"]; ok {
		if data, err := readAll(mt); err == nil && strings.TrimSpace(string(data)) != "image/openraster" {
			return nil, fmt.Errorf("%s is not an OpenRaster file (mimetype %q)", path, data)
		}
	}
	sx, ok := files["stack.xml"]
	if !ok {
		return nil, fmt.Errorf("%s has no stack.xml; is it an OpenRaster file?", path)
	}
	data, err := readAll(sx)
	if err != nil {
		return nil, err
	}
	var img oraImage
	if err := xml.Unmarshal(data, &img); err != nil {
		return nil, fmt.Errorf("parsing stack.xml: %w", err)
	}

	st := &Stack{W: img.W, H: img.H}
	if err := collect(img.Stack.Children, "", files, st); err != nil {
		return nil, err
	}
	// stack.xml is top first; drawing order is the other way round.
	for i, j := 0, len(st.Layers)-1; i < j; i, j = i+1, j-1 {
		st.Layers[i], st.Layers[j] = st.Layers[j], st.Layers[i]
	}
	return st, nil
}

// collect walks the stack in document order, one group deep. Deeper nesting
// is flattened into the outermost group, because the shell format has
// exactly two levels — a layer and its variants — and inventing a mapping
// for a third would be guessing at what the artist meant.
func collect(nodes []oraNode, group string, files map[string]*zip.File, st *Stack) error {
	for _, n := range nodes {
		switch n.XMLName.Local {
		case "layer":
			f, ok := files[n.Src]
			if !ok {
				return fmt.Errorf("layer %q refers to %s, which is not in the archive", n.Name, n.Src)
			}
			data, err := readAll(f)
			if err != nil {
				return err
			}
			decoded, err := png.Decode(bytes.NewReader(data))
			if err != nil {
				return fmt.Errorf("decoding layer %q (%s): %w", n.Name, n.Src, err)
			}
			st.Layers = append(st.Layers, Art{
				Name:    n.Name,
				Group:   group,
				X:       n.X,
				Y:       n.Y,
				Visible: n.Vis != "hidden",
				Img:     decoded,
			})
		case "stack":
			inner := group
			if inner == "" {
				inner = n.Name
			}
			if err := collect(n.Children, inner, files, st); err != nil {
				return err
			}
		}
	}
	return nil
}

func readAll(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}
