package wiki

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteReadPageWithEvidence(t *testing.T) {
	dir := t.TempDir()
	original := Page{
		Title:       "Test Page",
		Body:        "# Heading\n\nSome body text.\n",
		Links:       []Link{{To: "Other Page", Type: "supports"}},
		SourceIDs:   []int64{1, 2},
		ContentHash: "abc",
		UpdatedAt:   time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC),
		Evidence: []Evidence{
			{Quote: "first verbatim quote", LineStart: 3, LineEnd: 3},
			{Quote: "second one\nspans two lines", LineStart: 7, LineEnd: 8},
		},
	}
	if err := WritePage(original, dir); err != nil {
		t.Fatalf("WritePage: %v", err)
	}
	read, err := ReadPage(filepath.Join(dir, "Test Page.md"))
	if err != nil {
		t.Fatalf("ReadPage: %v", err)
	}
	if read.Title != original.Title {
		t.Errorf("title: got %q want %q", read.Title, original.Title)
	}
	if read.Body != original.Body {
		t.Errorf("body mismatch:\ngot:  %q\nwant: %q", read.Body, original.Body)
	}
	if len(read.Evidence) != 2 {
		t.Fatalf("evidence count: got %d want 2", len(read.Evidence))
	}
	if read.Evidence[0].Quote != "first verbatim quote" {
		t.Errorf("ev[0].Quote = %q", read.Evidence[0].Quote)
	}
	if read.Evidence[1].LineStart != 7 || read.Evidence[1].LineEnd != 8 {
		t.Errorf("ev[1] lines: got %d-%d want 7-8", read.Evidence[1].LineStart, read.Evidence[1].LineEnd)
	}
}

func TestParsePageBackwardCompatible(t *testing.T) {
	content := `---
title: Old Page
updated_at: 2026-04-01T10:00:00Z
content_hash: deadbeef
source_ids: [1]
---

Body content.
`
	p, err := ParsePage(content)
	if err != nil {
		t.Fatalf("ParsePage: %v", err)
	}
	if p.Title != "Old Page" || len(p.Evidence) != 0 {
		t.Errorf("got %+v", p)
	}
}

func TestPagePathSanitizes(t *testing.T) {
	got := PagePath("/wiki", "Foo / Bar : Baz")
	want := "/wiki/Foo _ Bar _ Baz.md"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

var _ = Page{Evidence: []Evidence{{}}}

func TestEvidenceSourceFilePathRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := Page{
		Title:       "T",
		Body:        "b",
		ContentHash: "h",
		UpdatedAt:   time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC),
		Evidence: []Evidence{
			{Quote: "q1", LineStart: 1, LineEnd: 2, SourceFilePath: "internal/db/db.go"},
			{Quote: "q2", LineStart: 3, LineEnd: 3, SourceFilePath: "page-4"},
		},
	}
	if err := WritePage(p, dir); err != nil {
		t.Fatal(err)
	}
	got, err := ReadPage(filepath.Join(dir, "T.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Evidence) != 2 {
		t.Fatalf("got %d evidence rows, want 2", len(got.Evidence))
	}
	if got.Evidence[0].SourceFilePath != "internal/db/db.go" {
		t.Errorf("ev[0].SourceFilePath = %q", got.Evidence[0].SourceFilePath)
	}
	if got.Evidence[1].SourceFilePath != "page-4" {
		t.Errorf("ev[1].SourceFilePath = %q", got.Evidence[1].SourceFilePath)
	}
}

func TestEvidenceSourceFilePathOmittedWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	p := Page{
		Title:       "U",
		Body:        "b",
		ContentHash: "h",
		UpdatedAt:   time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC),
		Evidence: []Evidence{
			{Quote: "no path", LineStart: 1, LineEnd: 1},
		},
	}
	if err := WritePage(p, dir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "U.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "source_file:") {
		t.Errorf("source_file should be omitted when empty; file:\n%s", string(data))
	}
	got, err := ReadPage(filepath.Join(dir, "U.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Evidence) != 1 || got.Evidence[0].SourceFilePath != "" {
		t.Errorf("got %+v", got.Evidence)
	}
}

func TestWritePageCreatesDir(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "nested", "wiki")
	if err := WritePage(Page{Title: "X", Body: "b", UpdatedAt: time.Now()}, nested); err != nil {
		t.Fatalf("WritePage: %v", err)
	}
	if _, err := os.Stat(filepath.Join(nested, "X.md")); err != nil {
		t.Errorf("file not created: %v", err)
	}
}
