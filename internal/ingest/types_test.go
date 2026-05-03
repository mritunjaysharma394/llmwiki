package ingest

import (
	"testing"
)

func TestNewSourceFileHashesAndCountsLines(t *testing.T) {
	sf := NewSourceFile("readme.md", []byte("alpha\nbeta\ngamma"))
	if sf.RelativePath != "readme.md" {
		t.Errorf("path = %q", sf.RelativePath)
	}
	if sf.ByteSize != 16 {
		t.Errorf("byte size = %d, want 16", sf.ByteSize)
	}
	if sf.LineCount != 3 {
		t.Errorf("line count = %d, want 3", sf.LineCount)
	}
	if len(sf.ContentHash) != 64 {
		t.Errorf("content hash length = %d, want 64 (sha256 hex)", len(sf.ContentHash))
	}
}

func TestNewSourceFileTrailingNewline(t *testing.T) {
	sf := NewSourceFile("x", []byte("a\nb\n"))
	if sf.LineCount != 2 {
		t.Errorf("trailing-newline line count = %d, want 2", sf.LineCount)
	}
}

func TestNewSourceFileEmpty(t *testing.T) {
	sf := NewSourceFile("x", []byte{})
	if sf.LineCount != 0 {
		t.Errorf("empty line count = %d, want 0", sf.LineCount)
	}
	if sf.ByteSize != 0 {
		t.Errorf("empty byte size = %d", sf.ByteSize)
	}
}
