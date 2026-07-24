package textextract

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// extractDOCX unzips a DOCX file (an OOXML WordprocessingML package) and pulls the
// plain text out of its main document part, word/document.xml.
//
// This is implemented directly on the standard library (archive/zip,
// encoding/xml) rather than pulling in a third-party DOCX library: a .docx is just a
// zip file with an XML part, plain-text extraction from it needs nothing beyond
// unzip-and-walk-the-XML, and staying on the standard library keeps this package's new
// dependency surface limited to the one place (PDF) that genuinely needs a parser.
func extractDOCX(content []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return "", fmt.Errorf("textextract: opening docx: %w", err)
	}

	var docPart *zip.File
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			docPart = f
			break
		}
	}
	if docPart == nil {
		return "", fmt.Errorf("textextract: docx has no word/document.xml part")
	}

	rc, err := docPart.Open()
	if err != nil {
		return "", fmt.Errorf("textextract: opening docx document part: %w", err)
	}
	defer func() { _ = rc.Close() }()

	data, err := io.ReadAll(rc)
	if err != nil {
		return "", fmt.Errorf("textextract: reading docx document part: %w", err)
	}

	return parseWordprocessingXML(data)
}

// parseWordprocessingXML walks WordprocessingML markup, concatenating the text inside
// each <w:t> run and inserting a newline at each paragraph (<w:p>) boundary.
func parseWordprocessingXML(data []byte) (string, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var sb strings.Builder
	inText := false

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("textextract: parsing docx xml: %w", err)
		}

		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "t" {
				inText = true
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				sb.WriteString("\n")
			}
		case xml.CharData:
			if inText {
				sb.Write(t)
			}
		}
	}

	return strings.TrimRight(sb.String(), "\n"), nil
}
