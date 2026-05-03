package cmd

import (
	"strings"
	"testing"
)

func TestChunkContentSmallReturnsSingle(t *testing.T) {
	got := chunkContent("hello world", 100)
	if len(got) != 1 || got[0] != "hello world" {
		t.Errorf("got %q", got)
	}
}

func TestChunkContentEmpty(t *testing.T) {
	got := chunkContent("", 100)
	if len(got) != 1 || got[0] != "" {
		t.Errorf("got %q", got)
	}
}

func TestChunkContentSplitsAtNewline(t *testing.T) {
	src := "aaaa\nbbbb\ncccc\ndddd\n"
	got := chunkContent(src, 10)
	if len(got) < 2 {
		t.Fatalf("got %d chunks", len(got))
	}
	for i, c := range got[:len(got)-1] {
		if !strings.HasSuffix(c, "\n") {
			t.Errorf("chunk %d does not end at newline: %q", i, c)
		}
	}
	if strings.Join(got, "") != src {
		t.Errorf("reassembly mismatch")
	}
}

func TestChunkContentNoCapAt16k(t *testing.T) {
	src := strings.Repeat("a\n", 25000)
	got := chunkContent(src, 16*1024)
	if len(got) < 3 {
		t.Errorf("expected ≥3 chunks at 16k, got %d", len(got))
	}
	if strings.Join(got, "") != src {
		t.Errorf("reassembly mismatch")
	}
}

func TestSlugifyForArchive(t *testing.T) {
	tests := []struct{ in, want string }{
		{"What dependencies?", "what-dependencies"},
		{"Hello,  World!", "hello-world"},
		{"a/b\\c", "a-b-c"},
		{"   ", ""},
	}
	for _, tc := range tests {
		if got := slugify(tc.in); got != tc.want {
			t.Errorf("slugify(%q) = %q want %q", tc.in, got, tc.want)
		}
	}
}
