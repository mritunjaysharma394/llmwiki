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

func TestPage_TagsSourcesCreatedRoundTrip(t *testing.T) {
	dir := t.TempDir()
	original := Page{
		Title:       "Round Trip",
		Body:        "body\n",
		ContentHash: "h",
		UpdatedAt:   time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC),
		Tags:        []string{"llmwiki", "ingest"},
		Sources:     []string{"internal/db/db.go", "internal/db/queries.go"},
		Created:     time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC),
	}
	if err := WritePage(original, dir); err != nil {
		t.Fatalf("WritePage: %v", err)
	}
	read, err := ReadPage(filepath.Join(dir, "Round Trip.md"))
	if err != nil {
		t.Fatalf("ReadPage: %v", err)
	}
	if len(read.Tags) != 2 || read.Tags[0] != "llmwiki" || read.Tags[1] != "ingest" {
		t.Errorf("tags: got %#v want [llmwiki ingest]", read.Tags)
	}
	if len(read.Sources) != 2 || read.Sources[0] != "internal/db/db.go" || read.Sources[1] != "internal/db/queries.go" {
		t.Errorf("sources: got %#v", read.Sources)
	}
	if !read.Created.Equal(original.Created) {
		t.Errorf("created: got %v want %v", read.Created, original.Created)
	}
}

func TestPage_PreV1_1FilesParseUnchanged(t *testing.T) {
	content := `---
title: Old Page
updated_at: 2026-04-01T10:00:00Z
content_hash: deadbeef
source_ids: [1, 2]
links:
  - to: Other
    type: supports
---

Pre-v1.1 body.
`
	p, err := ParsePage(content)
	if err != nil {
		t.Fatalf("ParsePage: %v", err)
	}
	if p.Title != "Old Page" {
		t.Errorf("title = %q", p.Title)
	}
	if !p.UpdatedAt.Equal(time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("updated_at = %v", p.UpdatedAt)
	}
	if p.ContentHash != "deadbeef" {
		t.Errorf("content_hash = %q", p.ContentHash)
	}
	if len(p.SourceIDs) != 2 || p.SourceIDs[0] != 1 || p.SourceIDs[1] != 2 {
		t.Errorf("source_ids = %v", p.SourceIDs)
	}
	if len(p.Links) != 1 || p.Links[0].To != "Other" || p.Links[0].Type != "supports" {
		t.Errorf("links = %#v", p.Links)
	}
	// New fields must be zero-valued.
	if len(p.Tags) != 0 {
		t.Errorf("Tags should be zero, got %#v", p.Tags)
	}
	if len(p.Sources) != 0 {
		t.Errorf("Sources should be zero, got %#v", p.Sources)
	}
	if !p.Created.IsZero() {
		t.Errorf("Created should be zero, got %v", p.Created)
	}

	// Round-trip the parsed Page back through WritePage; the new keys must
	// NOT spontaneously appear (since the fields are zero-valued).
	dir := t.TempDir()
	p.Body = "Pre-v1.1 body.\n"
	if err := WritePage(p, dir); err != nil {
		t.Fatalf("WritePage: %v", err)
	}
	written, err := os.ReadFile(filepath.Join(dir, "Old Page.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(written)
	if strings.Contains(s, "tags:") {
		t.Errorf("pre-v1.1 round-trip should not add tags (no real data):\n%s", s)
	}
	if strings.Contains(s, "sources:") {
		t.Errorf("pre-v1.1 round-trip should not add sources (no real data):\n%s", s)
	}
	if strings.Contains(s, "\ncreated:") {
		t.Errorf("pre-v1.1 round-trip should not add created (no real data):\n%s", s)
	}
	// `updated:` IS populated from real data (UpdatedAt) so it MAY appear on
	// round-trip — that's the plan's explicit contract. Just confirm we
	// don't crash and the round-trip preserves real data.
}

func TestPage_TagsArrayFormatIsDataviewCompatible(t *testing.T) {
	dir := t.TempDir()
	p := Page{
		Title:       "Fmt",
		Body:        "b",
		ContentHash: "h",
		UpdatedAt:   time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC),
		Tags:        []string{"llmwiki", "ingest"},
	}
	if err := WritePage(p, dir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "Fmt.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "tags: [llmwiki, ingest]\n") {
		t.Errorf("expected flat bracketed tags array; file:\n%s", string(data))
	}
	if strings.Contains(string(data), "tags:\n  -") {
		t.Errorf("tags should not be emitted in block-list form; file:\n%s", string(data))
	}
}

func TestPage_SourcesDerivedFromEvidence(t *testing.T) {
	dir := t.TempDir()
	// Caller leaves Sources nil; WritePage should derive the distinct set
	// from Evidence.SourceFilePath (de-duped, first-occurrence order).
	p := Page{
		Title:       "Derived",
		Body:        "b",
		ContentHash: "h",
		UpdatedAt:   time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC),
		Evidence: []Evidence{
			{Quote: "q1", LineStart: 1, LineEnd: 1, SourceFilePath: "internal/db/db.go"},
			{Quote: "q2", LineStart: 2, LineEnd: 2, SourceFilePath: "internal/db/queries.go"},
			{Quote: "q3", LineStart: 3, LineEnd: 3, SourceFilePath: "internal/db/db.go"}, // dup
		},
	}
	if err := WritePage(p, dir); err != nil {
		t.Fatal(err)
	}
	read, err := ReadPage(filepath.Join(dir, "Derived.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(read.Sources) != 2 {
		t.Fatalf("expected 2 distinct sources, got %#v", read.Sources)
	}
	if read.Sources[0] != "internal/db/db.go" || read.Sources[1] != "internal/db/queries.go" {
		t.Errorf("unexpected sources order/value: %#v", read.Sources)
	}
}

func TestPage_CreatedIsDateOnly(t *testing.T) {
	dir := t.TempDir()
	p := Page{
		Title:       "Date",
		Body:        "b",
		ContentHash: "h",
		UpdatedAt:   time.Date(2026, 5, 4, 10, 30, 0, 0, time.UTC),
		Created:     time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC),
	}
	if err := WritePage(p, dir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "Date.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, "created: 2026-05-04\n") {
		t.Errorf("expected `created: 2026-05-04`; file:\n%s", s)
	}
	if strings.Contains(s, "created: 2026-05-04T") {
		t.Errorf("created should be date-only, not RFC3339; file:\n%s", s)
	}
	if !strings.Contains(s, "updated: 2026-05-04\n") {
		t.Errorf("expected `updated: 2026-05-04` twin; file:\n%s", s)
	}
	if !strings.Contains(s, "updated_at: 2026-05-04T10:30:00Z\n") {
		t.Errorf("updated_at RFC3339 should still be present; file:\n%s", s)
	}
}
