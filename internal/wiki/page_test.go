package wiki

import (
	"os"
	"path/filepath"
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
	// Pages written before evidence support should still parse.
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

// Reach into Page to confirm Evidence field exists at compile time.
var _ = Page{Evidence: []Evidence{{}}}

// Ensure WritePage doesn't choke on missing optional dirs.
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

func TestEvidenceQuoteEscapingAdversarial(t *testing.T) {
	cases := []Evidence{
		{Quote: `literal backslash-n: \n`, LineStart: 1, LineEnd: 1},
		{Quote: `literal backslash-r: \r`, LineStart: 2, LineEnd: 2},
		{Quote: `literal backslash-quote: \"`, LineStart: 3, LineEnd: 3},
		{Quote: `single backslash: \`, LineStart: 4, LineEnd: 4},
		{Quote: `windows path C:\Users\foo`, LineStart: 5, LineEnd: 5},
		{Quote: `mixed "real quote" plus \\ plus \n literal`, LineStart: 6, LineEnd: 6},
		{Quote: "actual newline in middle\nsplits across\nthree lines", LineStart: 7, LineEnd: 9},
		{Quote: `embedded "double" quote`, LineStart: 10, LineEnd: 10},
	}
	dir := t.TempDir()
	original := Page{Title: "Adv", Body: "x", UpdatedAt: time.Now(), Evidence: cases}
	if err := WritePage(original, dir); err != nil {
		t.Fatalf("WritePage: %v", err)
	}
	read, err := ReadPage(filepath.Join(dir, "Adv.md"))
	if err != nil {
		t.Fatalf("ReadPage: %v", err)
	}
	if len(read.Evidence) != len(cases) {
		t.Fatalf("got %d evidence rows, want %d", len(read.Evidence), len(cases))
	}
	for i, want := range cases {
		if read.Evidence[i].Quote != want.Quote {
			t.Errorf("case %d quote mismatch:\n  got:  %q\n  want: %q", i, read.Evidence[i].Quote, want.Quote)
		}
	}
}

func TestUnescapeQuoteUnit(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{`""`, ""},
		{`"abc"`, "abc"},
		{`"a\nb"`, "a\nb"},
		{`"a\\nb"`, `a\nb`},      // literal \n stays literal
		{`"a\\\\nb"`, `a\\nb`},   // 4 backslashes → 2 backslashes + n
		{`"a\""`, `a"`},
		{`"a\\\"b"`, `a\"b`},     // backslash + escaped-quote
		{`"\r\n"`, "\r\n"},
		{`"\\r\\n"`, `\r\n`},
		{`"trailing slash \"`, `trailing slash \`},
	}
	for _, tc := range cases {
		got := unescapeQuote(tc.in)
		if got != tc.want {
			t.Errorf("unescapeQuote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
