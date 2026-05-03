package ingest

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildSimplePDF constructs a minimal valid 2-page text PDF byte stream, with
// the given text on each page. It uses the standard built-in Helvetica font
// (no embedded font program needed), uncompressed content streams, and a
// hand-built xref so ledongthuc/pdf can parse it without surprises.
func buildSimplePDF(page1Text, page2Text string) []byte {
	mkContent := func(text string) string {
		// Standard text-show stream: begin text, choose font/size, position,
		// show each line, end text. Newlines are encoded with T* (next line).
		var sb strings.Builder
		sb.WriteString("BT\n/F1 18 Tf\n72 720 Td\n")
		// Split into lines so each can be a Tj with T* between.
		lines := strings.Split(text, "\n")
		for i, ln := range lines {
			if i > 0 {
				sb.WriteString("0 -22 Td\n")
			}
			// Escape parentheses and backslashes per PDF string syntax.
			esc := strings.NewReplacer(`\`, `\\`, `(`, `\(`, `)`, `\)`).Replace(ln)
			fmt.Fprintf(&sb, "(%s) Tj\n", esc)
		}
		sb.WriteString("ET\n")
		return sb.String()
	}

	c1 := mkContent(page1Text)
	c2 := mkContent(page2Text)

	// We'll emit 7 indirect objects and record their offsets for the xref.
	// 1: Catalog
	// 2: Pages
	// 3: Page 1
	// 4: Page 1 contents
	// 5: Page 2
	// 6: Page 2 contents
	// 7: Font (Helvetica)
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R 5 0 R] /Count 2 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 7 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(c1), c1),
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 6 0 R /Resources << /Font << /F1 7 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(c2), c2),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}

	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	buf.WriteString("%\xe2\xe3\xcf\xd3\n") // binary marker so tools detect it as binary

	offsets := make([]int, len(objs)+1) // 1-indexed
	for i, body := range objs {
		offsets[i+1] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}

	xrefStart := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", len(objs)+1)
	buf.WriteString("0000000000 65535 f \n")
	for i := 1; i <= len(objs); i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xrefStart)
	return buf.Bytes()
}

// writeSimplePDF generates a 2-page text PDF in t.TempDir() and returns its path.
// Each page contains enough text to clear PDFMinTextPerPage so the scanned-page
// heuristic does not trip.
func writeSimplePDF(t *testing.T) string {
	t.Helper()
	page1 := "alpha beta gamma delta epsilon zeta eta theta iota kappa"
	page2 := "lambda mu nu xi omicron pi rho sigma tau upsilon phi chi"
	data := buildSimplePDF(page1, page2)
	path := filepath.Join(t.TempDir(), "simple.pdf")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write simple pdf: %v", err)
	}
	return path
}

func TestReadPDFSimple(t *testing.T) {
	path := writeSimplePDF(t)

	files, err := ReadPDF(path)
	if err != nil {
		t.Fatalf("ReadPDF: %v", err)
	}
	if len(files) < 1 {
		t.Fatalf("got %d pages, want >= 1", len(files))
	}
	if files[0].RelativePath != "page-1" {
		t.Errorf("first page RelativePath = %q, want %q", files[0].RelativePath, "page-1")
	}
	if files[0].Content == "" {
		t.Errorf("page-1 content is empty")
	}
	if files[0].ContentHash == "" {
		t.Errorf("page-1 ContentHash is empty")
	}
	if files[0].LineCount < 1 {
		t.Errorf("page-1 LineCount = %d, want >= 1", files[0].LineCount)
	}
	if files[0].ByteSize <= 0 {
		t.Errorf("page-1 ByteSize = %d, want > 0", files[0].ByteSize)
	}
	// Sanity: at least one of the page-1 marker words should appear in the
	// extracted text. Different decoders capitalize/space differently, so we
	// only check for case-insensitive substring.
	low := strings.ToLower(files[0].Content)
	if !strings.Contains(low, "alpha") && !strings.Contains(low, "beta") &&
		!strings.Contains(low, "gamma") {
		t.Errorf("page-1 content missing expected words: %q", files[0].Content)
	}
	// If page-2 came through, it should be labelled correctly.
	if len(files) >= 2 && files[1].RelativePath != "page-2" {
		t.Errorf("second page RelativePath = %q, want %q", files[1].RelativePath, "page-2")
	}
}

func TestReadPDFNonexistent(t *testing.T) {
	_, err := ReadPDF(filepath.Join(t.TempDir(), "does-not-exist.pdf"))
	if err == nil {
		t.Fatalf("expected error for missing file")
	}
	if !strings.Contains(err.Error(), "open pdf") {
		t.Errorf("error should mention 'open pdf': %v", err)
	}
}
