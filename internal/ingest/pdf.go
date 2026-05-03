package ingest

import "errors"

// ReadPDF extracts a PDF into one SourceFile per page (RelativePath = "page-N").
// The real implementation lands in Phase D (Task 7); for now the walker can
// dispatch to this stub so plumbing is in place.
func ReadPDF(path string) ([]SourceFile, error) {
	return nil, errors.New("PDF support added in Task 7")
}
