package wiki

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mritunjaysharma394/llmwiki/internal/schema"
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

// ---------- Sub-project 7 / Phase J Task 15 ----------
//
// WritePageWithSchema reads field-name overrides + declared order from
// Schema.Ontology. The legacy WritePage shim delegates with
// schema.Bundled() so pre-v0.7 callers see no behaviour change. The
// canonical struct field carrying evidence quotes (`Page.Evidence`) is
// fixed; the rename is a name-string mapping over disk emission only.

// canonicalSchemaWithDeclared builds a Schema with the bundled canonical
// ontology field list, applying any per-canonical declared-name override
// from the supplied map. Order matches schema.Bundled() (i.e. v0.6
// emission order).
func canonicalSchemaWithDeclared(t *testing.T, decl map[string]string) schema.Schema {
	t.Helper()
	s := schema.Bundled()
	out := s
	out.Ontology.Fields = make([]schema.OntologyField, len(s.Ontology.Fields))
	copy(out.Ontology.Fields, s.Ontology.Fields)
	for i := range out.Ontology.Fields {
		c := out.Ontology.Fields[i].CanonicalName
		if d, ok := decl[c]; ok && d != "" {
			out.Ontology.Fields[i].DeclaredName = d
		}
	}
	return out
}

// TestWritePage_BundledSchema_ProducesV06FrontmatterByteIdentical pins
// the load-bearing backwards-compat contract: writing a page through
// WritePageWithSchema with schema.Bundled() produces frontmatter
// byte-identical to v0.6's hard-coded WritePage. A v0.6 wiki opening
// under v0.7 with no AGENTS.md sees zero on-disk drift.
func TestWritePage_BundledSchema_ProducesV06FrontmatterByteIdentical(t *testing.T) {
	dir := t.TempDir()
	p := Page{
		Title:       "Compat",
		Body:        "# Body\n\nHello.\n",
		Links:       []Link{{To: "Other Page", Type: "supports"}},
		SourceIDs:   []int64{1, 2},
		ContentHash: "abc",
		UpdatedAt:   time.Date(2026, 5, 4, 10, 30, 0, 0, time.UTC),
		Evidence: []Evidence{
			{Quote: "first quote", LineStart: 3, LineEnd: 3, SourceFilePath: "internal/db/db.go"},
		},
		Tags:    []string{"llmwiki", "ingest"},
		Sources: []string{"internal/db/db.go"},
		Created: time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC),
	}
	if err := WritePageWithSchema(p, dir, schema.Bundled()); err != nil {
		t.Fatalf("WritePageWithSchema: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "Compat.md"))
	if err != nil {
		t.Fatal(err)
	}
	want := "---\n" +
		"title: Compat\n" +
		"updated_at: 2026-05-04T10:30:00Z\n" +
		"content_hash: abc\n" +
		"source_ids: [1, 2]\n" +
		"tags: [llmwiki, ingest]\n" +
		"sources: [internal/db/db.go]\n" +
		"created: 2026-05-04\n" +
		"updated: 2026-05-04\n" +
		"links:\n" +
		"  - to: Other Page\n" +
		"    type: supports\n" +
		"evidence:\n" +
		"  - quote: \"first quote\"\n" +
		"    line_start: 3\n" +
		"    line_end: 3\n" +
		"    source_file: internal/db/db.go\n" +
		"---\n\n" +
		"# Body\n\nHello.\n"
	if string(got) != want {
		t.Errorf("bundled-schema emission drifted from v0.6\n--- want ---\n%s\n--- got ---\n%s", want, string(got))
	}
}

// TestWritePage_RenamedSchema_EmitsRenamedKeys: schema renames evidence
// → citations; the on-disk frontmatter key reflects the rename even
// though Page.Evidence is the struct field carrying the quotes.
func TestWritePage_RenamedSchema_EmitsRenamedKeys(t *testing.T) {
	dir := t.TempDir()
	sch := canonicalSchemaWithDeclared(t, map[string]string{"evidence": "citations"})
	p := Page{
		Title:       "Rename",
		Body:        "body\n",
		ContentHash: "h",
		UpdatedAt:   time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC),
		Evidence: []Evidence{
			{Quote: "q1", LineStart: 1, LineEnd: 1, SourceFilePath: "internal/db/db.go"},
		},
	}
	if err := WritePageWithSchema(p, dir, sch); err != nil {
		t.Fatalf("WritePageWithSchema: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "Rename.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, "citations:\n  - quote: ") {
		t.Errorf("expected `citations:` section; file:\n%s", s)
	}
	if strings.Contains(s, "evidence:\n  - quote:") {
		t.Errorf("evidence: must NOT appear when schema renamed it; file:\n%s", s)
	}
}

// TestWritePage_ReorderedSchema_EmitsInDeclaredOrder: reordering
// [evidence, title, body] in the schema's ontology slice puts evidence
// before title in the on-disk frontmatter.
func TestWritePage_ReorderedSchema_EmitsInDeclaredOrder(t *testing.T) {
	dir := t.TempDir()
	sch := schema.Bundled()
	// Reorder: pull `evidence` to the front, then `title`, then everything
	// else preserving their original relative order.
	var ev, ti schema.OntologyField
	rest := make([]schema.OntologyField, 0, len(sch.Ontology.Fields))
	for _, f := range sch.Ontology.Fields {
		switch f.CanonicalName {
		case "evidence":
			ev = f
		case "title":
			ti = f
		default:
			rest = append(rest, f)
		}
	}
	reordered := append([]schema.OntologyField{ev, ti}, rest...)
	sch.Ontology.Fields = reordered
	p := Page{
		Title:       "Reorder",
		Body:        "b\n",
		ContentHash: "h",
		UpdatedAt:   time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC),
		Evidence: []Evidence{
			{Quote: "q1", LineStart: 1, LineEnd: 1, SourceFilePath: "internal/db/db.go"},
		},
	}
	if err := WritePageWithSchema(p, dir, sch); err != nil {
		t.Fatalf("WritePageWithSchema: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "Reorder.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	evIdx := strings.Index(s, "evidence:")
	tiIdx := strings.Index(s, "title:")
	if evIdx < 0 || tiIdx < 0 {
		t.Fatalf("expected both evidence: and title: in frontmatter; file:\n%s", s)
	}
	if evIdx >= tiIdx {
		t.Errorf("expected evidence: before title:; got evIdx=%d tiIdx=%d\n%s", evIdx, tiIdx, s)
	}
}

// TestWritePage_ExtraFrontmatterPassThrough_DeclaredButUnvalidated:
// the schema declares a `priority` extra field; the Page struct doesn't
// carry one (Page.ExtraFrontmatter is nil); WritePageWithSchema must
// NOT fabricate a `priority:` line. The pass-through path is for
// reading; writing only emits values that already exist.
func TestWritePage_ExtraFrontmatterPassThrough_DeclaredButUnvalidated(t *testing.T) {
	dir := t.TempDir()
	sch := schema.Bundled()
	// Append a non-canonical declared field to the ontology.
	sch.Ontology.Fields = append(sch.Ontology.Fields, schema.OntologyField{
		CanonicalName: "priority",
		DeclaredName:  "priority",
		Type:          "string",
		Description:   "user-declared extra",
	})
	p := Page{
		Title:       "Extra",
		Body:        "b\n",
		ContentHash: "h",
		UpdatedAt:   time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC),
	}
	if err := WritePageWithSchema(p, dir, sch); err != nil {
		t.Fatalf("WritePageWithSchema: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "Extra.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "priority:") {
		t.Errorf("WritePage must NOT fabricate priority: when Page struct + ExtraFrontmatter don't carry it; file:\n%s", string(data))
	}
}

// TestWritePage_RenamedSchema_TitleNotRenamed_StillWritesTitle: only
// `evidence` is renamed; `title` stays canonical and emits as `title:`.
func TestWritePage_RenamedSchema_TitleNotRenamed_StillWritesTitle(t *testing.T) {
	dir := t.TempDir()
	sch := canonicalSchemaWithDeclared(t, map[string]string{"evidence": "citations"})
	p := Page{
		Title:       "Partial",
		Body:        "b\n",
		ContentHash: "h",
		UpdatedAt:   time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC),
	}
	if err := WritePageWithSchema(p, dir, sch); err != nil {
		t.Fatalf("WritePageWithSchema: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "Partial.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "title: Partial\n") {
		t.Errorf("expected unrenamed title:; file:\n%s", string(data))
	}
}

// TestWritePage_BackwardsCompatShim_NoSchemaArg_UsesBundled: the legacy
// WritePage(p, dir) signature delegates with schema.Bundled() so callers
// that haven't yet adopted the schema-aware entrypoint see byte-for-byte
// identical output.
func TestWritePage_BackwardsCompatShim_NoSchemaArg_UsesBundled(t *testing.T) {
	p := Page{
		Title:       "Shim",
		Body:        "b\n",
		ContentHash: "h",
		UpdatedAt:   time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC),
		Evidence: []Evidence{
			{Quote: "q1", LineStart: 1, LineEnd: 1, SourceFilePath: "internal/db/db.go"},
		},
		Tags:    []string{"llmwiki", "ingest"},
		Sources: []string{"internal/db/db.go"},
		Created: time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC),
	}
	dirA := t.TempDir()
	dirB := t.TempDir()
	if err := WritePage(p, dirA); err != nil {
		t.Fatalf("WritePage shim: %v", err)
	}
	if err := WritePageWithSchema(p, dirB, schema.Bundled()); err != nil {
		t.Fatalf("WritePageWithSchema: %v", err)
	}
	a, err := os.ReadFile(filepath.Join(dirA, "Shim.md"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dirB, "Shim.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Errorf("WritePage shim != WritePageWithSchema(_, _, Bundled())\nshim:\n%s\nschema-aware:\n%s", string(a), string(b))
	}
}

// reflect is used by Task 16's round-trip tests.
var _ = reflect.DeepEqual

// ---------- Sub-project 7 / Phase J Task 16 ----------
//
// ParsePageWithSchema reads back via the same canonical/declared map;
// pre-v0.7 pages on disk fall back to canonical names; extra
// frontmatter (declared but not in the canonical struct set) round-
// trips through Page.ExtraFrontmatter.
//
// TRUST PROPERTY UNCHANGED. The canonical Page.Evidence struct field
// is fixed; ParsePage maps the on-disk key (whatever the user named
// it) back to Page.Evidence so the validator's check is name-agnostic.

// TestParsePage_RenamedSchema_ReadsRenamedKeys: write a page under a
// renamed schema (evidence → citations); parse with the same schema;
// assert Page.Evidence is populated.
func TestParsePage_RenamedSchema_ReadsRenamedKeys(t *testing.T) {
	dir := t.TempDir()
	sch := canonicalSchemaWithDeclared(t, map[string]string{"evidence": "citations"})
	want := Page{
		Title:       "Renamed",
		Body:        "body\n",
		ContentHash: "h",
		UpdatedAt:   time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC),
		Evidence: []Evidence{
			{Quote: "q1", LineStart: 1, LineEnd: 1, SourceFilePath: "internal/db/db.go"},
		},
	}
	if err := WritePageWithSchema(want, dir, sch); err != nil {
		t.Fatalf("WritePageWithSchema: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "Renamed.md"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParsePageWithSchema(string(data), sch)
	if err != nil {
		t.Fatalf("ParsePageWithSchema: %v", err)
	}
	if len(got.Evidence) != 1 || got.Evidence[0].Quote != "q1" {
		t.Errorf("Page.Evidence not populated from `citations:`; got %#v", got.Evidence)
	}
}

// TestParsePage_PreV07Page_FallsBackToBundledNames: content with
// canonical `evidence:` on disk; parse under a renamed schema; the
// canonical-name fallback rule lets the parser still populate
// Page.Evidence (pre-v0.7 pages stay readable under a renamed schema).
func TestParsePage_PreV07Page_FallsBackToBundledNames(t *testing.T) {
	sch := canonicalSchemaWithDeclared(t, map[string]string{"evidence": "citations"})
	content := "---\n" +
		"title: Pre v0.7\n" +
		"updated_at: 2026-04-01T10:00:00Z\n" +
		"content_hash: deadbeef\n" +
		"source_ids: [1]\n" +
		"evidence:\n" +
		"  - quote: \"old quote\"\n" +
		"    line_start: 3\n" +
		"    line_end: 3\n" +
		"    source_file: foo.go\n" +
		"---\n\n" +
		"body\n"
	got, err := ParsePageWithSchema(content, sch)
	if err != nil {
		t.Fatalf("ParsePageWithSchema: %v", err)
	}
	if len(got.Evidence) != 1 || got.Evidence[0].Quote != "old quote" {
		t.Errorf("pre-v0.7 evidence: must still populate Page.Evidence under renamed schema; got %#v", got.Evidence)
	}
}

// TestParsePage_BothNamesPresent_PrefersDeclared: pathological case
// from a botched migration — both `evidence:` and `citations:` on
// disk. The plan says the declared name (citations) wins.
func TestParsePage_BothNamesPresent_PrefersDeclared(t *testing.T) {
	sch := canonicalSchemaWithDeclared(t, map[string]string{"evidence": "citations"})
	content := "---\n" +
		"title: Both\n" +
		"updated_at: 2026-04-01T10:00:00Z\n" +
		"content_hash: h\n" +
		"source_ids: [1]\n" +
		"evidence:\n" +
		"  - quote: \"old canonical quote\"\n" +
		"    line_start: 1\n" +
		"    line_end: 1\n" +
		"    source_file: foo.go\n" +
		"citations:\n" +
		"  - quote: \"declared-name quote\"\n" +
		"    line_start: 5\n" +
		"    line_end: 5\n" +
		"    source_file: bar.go\n" +
		"---\n\n" +
		"body\n"
	got, err := ParsePageWithSchema(content, sch)
	if err != nil {
		t.Fatalf("ParsePageWithSchema: %v", err)
	}
	if len(got.Evidence) == 0 {
		t.Fatalf("no evidence parsed at all; got %#v", got)
	}
	// Plan: declared name wins. The canonical-only `evidence:` block is
	// dropped; the `citations:` block is what lands on Page.Evidence.
	if got.Evidence[0].Quote != "declared-name quote" {
		t.Errorf("declared name should win; got Page.Evidence[0].Quote = %q", got.Evidence[0].Quote)
	}
	for _, e := range got.Evidence {
		if e.Quote == "old canonical quote" {
			t.Errorf("canonical-only block should be dropped when declared block is also present; got %#v", got.Evidence)
		}
	}
}

// TestParsePage_ExtraFrontmatterPassThrough_DeclaredButUnknown: schema
// declares `priority` (extra field); content has `priority: high`;
// Page.ExtraFrontmatter populated.
func TestParsePage_ExtraFrontmatterPassThrough_DeclaredButUnknown(t *testing.T) {
	sch := schema.Bundled()
	sch.Ontology.Fields = append(sch.Ontology.Fields, schema.OntologyField{
		CanonicalName: "priority",
		DeclaredName:  "priority",
		Type:          "string",
		Description:   "user-declared extra",
	})
	content := "---\n" +
		"title: Pri\n" +
		"updated_at: 2026-04-01T10:00:00Z\n" +
		"content_hash: h\n" +
		"source_ids: [1]\n" +
		"priority: high\n" +
		"---\n\n" +
		"body\n"
	got, err := ParsePageWithSchema(content, sch)
	if err != nil {
		t.Fatalf("ParsePageWithSchema: %v", err)
	}
	if got.ExtraFrontmatter == nil {
		t.Fatalf("ExtraFrontmatter is nil; got %#v", got)
	}
	if v := got.ExtraFrontmatter["priority"]; v != "high" {
		t.Errorf("ExtraFrontmatter[\"priority\"] = %q, want %q", v, "high")
	}
}

// TestParsePage_BackwardsCompatShim_NoSchemaArg_UsesBundled: legacy
// ParsePage(content) delegates to ParsePageWithSchema(content,
// schema.Bundled()); behaviour identical for canonical-named pages.
func TestParsePage_BackwardsCompatShim_NoSchemaArg_UsesBundled(t *testing.T) {
	content := "---\n" +
		"title: Shim\n" +
		"updated_at: 2026-04-01T10:00:00Z\n" +
		"content_hash: h\n" +
		"source_ids: [1]\n" +
		"evidence:\n" +
		"  - quote: \"q\"\n" +
		"    line_start: 1\n" +
		"    line_end: 1\n" +
		"    source_file: foo.go\n" +
		"---\n\n" +
		"body\n"
	a, errA := ParsePage(content)
	if errA != nil {
		t.Fatalf("ParsePage shim: %v", errA)
	}
	b, errB := ParsePageWithSchema(content, schema.Bundled())
	if errB != nil {
		t.Fatalf("ParsePageWithSchema: %v", errB)
	}
	if !reflect.DeepEqual(a, b) {
		t.Errorf("ParsePage shim != ParsePageWithSchema(_, Bundled())\nshim: %#v\nschema-aware: %#v", a, b)
	}
}

// TestRoundTrip_RenamedSchema: write with renamed schema, read back
// with the same schema, assert structural equality.
func TestRoundTrip_RenamedSchema(t *testing.T) {
	dir := t.TempDir()
	sch := canonicalSchemaWithDeclared(t, map[string]string{"evidence": "citations"})
	want := Page{
		Title:       "RoundTripRename",
		Body:        "# Heading\n\nBody.\n",
		Links:       []Link{{To: "Other", Type: "supports"}},
		SourceIDs:   []int64{1, 2},
		ContentHash: "h",
		UpdatedAt:   time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC),
		Evidence: []Evidence{
			{Quote: "q1", LineStart: 1, LineEnd: 2, SourceFilePath: "internal/db/db.go"},
		},
		Tags:    []string{"llmwiki", "ingest"},
		Sources: []string{"internal/db/db.go"},
		Created: time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC),
	}
	if err := WritePageWithSchema(want, dir, sch); err != nil {
		t.Fatalf("WritePageWithSchema: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "RoundTripRename.md"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParsePageWithSchema(string(data), sch)
	if err != nil {
		t.Fatalf("ParsePageWithSchema: %v", err)
	}
	if got.Title != want.Title {
		t.Errorf("Title: got %q want %q", got.Title, want.Title)
	}
	if got.Body != want.Body {
		t.Errorf("Body: got %q want %q", got.Body, want.Body)
	}
	if !reflect.DeepEqual(got.Evidence, want.Evidence) {
		t.Errorf("Evidence: got %#v want %#v", got.Evidence, want.Evidence)
	}
	if !reflect.DeepEqual(got.Links, want.Links) {
		t.Errorf("Links: got %#v want %#v", got.Links, want.Links)
	}
	if !reflect.DeepEqual(got.Tags, want.Tags) {
		t.Errorf("Tags: got %#v want %#v", got.Tags, want.Tags)
	}
	if !reflect.DeepEqual(got.Sources, want.Sources) {
		t.Errorf("Sources: got %#v want %#v", got.Sources, want.Sources)
	}
	if !got.Created.Equal(want.Created) {
		t.Errorf("Created: got %v want %v", got.Created, want.Created)
	}
	if !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Errorf("UpdatedAt: got %v want %v", got.UpdatedAt, want.UpdatedAt)
	}
}

// TestRoundTrip_ExtraFrontmatter_ParseWritePreservesValues: round-trip
// a Page with non-empty ExtraFrontmatter under a schema declaring
// those fields; assert disk emission and re-parse preserve the values.
func TestRoundTrip_ExtraFrontmatter_ParseWritePreservesValues(t *testing.T) {
	dir := t.TempDir()
	sch := schema.Bundled()
	sch.Ontology.Fields = append(sch.Ontology.Fields,
		schema.OntologyField{CanonicalName: "priority", DeclaredName: "priority", Type: "string"},
		schema.OntologyField{CanonicalName: "owner", DeclaredName: "owner", Type: "string"},
	)
	want := Page{
		Title:       "Extras",
		Body:        "body\n",
		ContentHash: "h",
		UpdatedAt:   time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC),
		Evidence: []Evidence{
			{Quote: "q1", LineStart: 1, LineEnd: 1, SourceFilePath: "internal/db/db.go"},
		},
		ExtraFrontmatter: map[string]string{
			"priority": "high",
			"owner":    "alice",
		},
	}
	if err := WritePageWithSchema(want, dir, sch); err != nil {
		t.Fatalf("WritePageWithSchema: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "Extras.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, "priority: high\n") {
		t.Errorf("expected `priority: high` on disk; file:\n%s", s)
	}
	if !strings.Contains(s, "owner: alice\n") {
		t.Errorf("expected `owner: alice` on disk; file:\n%s", s)
	}
	// Alphabetical order: owner before priority.
	ownerIdx := strings.Index(s, "owner: alice")
	priIdx := strings.Index(s, "priority: high")
	if ownerIdx < 0 || priIdx < 0 || ownerIdx >= priIdx {
		t.Errorf("ExtraFrontmatter must emit alphabetically (owner before priority); ownerIdx=%d priIdx=%d\n%s", ownerIdx, priIdx, s)
	}
	got, err := ParsePageWithSchema(s, sch)
	if err != nil {
		t.Fatalf("ParsePageWithSchema: %v", err)
	}
	if got.ExtraFrontmatter["priority"] != "high" || got.ExtraFrontmatter["owner"] != "alice" {
		t.Errorf("ExtraFrontmatter round-trip lost values; got %#v", got.ExtraFrontmatter)
	}
}
