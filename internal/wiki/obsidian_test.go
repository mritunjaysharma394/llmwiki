package wiki

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/mritunjaysharma394/llmwiki/internal/db"
)

func TestRewriteBareReferencesAsWikilinks_KnownTitles(t *testing.T) {
	body := "See Trust Property for details. The Database Layer also matters."
	titles := []string{"Trust Property", "Database Layer", "Ingest Pipeline"}
	got := RewriteBareReferencesAsWikilinks(body, titles)
	want := "See [[Trust Property]] for details. The [[Database Layer]] also matters."
	if got != want {
		t.Errorf("got:  %q\nwant: %q", got, want)
	}
}

func TestRewriteBareReferencesAsWikilinks_Idempotent(t *testing.T) {
	body := "See Trust Property for details. The Database Layer also matters."
	titles := []string{"Trust Property", "Database Layer"}
	once := RewriteBareReferencesAsWikilinks(body, titles)
	twice := RewriteBareReferencesAsWikilinks(once, titles)
	if once != twice {
		t.Errorf("not idempotent:\nonce:  %q\ntwice: %q", once, twice)
	}
}

func TestRewriteBareReferencesAsWikilinks_SkipsCodeFences(t *testing.T) {
	body := "Use Trust Property\n```go\nTrust Property := struct{}\n```\n"
	titles := []string{"Trust Property"}
	got := RewriteBareReferencesAsWikilinks(body, titles)
	want := "Use [[Trust Property]]\n```go\nTrust Property := struct{}\n```\n"
	if got != want {
		t.Errorf("got:  %q\nwant: %q", got, want)
	}
}

func TestRewriteBareReferencesAsWikilinks_SkipsInlineBackticks(t *testing.T) {
	body := "the `Trust Property` field"
	titles := []string{"Trust Property"}
	got := RewriteBareReferencesAsWikilinks(body, titles)
	if got != body {
		t.Errorf("inline backticks should be untouched: got %q want %q", got, body)
	}
}

func TestRewriteBareReferencesAsWikilinks_CaseSensitive(t *testing.T) {
	body := "Mention trust property in lowercase."
	titles := []string{"Trust Property"}
	got := RewriteBareReferencesAsWikilinks(body, titles)
	if got != body {
		t.Errorf("case-sensitive miss expected, got: %q", got)
	}
}

func TestRewriteBareReferencesAsWikilinks_WholeWord(t *testing.T) {
	body := "DBA is not DB and DBA != DB."
	titles := []string{"DB"}
	got := RewriteBareReferencesAsWikilinks(body, titles)
	want := "DBA is not [[DB]] and DBA != [[DB]]."
	if got != want {
		t.Errorf("got:  %q\nwant: %q", got, want)
	}
}

func TestRewriteBareReferencesAsWikilinks_LongestFirst(t *testing.T) {
	// "Trust Property Validator" should win over "Trust Property" so we don't
	// produce nested-link garbage like "[[Trust Property]] Validator".
	body := "The Trust Property Validator runs first."
	titles := []string{"Trust Property", "Trust Property Validator"}
	got := RewriteBareReferencesAsWikilinks(body, titles)
	want := "The [[Trust Property Validator]] runs first."
	if got != want {
		t.Errorf("got:  %q\nwant: %q", got, want)
	}
}

func TestRewriteBareReferencesAsWikilinks_SkipsFrontmatter(t *testing.T) {
	// Bodies passed in by the ingest pipeline don't include frontmatter, but
	// the rewriter is documented to skip it for safety. Verify the contract.
	body := "---\nfoo: Trust Property\n---\n\nSee Trust Property here.\n"
	titles := []string{"Trust Property"}
	got := RewriteBareReferencesAsWikilinks(body, titles)
	want := "---\nfoo: Trust Property\n---\n\nSee [[Trust Property]] here.\n"
	if got != want {
		t.Errorf("got:  %q\nwant: %q", got, want)
	}
}

func TestRewriteBareReferencesAsWikilinks_MultiParagraph(t *testing.T) {
	body := "First paragraph mentions Trust Property.\n\nSecond paragraph talks about Database Layer.\n\nThird paragraph again Trust Property and Database Layer.\n"
	titles := []string{"Trust Property", "Database Layer"}
	got := RewriteBareReferencesAsWikilinks(body, titles)
	if !strings.Contains(got, "[[Trust Property]]") {
		t.Errorf("missing [[Trust Property]] in: %q", got)
	}
	if !strings.Contains(got, "[[Database Layer]]") {
		t.Errorf("missing [[Database Layer]] in: %q", got)
	}
	// Three occurrences of the linked Trust Property total — wait, only two.
	// Let's count carefully.
	if strings.Count(got, "[[Trust Property]]") != 2 {
		t.Errorf("expected 2 [[Trust Property]], got %d in %q", strings.Count(got, "[[Trust Property]]"), got)
	}
	if strings.Count(got, "[[Database Layer]]") != 2 {
		t.Errorf("expected 2 [[Database Layer]], got %d in %q", strings.Count(got, "[[Database Layer]]"), got)
	}
}

