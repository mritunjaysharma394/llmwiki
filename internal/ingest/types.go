package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// SourceFile is one logical "file" inside a source: a single file inside a
// directory/repo, a single page inside a PDF (relative_path = "page-N"), or
// the whole document for an HTML/text source (relative_path = "index.html").
//
// Every Evidence row in the DB is anchored to one SourceFile. Quote line
// numbers are within Content (1-indexed).
type SourceFile struct {
	RelativePath string
	Content      string
	ContentHash  string
	ByteSize     int64
	LineCount    int
}

// NewSourceFile populates Content/Hash/ByteSize/LineCount from raw bytes.
func NewSourceFile(relPath string, content []byte) SourceFile {
	sum := sha256.Sum256(content)
	return SourceFile{
		RelativePath: relPath,
		Content:      string(content),
		ContentHash:  hex.EncodeToString(sum[:]),
		ByteSize:     int64(len(content)),
		LineCount:    countLines(string(content)),
	}
}

// countLines returns 1-indexed-aware line count: "" -> 0, "a" -> 1, "a\n" -> 1,
// "a\nb" -> 2, "a\nb\n" -> 2. Matches what the user perceives as "N lines".
func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}
