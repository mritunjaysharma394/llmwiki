package ingest

import (
	"strings"
	"testing"
)

func TestChunkSourceFilesEmpty(t *testing.T) {
	got := ChunkSourceFiles(nil, 1024)
	if len(got) != 0 {
		t.Errorf("nil input -> %d chunks, want 0", len(got))
	}
	got = ChunkSourceFiles([]SourceFile{}, 1024)
	if len(got) != 0 {
		t.Errorf("empty slice -> %d chunks, want 0", len(got))
	}
}

func TestChunkSourceFilesSingleSmall(t *testing.T) {
	f := NewSourceFile("a.md", []byte("hello"))
	got := ChunkSourceFiles([]SourceFile{f}, 1024)
	if len(got) != 1 {
		t.Fatalf("got %d chunks, want 1", len(got))
	}
	if !strings.Contains(got[0].Text, "=== a.md ===") {
		t.Errorf("missing header: %q", got[0].Text)
	}
	if !strings.Contains(got[0].Text, "hello") {
		t.Errorf("missing body: %q", got[0].Text)
	}
	if len(got[0].Files) != 1 || got[0].Files[0].RelativePath != "a.md" {
		t.Errorf("Files anchor = %+v", got[0].Files)
	}
}

func TestChunkSourceFilesPacksMultiple(t *testing.T) {
	files := []SourceFile{
		NewSourceFile("a.md", []byte(strings.Repeat("a", 30))),
		NewSourceFile("b.md", []byte(strings.Repeat("b", 30))),
		NewSourceFile("c.md", []byte(strings.Repeat("c", 30))),
	}
	// Budget enough for two file blocks, not three.
	got := ChunkSourceFiles(files, 100)
	if len(got) != 2 {
		t.Fatalf("got %d chunks, want 2 (texts: %v)", len(got), chunkTexts(got))
	}
	if len(got[0].Files) != 2 {
		t.Errorf("chunk[0] anchors = %d, want 2", len(got[0].Files))
	}
	if len(got[1].Files) != 1 {
		t.Errorf("chunk[1] anchors = %d, want 1", len(got[1].Files))
	}
}

func TestChunkSourceFilesTwoSmallFilesPackTogether(t *testing.T) {
	files := []SourceFile{
		NewSourceFile("a.md", []byte("alpha")),
		NewSourceFile("b.md", []byte("beta")),
	}
	got := ChunkSourceFiles(files, 1024)
	if len(got) != 1 {
		t.Fatalf("got %d chunks, want 1", len(got))
	}
	if !strings.Contains(got[0].Text, "=== a.md ===") {
		t.Errorf("missing a.md header in chunk text")
	}
	if !strings.Contains(got[0].Text, "=== b.md ===") {
		t.Errorf("missing b.md header in chunk text")
	}
	if len(got[0].Files) != 2 {
		t.Errorf("anchors = %d, want 2", len(got[0].Files))
	}
}

func TestChunkSourceFilesEachChunkHasHeaderForEachFile(t *testing.T) {
	files := []SourceFile{
		NewSourceFile("a.md", []byte(strings.Repeat("a", 20))),
		NewSourceFile("b.md", []byte(strings.Repeat("b", 20))),
		NewSourceFile("c.md", []byte(strings.Repeat("c", 20))),
		NewSourceFile("d.md", []byte(strings.Repeat("d", 20))),
	}
	got := ChunkSourceFiles(files, 80)
	if len(got) == 0 {
		t.Fatal("expected at least one chunk")
	}
	for i, c := range got {
		if len(c.Files) == 0 {
			t.Errorf("chunk %d has no anchor files", i)
			continue
		}
		for _, f := range c.Files {
			needle := "=== " + f.RelativePath
			if !strings.Contains(c.Text, needle) {
				t.Errorf("chunk %d missing header for %q in text:\n%s", i, f.RelativePath, c.Text)
			}
		}
	}
}

func TestChunkSourceFilesSplitsOversizedOnLineBoundary(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 100; i++ {
		sb.WriteString("line ")
		sb.WriteString(strings.Repeat("x", 20))
		sb.WriteString("\n")
	}
	f := NewSourceFile("big.txt", []byte(sb.String()))
	got := ChunkSourceFiles([]SourceFile{f}, 200)
	if len(got) < 3 {
		t.Fatalf("oversized file produced %d chunks, want >=3", len(got))
	}
	for i, c := range got {
		if !strings.Contains(c.Text, "=== big.txt") {
			end := 60
			if end > len(c.Text) {
				end = len(c.Text)
			}
			t.Errorf("chunk %d missing big.txt header: %q", i, c.Text[:end])
		}
		if !strings.Contains(c.Text, "(lines ") {
			t.Errorf("chunk %d missing line-range annotation", i)
		}
	}
}

