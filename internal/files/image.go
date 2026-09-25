package files

import (
	"errors"
	"io"
)

// prepareImage decodes the picture in r, which is size bytes long, and
// returns it as a PNG small enough to show the vision model.
func prepareImage(r io.Reader, size int64) ([]byte, error) {
	return nil, errors.New("pictures cannot be read yet")
}
