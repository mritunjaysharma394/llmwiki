package ingest

import (
	"fmt"
	"os"
	"strings"

	"github.com/ledongthuc/pdf"
)

// PDFMinTextPerPage is the minimum extracted text length below which a page is
// treated as scanned/OCR-only and skipped with a warning.
const PDFMinTextPerPage = 50

// ReadPDF extracts text per page using ledongthuc/pdf's GetTextByRow API.
// Each page becomes one SourceFile with RelativePath "page-N". Pages with
// fewer than PDFMinTextPerPage characters of extractable text (likely scanned
// images) are skipped with a warning. If every page is skipped, returns an
// error explaining the PDF is likely scanned/OCR-only.
func ReadPDF(path string) ([]SourceFile, error) {
	f, r, err := pdf.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open pdf %s: %w", path, err)
	}
	defer f.Close()

	n := r.NumPage()
	var out []SourceFile
	for i := 1; i <= n; i++ {
		page := r.Page(i)
		if page.V.IsNull() {
			continue
		}

		text := extractPageText(page)
		if len(text) < PDFMinTextPerPage {
			fmt.Fprintf(os.Stderr, "  WARN page %d of %s: appears to be scanned/OCR-only (%d chars), skipping\n", i, path, len(text))
			continue
		}
		out = append(out, NewSourceFile(fmt.Sprintf("page-%d", i), []byte(text)))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no extractable text in PDF (likely scanned): %s", path)
	}
	return out, nil
}

// extractPageText pulls plain text from a single PDF page. Tries the row-aware
// API first (better layout fidelity); falls back to GetPlainText if rows return
// empty or error, since some PDFs decode under one path but not the other.
func extractPageText(page pdf.Page) string {
	rows, err := page.GetTextByRow()
	if err == nil && len(rows) > 0 {
		var sb strings.Builder
		for _, row := range rows {
			for _, w := range row.Content {
				sb.WriteString(w.S)
			}
			sb.WriteString("\n")
		}
		if t := strings.TrimSpace(sb.String()); t != "" {
			return t
		}
	}
	// Fallback: GetPlainText interprets BT/ET and Tj/TJ ops without spatial info.
	if plain, err := page.GetPlainText(nil); err == nil {
		return strings.TrimSpace(plain)
	}
	return ""
}