func TestChunkSourceFilesSplitsReassembleToOriginal(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 50; i++ {
		sb.WriteString("line ")
		sb.WriteString(strings.Repeat("y", 30))
		sb.WriteString("\n")
	}
	original := sb.String()
	f := NewSourceFile("big.txt", []byte(original))
	got := ChunkSourceFiles([]SourceFile{f}, 250)
	if len(got) < 2 {
		t.Fatalf("expected multiple split chunks, got %d", len(got))
	}

	var rebuilt strings.Builder
	for _, c := range got {
		// Strip the "=== big.txt (lines a-b) ===\n" header and the trailing "\n\n".
		text := c.Text
		nl := strings.IndexByte(text, '\n')
		if nl < 0 {
			t.Fatalf("chunk text had no newline: %q", text)
		}
		body := text[nl+1:]
		body = strings.TrimSuffix(body, "\n\n")
		rebuilt.WriteString(body)
	}
	if rebuilt.String() != original {
		t.Errorf("rebuilt content does not match original\nrebuilt len=%d original len=%d",
			rebuilt.Len(), len(original))
	}
}

func TestChunkSourceFilesMultipleOversized(t *testing.T) {
	mkBig := func(name string, n int) SourceFile {
		var sb strings.Builder
		for i := 0; i < n; i++ {
			sb.WriteString("row ")
			sb.WriteString(strings.Repeat("z", 25))
			sb.WriteString("\n")
		}
		return NewSourceFile(name, []byte(sb.String()))
	}
	files := []SourceFile{
		mkBig("big1.txt", 60),
		mkBig("big2.txt", 80),
	}
	got := ChunkSourceFiles(files, 200)

	// Each oversized file should produce multiple chunks; each chunk anchors a
	// single file (the one it came from).
	count1, count2 := 0, 0
	for _, c := range got {
		if len(c.Files) != 1 {
			t.Errorf("split chunk should anchor exactly one file, got %d", len(c.Files))
			continue
		}
		switch c.Files[0].RelativePath {
		case "big1.txt":
			count1++
			if !strings.Contains(c.Text, "=== big1.txt (lines ") {
				t.Errorf("big1.txt chunk missing line-range header: %q", firstLine(c.Text))
			}
		case "big2.txt":
			count2++
			if !strings.Contains(c.Text, "=== big2.txt (lines ") {
				t.Errorf("big2.txt chunk missing line-range header: %q", firstLine(c.Text))
			}
		default:
			t.Errorf("unexpected anchor file: %q", c.Files[0].RelativePath)
		}
	}
	if count1 < 2 {
		t.Errorf("big1.txt produced %d chunks, want >=2", count1)
	}
	if count2 < 2 {
		t.Errorf("big2.txt produced %d chunks, want >=2", count2)
	}
}

func TestChunkSourceFilesFileEqualsBudget(t *testing.T) {
	// File block = "=== a.md ===\n" (14) + body + "\n\n" (2). Pick body so that
	// the block length equals maxBytes exactly.
	const max = 100
	const headerLen = len("=== a.md ===\n")
	const trailerLen = len("\n\n")
	bodyLen := max - headerLen - trailerLen
	body := strings.Repeat("x", bodyLen)
	f := NewSourceFile("a.md", []byte(body))
	got := ChunkSourceFiles([]SourceFile{f}, max)
	if len(got) != 1 {
		t.Fatalf("file equal to budget -> %d chunks, want 1", len(got))
	}
	if len(got[0].Text) != max {
		t.Errorf("chunk text len = %d, want %d", len(got[0].Text), max)
	}
	if !strings.Contains(got[0].Text, "=== a.md ===") {
		t.Errorf("missing whole-file header (split should not have happened)")
	}
	if strings.Contains(got[0].Text, "(lines ") {
		t.Errorf("equal-to-budget file should not be split: %q", got[0].Text)
	}
}

func chunkTexts(cs []Chunk) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Header
	}
	return out
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
