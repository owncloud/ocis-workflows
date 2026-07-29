// Package textextract turns binary document formats (PDF, DOCX) into plain text so
// downstream nodes — an LLM Prompt node in particular — can work with a document's
// actual content instead of that format's raw bytes stringified as-is.
//
// Scope note: image-based text (scanned PDFs, photos of a page) is explicitly out of
// scope here. Recovering text from an image needs either a bundled OCR engine or a
// call out to an external OCR service — a meaningfully bigger lift than parsing an
// already-textual document format. That remains a natural, separate follow-up; nothing
// in this package attempts it.
package textextract

import (
	"archive/zip"
	"bytes"
	"path/filepath"
	"strings"
)

// Format identifies which extraction strategy a document's content needs.
type Format string

const (
	// FormatPlainText covers content that is already readable as text as-is (.txt,
	// .md, ...) as well as anything this package doesn't otherwise recognize — the
	// safe default that preserves the executor's pre-existing behavior.
	FormatPlainText Format = "plaintext"
	// FormatPDF is Portable Document Format content.
	FormatPDF Format = "pdf"
	// FormatDOCX is Office Open XML WordprocessingML content (.docx).
	FormatDOCX Format = "docx"
)

// DetectFormat guesses a document's format. It prefers filename's extension and falls
// back to sniffing content's magic bytes/structure when the extension is missing or
// not one this package recognizes (e.g. a file uploaded without an extension, or
// served through a route that only forwards raw bytes).
func DetectFormat(filename string, content []byte) Format {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".pdf":
		return FormatPDF
	case ".docx":
		return FormatDOCX
	}

	if bytes.HasPrefix(content, []byte("%PDF-")) {
		return FormatPDF
	}
	if isDOCX(content) {
		return FormatDOCX
	}
	return FormatPlainText
}

// Extract converts content to plain text based on its detected format. Plain-text and
// unrecognized formats are returned unchanged, so existing .txt/.md-driven workflows
// keep working exactly as they did before this package existed.
func Extract(filename string, content []byte) (string, error) {
	switch DetectFormat(filename, content) {
	case FormatPDF:
		return extractPDF(content)
	case FormatDOCX:
		return extractDOCX(content)
	default:
		return string(content), nil
	}
}

// isDOCX reports whether content looks like an OOXML WordprocessingML package: a zip
// archive containing a word/document.xml part. This distinguishes .docx from other
// zip-based formats (.xlsx, .pptx, plain .zip) without relying on the file extension.
func isDOCX(content []byte) bool {
	if !bytes.HasPrefix(content, []byte("PK\x03\x04")) {
		return false
	}
	zr, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return false
	}
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			return true
		}
	}
	return false
}