func TestRegenerateIndex_EmptyWiki(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 4, 14, 30, 12, 0, time.UTC)
	if err := RegenerateIndex(dir, nil, nil, now); err != nil {
		t.Fatalf("RegenerateIndex: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "index.md"))
	if err != nil {
		t.Fatalf("read index.md: %v", err)
	}
	s := string(body)
	if !strings.HasPrefix(s, "---\n") {
		t.Errorf("missing frontmatter")
	}
	if !strings.Contains(s, "title: index") {
		t.Errorf("missing title key")
	}
	if !strings.Contains(s, "generator: llmwiki") {
		t.Errorf("missing generator key")
	}
	if !strings.Contains(s, "## Pages (0)") {
		t.Errorf("missing Pages (0) header in:\n%s", s)
	}
}

func TestRegenerateIndex_DeterministicByteIdentical(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	pages := []db.PageRecord{
		{Title: "Beta", UpdatedAt: time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC), SourceIDs: []int64{1}},
		{Title: "Alpha", UpdatedAt: time.Date(2026, 5, 3, 11, 0, 0, 0, time.UTC), SourceIDs: []int64{1}},
	}
	sources := []db.Source{
		{ID: 1, URI: "https://example.com/x"},
	}
	now := time.Date(2026, 5, 4, 14, 30, 12, 0, time.UTC)

	if err := RegenerateIndex(dir1, pages, sources, now); err != nil {
		t.Fatalf("RegenerateIndex 1: %v", err)
	}
	if err := RegenerateIndex(dir2, pages, sources, now); err != nil {
		t.Fatalf("RegenerateIndex 2: %v", err)
	}
	a, _ := os.ReadFile(filepath.Join(dir1, "index.md"))
	b, _ := os.ReadFile(filepath.Join(dir2, "index.md"))
	if !bytes.Equal(a, b) {
		t.Errorf("not byte-identical:\n--- a ---\n%s\n--- b ---\n%s", a, b)
	}
	// Alpha must come before Beta (sorted).
	idxAlpha := bytes.Index(a, []byte("[[Alpha]]"))
	idxBeta := bytes.Index(a, []byte("[[Beta]]"))
	if idxAlpha < 0 || idxBeta < 0 || idxAlpha > idxBeta {
		t.Errorf("expected Alpha before Beta in:\n%s", a)
	}
}

func TestRegenerateIndex_GroupsBySource(t *testing.T) {
	dir := t.TempDir()
	pages := []db.PageRecord{
		{Title: "P1", UpdatedAt: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), SourceIDs: []int64{1}},
		{Title: "P2", UpdatedAt: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), SourceIDs: []int64{2}},
		{Title: "P3", UpdatedAt: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), SourceIDs: []int64{3}},
	}
	sources := []db.Source{
		{ID: 1, URI: "src/one"},
		{ID: 2, URI: "src/two"},
		{ID: 3, URI: "src/three"},
	}
	now := time.Date(2026, 5, 4, 14, 30, 12, 0, time.UTC)
	if err := RegenerateIndex(dir, pages, sources, now); err != nil {
		t.Fatalf("RegenerateIndex: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "index.md"))
	s := string(body)
	for _, want := range []string{"### src/one", "### src/two", "### src/three"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
}

func TestAppendLog_RFC3339UTC(t *testing.T) {
	dir := t.TempDir()
	t1 := time.Date(2026, 5, 4, 14, 30, 12, 0, time.UTC)
	t2 := time.Date(2026, 5, 4, 14, 31, 45, 0, time.UTC)
	if err := AppendLog(dir, LogEntry{At: t1, Kind: "ingest", Payload: "./README.md → 7 pages"}); err != nil {
		t.Fatalf("AppendLog 1: %v", err)
	}
	if err := AppendLog(dir, LogEntry{At: t2, Kind: "ask", Payload: "what does X do"}); err != nil {
		t.Fatalf("AppendLog 2: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "log.md"))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d:\n%s", len(lines), body)
	}
	rfc := regexp.MustCompile(`^- \d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z \*\*[^*]+\*\* `)
	for i, ln := range lines {
		if !rfc.MatchString(ln) {
			t.Errorf("line %d not RFC3339 UTC: %q", i, ln)
		}
	}
	if !strings.Contains(lines[0], "**ingest**") || !strings.Contains(lines[0], "./README.md") {
		t.Errorf("line 0 missing ingest/payload: %q", lines[0])
	}
	if !strings.Contains(lines[1], "**ask**") || !strings.Contains(lines[1], "what does X do") {
		t.Errorf("line 1 missing ask/payload: %q", lines[1])
	}
}

func TestAppendLog_AppendOnly(t *testing.T) {
	dir := t.TempDir()
	t1 := time.Date(2026, 5, 4, 14, 30, 12, 0, time.UTC)
	if err := AppendLog(dir, LogEntry{At: t1, Kind: "ingest", Payload: "first"}); err != nil {
		t.Fatalf("AppendLog 1: %v", err)
	}
	first, _ := os.ReadFile(filepath.Join(dir, "log.md"))
	t2 := time.Date(2026, 5, 4, 14, 35, 0, 0, time.UTC)
	if err := AppendLog(dir, LogEntry{At: t2, Kind: "ask", Payload: "second"}); err != nil {
		t.Fatalf("AppendLog 2: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "log.md"))
	// First line must be unchanged: the prefix of `after` must equal `first`.
	if !bytes.HasPrefix(after, first) {
		t.Errorf("first append was rewritten:\nfirst:\n%s\nafter:\n%s", first, after)
	}
}
