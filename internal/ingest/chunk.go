package ingest

import (
	"fmt"
	"strings"
)

// Chunk is a single LLM-call payload made of one or more SourceFile excerpts.
type Chunk struct {
	Header string       // human-readable description for progress display
	Text   string       // payload sent to LLM, includes "=== path ===" file headers
	Files  []SourceFile // SourceFiles included (or partially included) in this chunk
}

// ChunkSourceFiles greedily bin-packs files into chunks under maxBytes.
// File boundaries are preserved with "=== path ===" headers. If a single file
// exceeds maxBytes, it is split on line boundaries; each split chunk's header
// includes "(lines a-b)" so the LLM and a human reviewer know which slice they
// are seeing. The validator still uses (quote, source_file) and computes line
// numbers within the original full file content — split annotations are
// advisory only.
func ChunkSourceFiles(files []SourceFile, maxBytes int) []Chunk {
	if len(files) == 0 {
		return nil
	}
	if maxBytes <= 0 {
		maxBytes = 16 * 1024
	}

	var out []Chunk
	var cur strings.Builder
	var curFiles []SourceFile

	flush := func() {
		if cur.Len() == 0 {
			return
		}
		text := cur.String()
		out = append(out, Chunk{
			Header: chunkHeaderFor(curFiles),
			Text:   text,
			Files:  curFiles,
		})
		cur.Reset()
		curFiles = nil
	}

	for _, f := range files {
		block := fmt.Sprintf("=== %s ===\n%s\n\n", f.RelativePath, f.Content)
		if len(block) <= maxBytes {
			// Fits whole; flush current if it would overflow.
			if cur.Len()+len(block) > maxBytes {
				flush()
			}
			cur.WriteString(block)
			curFiles = append(curFiles, f)
			continue
		}
		// Oversized — split on line boundaries.
		flush()
		out = append(out, splitFileOnLineBoundaries(f, maxBytes)...)
	}
	flush()
	return out
}

func chunkHeaderFor(files []SourceFile) string {
	if len(files) == 1 {
		return files[0].RelativePath
	}
	if len(files) == 0 {
		return "(empty)"
	}
	return fmt.Sprintf("%s + %d more", files[0].RelativePath, len(files)-1)
}

// splitFileOnLineBoundaries produces consecutive Chunks each containing one
// slice of f.Content. Headers carry "(lines a-b)" annotations.
func splitFileOnLineBoundaries(f SourceFile, maxBytes int) []Chunk {
	lines := strings.SplitAfter(f.Content, "\n") // keeps \n
	var out []Chunk

	var buf strings.Builder
	startLine := 1
	curLine := 1
	for _, ln := range lines {
		if ln == "" {
			// SplitAfter on a string ending in "\n" produces a trailing empty
			// element; skip it so we don't bump curLine past the real end.
			continue
		}
		// Header overhead per chunk is bounded; account for a generous 64 bytes.
		if buf.Len()+len(ln)+64 > maxBytes && buf.Len() > 0 {
			endLine := curLine - 1
			if endLine < startLine {
				endLine = startLine
			}
			out = append(out, makeSplitChunk(f, buf.String(), startLine, endLine))
			buf.Reset()
			startLine = curLine
		}
		buf.WriteString(ln)
		curLine++
	}
	if buf.Len() > 0 {
		endLine := curLine - 1
		if endLine < startLine {
			endLine = startLine
		}
		out = append(out, makeSplitChunk(f, buf.String(), startLine, endLine))
	}
	return out
}

func makeSplitChunk(f SourceFile, body string, startLine, endLine int) Chunk {
	text := fmt.Sprintf("=== %s (lines %d-%d) ===\n%s\n\n", f.RelativePath, startLine, endLine, body)
	return Chunk{
		Header: fmt.Sprintf("%s (lines %d-%d)", f.RelativePath, startLine, endLine),
		Text:   text,
		Files:  []SourceFile{f}, // entire SourceFile is the validation anchor; line numbers are within the full Content
	}
}
