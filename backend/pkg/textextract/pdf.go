package textextract

import (
	"bytes"
	"fmt"
	"io"

	"github.com/ledongthuc/pdf"
)

// extractPDF renders a PDF's text content into a single plain-text string.
//
// github.com/ledongthuc/pdf is a pure-Go, BSD-3-Clause-licensed PDF parser (a
// maintained fork of the Go team's own rsc.io/pdf) — no cgo, no bundled binary, and a
// license compatible with this project's existing (BSD/MIT-style) dependencies.
func extractPDF(content []byte) (string, error) {
	reader, err := pdf.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return "", fmt.Errorf("textextract: opening pdf: %w", err)
	}

	textReader, err := reader.GetPlainText()
	if err != nil {
		return "", fmt.Errorf("textextract: reading pdf text: %w", err)
	}

	text, err := io.ReadAll(textReader)
	if err != nil {
		return "", fmt.Errorf("textextract: reading pdf text: %w", err)
	}
	return string(text), nil
}
