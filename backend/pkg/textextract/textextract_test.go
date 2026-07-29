package textextract

import (
	"archive/zip"
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestDetectFormat(t *testing.T) {
	cases := []struct {
		name     string
		filename string
		content  []byte
		want     Format
	}{
		{"pdf by extension", "invoice.pdf", []byte("whatever bytes"), FormatPDF},
		{"docx by extension, case-insensitive", "report.DOCX", []byte("whatever bytes"), FormatDOCX},
		{"txt by extension", "notes.txt", []byte("hello"), FormatPlainText},
		{"markdown by extension", "README.md", []byte("# hi"), FormatPlainText},
		{"no extension sniffs pdf magic bytes", "mystery", []byte("%PDF-1.4\n..."), FormatPDF},
		{"no extension, no recognizable content, defaults to plain text", "mystery", []byte("just some bytes"), FormatPlainText},
		{"unrecognized extension defaults to plain text", "data.csv", []byte("a,b,c"), FormatPlainText},
		{"empty content, no extension", "mystery", []byte(""), FormatPlainText},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DetectFormat(c.filename, c.content); got != c.want {
				t.Fatalf("DetectFormat(%q, ...) = %q, want %q", c.filename, got, c.want)
			}
		})
	}

	// A real DOCX (zip containing word/document.xml) should sniff as docx even
	// without the .docx extension, mirroring the pdf-magic-byte case above.
	t.Run("no extension sniffs docx zip structure", func(t *testing.T) {
		docxBytes := buildTestDOCX([]string{"hello"})
		if got := DetectFormat("mystery", docxBytes); got != FormatDOCX {
			t.Fatalf("DetectFormat() = %q, want %q", got, FormatDOCX)
		}
	})
}

func TestExtractPlainTextPassthrough(t *testing.T) {
	text, err := Extract("notes.txt", []byte("hello there, unchanged"))
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if text != "hello there, unchanged" {
		t.Fatalf("Extract() = %q, want passthrough of the original content", text)
	}
}

func TestExtractUnrecognizedFormatPassthrough(t *testing.T) {
	// Anything this package doesn't recognize must behave exactly like plain text did
	// before this package existed — no silent behavior change for existing workflows.
	text, err := Extract("image.png", []byte("raw binary soup"))
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if text != "raw binary soup" {
		t.Fatalf("Extract() = %q, want unchanged passthrough", text)
	}
}

func TestExtractPDF(t *testing.T) {
	pdfBytes := buildTestPDF("Hello From PDF")

	text, err := Extract("document.pdf", pdfBytes)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}

	collapsed := strings.Join(strings.Fields(text), " ")
	if !strings.Contains(collapsed, "Hello From PDF") {
		t.Fatalf("Extract() = %q, want it to contain %q", text, "Hello From PDF")
	}
}

func TestExtractPDFInvalidData(t *testing.T) {
	_, err := Extract("broken.pdf", []byte("this is not a valid pdf file at all"))
	if err == nil {
		t.Fatal("Extract() expected an error for a corrupt PDF, got nil")
	}
}

func TestExtractDOCX(t *testing.T) {
	docxBytes := buildTestDOCX([]string{"Hello World", "Second paragraph"})

	text, err := Extract("document.docx", docxBytes)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if !strings.Contains(text, "Hello World") {
		t.Fatalf("Extract() = %q, want it to contain %q", text, "Hello World")
	}
	if !strings.Contains(text, "Second paragraph") {
		t.Fatalf("Extract() = %q, want it to contain %q", text, "Second paragraph")
	}
}

func TestExtractDOCXInvalidData(t *testing.T) {
	_, err := Extract("broken.docx", []byte("this is not a valid docx file at all"))
	if err == nil {
		t.Fatal("Extract() expected an error for a corrupt DOCX, got nil")
	}
}

// buildTestPDF constructs a minimal, valid single-page PDF containing text inside a
// content stream, computing a correct xref table from real byte offsets. Building it
// in-process (rather than checking in a binary fixture) keeps the fixture's exact
// content — and the reason it's valid — visible right next to the test that uses it.
func buildTestPDF(text string) []byte {
	var buf bytes.Buffer
	offsets := make([]int, 6)
	buf.WriteString("%PDF-1.4\n")

	write := func(i int, s string) {
		offsets[i] = buf.Len()
		buf.WriteString(s)
	}

	write(1, "1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	write(2, "2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")
	write(3, "3 0 obj\n<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 4 0 R >> >> "+
		"/MediaBox [0 0 200 200] /Contents 5 0 R >>\nendobj\n")
	write(4, "4 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n")

	content := fmt.Sprintf("BT /F1 24 Tf 10 100 Td (%s) Tj ET", text)
	write(5, fmt.Sprintf("5 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(content), content))

	xrefStart := buf.Len()
	buf.WriteString("xref\n0 6\n")
	buf.WriteString("0000000000 65535 f \n")
	for i := 1; i <= 5; i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	buf.WriteString("trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n")
	fmt.Fprintf(&buf, "%d\n", xrefStart)
	buf.WriteString("%%EOF")

	return buf.Bytes()
}

// buildTestDOCX constructs a minimal, valid DOCX — a zip archive containing a
// word/document.xml WordprocessingML part — with one paragraph per entry.
func buildTestDOCX(paragraphs []string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	var body strings.Builder
	body.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	body.WriteString(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`)
	for _, p := range paragraphs {
		body.WriteString(`<w:p><w:r><w:t xml:space="preserve">`)
		body.WriteString(p)
		body.WriteString(`</w:t></w:r></w:p>`)
	}
	body.WriteString(`</w:body></w:document>`)

	f, _ := zw.Create("word/document.xml")
	_, _ = f.Write([]byte(body.String()))
	_ = zw.Close()

	return buf.Bytes()
}
