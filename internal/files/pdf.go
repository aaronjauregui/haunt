package files

import (
	"context"
	"errors"
	"io"
)

// extractPDF returns the text layer of the PDF in r, which is size bytes
// long, and how many pages it has. A PDF with no text layer gives empty
// text and no error.
func extractPDF(ctx context.Context, r io.ReaderAt, size int64) (text string, pages int, err error) {
	return "", 0, errors.New("PDFs cannot be read yet")
}
