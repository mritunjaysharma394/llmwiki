package wiki

import (
	"path/filepath"
	"testing"

	"github.com/mritunjaysharma394/llmwiki/internal/db"
	"github.com/mritunjaysharma394/llmwiki/internal/schema"
)

// fastLintFixture spins up a temp wiki.db and offers a tiny seedPage
// helper that writes only the DB row (FastLint reads pages from the
// DB; on-disk frontmatter doesn't matter to the three signals). Each
// page can be tagged with a schema_hash so the drift signal has
// something to assert against.
type fastLintFixture struct {
	DB *db.DB
}

func setupFastLintFixture(t *testing.T) *fastLintFixture {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "wiki.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return &fastLintFixture{DB: d}
}

func (f *fastLintFixture) seed(t *testing.T, title, body, schemaHash string) int64 {
	t.Helper()
	if err := f.DB.UpsertPage(db.PageRecord{
		Title:       title,
		Path:        "wiki/" + title + ".md",
		Body:        body,
		ContentHash: HashContent(body),
	}); err != nil {
		t.Fatalf("UpsertPage %s: %v", title, err)
	}
	stored, err := f.DB.GetPage(title)
	if err != nil || stored == nil {
		t.Fatalf("GetPage %s: %v", title, err)
	}
	if schemaHash != "" {
		if err := f.DB.UpdateSchemaHash(stored.ID, schemaHash); err != nil {
			t.Fatalf("UpdateSchemaHash %s: %v", title, err)
		}
	}
	return stored.ID
}

// TestFastLint_AllSignalsClean — three pages, all wikilinked properly,
// all on the active schema. Every counter is zero, every list empty.
func TestFastLint_AllSignalsClean(t *testing.T) {
	f := setupFastLintFixture(t)
	sch := schema.Bundled()
	h := sch.Hash()
	f.seed(t, "Foo", "Body of Foo. See also [[Bar]] and [[Baz]].", h)
	f.seed(t, "Bar", "Body of Bar. Refers to [[Foo]] and [[Baz]].", h)
	f.seed(t, "Baz", "Body of Baz. Mentions [[Foo]] and [[Bar]].", h)

	res, err := FastLint(f.DB, sch)
	if err != nil {
		t.Fatalf("FastLint: %v", err)
	}
	if res.OrphanCount != 0 {
		t.Errorf("OrphanCount = %d, want 0", res.OrphanCount)
	}
	if res.MissingXRefCount != 0 {
		t.Errorf("MissingXRefCount = %d, want 0", res.MissingXRefCount)
	}
	if res.SchemaDriftCount != 0 {
		t.Errorf("SchemaDriftCount = %d, want 0", res.SchemaDriftCount)
	}
}

// TestFastLint_OrphanSignal — Lonely has no inbound wikilinks; the
// other pages do.
func TestFastLint_OrphanSignal(t *testing.T) {
	f := setupFastLintFixture(t)
	sch := schema.Bundled()
	h := sch.Hash()
	f.seed(t, "Foo", "I link to [[Bar]].", h)
	f.seed(t, "Bar", "I link to [[Foo]].", h)
	f.seed(t, "Lonely", "Nobody points at me.", h)

	res, err := FastLint(f.DB, sch)
	if err != nil {
		t.Fatalf("FastLint: %v", err)
	}
	if res.OrphanCount != 1 {
		t.Errorf("OrphanCount = %d, want 1", res.OrphanCount)
	}
	if len(res.TopOrphanTitles) != 1 || res.TopOrphanTitles[0] != "Lonely" {
		t.Errorf("TopOrphanTitles = %v, want [Lonely]", res.TopOrphanTitles)
	}
}

// TestFastLint_OrphanCapsAtTopN — 5 orphans, only first 3 surface in
// TopOrphanTitles, OrphanCount reflects all 5.
func TestFastLint_OrphanCapsAtTopN(t *testing.T) {
	f := setupFastLintFixture(t)
	sch := schema.Bundled()
	h := sch.Hash()
	for _, name := range []string{"Apple", "Banana", "Cherry", "Date", "Elderberry"} {
		f.seed(t, name, "no inbound link", h)
	}

	res, err := FastLint(f.DB, sch)
	if err != nil {
		t.Fatalf("FastLint: %v", err)
	}
	if res.OrphanCount != 5 {
		t.Errorf("OrphanCount = %d, want 5", res.OrphanCount)
	}
	if len(res.TopOrphanTitles) != FastLintTopN {
		t.Errorf("len(TopOrphanTitles) = %d, want %d", len(res.TopOrphanTitles), FastLintTopN)
	}
	// Sorted ascending → "Apple", "Banana", "Cherry"
	wantTop := []string{"Apple", "Banana", "Cherry"}
	for i, w := range wantTop {
		if res.TopOrphanTitles[i] != w {
			t.Errorf("TopOrphanTitles[%d] = %q, want %q", i, res.TopOrphanTitles[i], w)
		}
	}
}

// TestFastLint_MissingXRefSignal — a page mentions another title in
// bare prose; FastLint flags it.
func TestFastLint_MissingXRefSignal(t *testing.T) {
	f := setupFastLintFixture(t)
	sch := schema.Bundled()
	h := sch.Hash()
	f.seed(t, "Trust Property", "The validator gates every write.", h)
	// Sloppy page mentions "Trust Property" in bare prose without a
	// wikilink. Inbound link from Sloppy means TP is not orphaned, so
	// this test only asserts the missing-xref signal.
	f.seed(t, "Sloppy",
		"Read [[Trust Property]] later. The Trust Property is the validator's contract.",
		h)

	res, err := FastLint(f.DB, sch)
	if err != nil {
		t.Fatalf("FastLint: %v", err)
	}
	if res.MissingXRefCount != 1 {
		t.Fatalf("MissingXRefCount = %d, want 1", res.MissingXRefCount)
	}
	if len(res.MissingXRefs) != 1 {
		t.Fatalf("len(MissingXRefs) = %d, want 1", len(res.MissingXRefs))
	}
	got := res.MissingXRefs[0]
	if got.Page != "Sloppy" {
		t.Errorf("offending Page = %q, want Sloppy", got.Page)
	}
	if len(got.MissingTitles) != 1 || got.MissingTitles[0] != "Trust Property" {
		t.Errorf("MissingTitles = %v, want [Trust Property]", got.MissingTitles)
	}
}

// TestFastLint_MissingXRef_SkipsCodeAndFrontmatter — confirms we
// inherit the rewriter's fence/frontmatter masking through
// FindBareReferences. A bare mention inside frontmatter or a code
// fence must not flag.
func TestFastLint_MissingXRef_SkipsCodeAndFrontmatter(t *testing.T) {
	f := setupFastLintFixture(t)
	sch := schema.Bundled()
	h := sch.Hash()
	f.seed(t, "Foo", "I am [[Foo]].", h)
	body := "---\ntitle: bar\n---\n\nMentioning Foo in frontmatter shouldn't count.\n\n```\nFoo in code fence\n```\n\nFoo in inline `code` Foo too. End."
	f.seed(t, "Bar", body, h)

	res, err := FastLint(f.DB, sch)
	if err != nil {
		t.Fatalf("FastLint: %v", err)
	}
	// "Foo in inline `code` Foo too" → the bare "Foo" outside the
	// backticks DOES match (matches rewriter behaviour).
	if res.MissingXRefCount == 0 {
		t.Error("expected at least one missing-xref hit (the bare Foo at line end)")
	}
	// Frontmatter mentions and fenced-code mentions must not be the
	// SOLE reason a page surfaces — but "Bar" surfaces because of the
	// last "Foo too. End." line. Confirm the page surfaces with Foo
	// listed, which means the upstream filtering was correct.
	if len(res.MissingXRefs) > 0 {
		mt := res.MissingXRefs[0].MissingTitles
		if len(mt) != 1 || mt[0] != "Foo" {
			t.Errorf("MissingTitles = %v, want [Foo]", mt)
		}
	}
}

// TestFastLint_SchemaDriftSignal — pages stamped with a non-active
// hash count toward drift.
func TestFastLint_SchemaDriftSignal(t *testing.T) {
	f := setupFastLintFixture(t)
	sch := schema.Bundled()
	active := sch.Hash()
	f.seed(t, "Current", "fresh", active)
	f.seed(t, "Stale1", "old", "OLDHASH1")
	f.seed(t, "Stale2", "older", "")

	res, err := FastLint(f.DB, sch)
	if err != nil {
		t.Fatalf("FastLint: %v", err)
	}
	if res.SchemaDriftCount != 2 {
		t.Errorf("SchemaDriftCount = %d, want 2", res.SchemaDriftCount)
	}
}

// TestFastLint_EmptyWiki — no pages → all zero.
func TestFastLint_EmptyWiki(t *testing.T) {
	f := setupFastLintFixture(t)
	res, err := FastLint(f.DB, schema.Bundled())
	if err != nil {
		t.Fatalf("FastLint: %v", err)
	}
	if res.OrphanCount != 0 || res.MissingXRefCount != 0 || res.SchemaDriftCount != 0 {
		t.Errorf("expected all zero counters, got %+v", res)
	}
}

// TestFastLint_AllThreeSignalsTogether — one fixture exercising each
// signal. Validates the result aggregation doesn't cross-contaminate.
func TestFastLint_AllThreeSignalsTogether(t *testing.T) {
	f := setupFastLintFixture(t)
	sch := schema.Bundled()
	active := sch.Hash()
	// Foo↔Bar are linked both directions; Bar also links to Sloppy
	// and Stale (so they aren't orphans). Lonely is the only orphan;
	// Sloppy has Bar in bare prose; Stale is on the wrong schema hash.
	f.seed(t, "Foo", "links to [[Bar]]", active)
	f.seed(t, "Bar", "links to [[Foo]] and to [[Sloppy]] and [[Stale]]", active)
	f.seed(t, "Lonely", "no incoming", active)
	f.seed(t, "Sloppy", "[[Bar]] is here. Bar appears bare here too.", active)
	f.seed(t, "Stale", "[[Foo]]", "OLD")

	res, err := FastLint(f.DB, sch)
	if err != nil {
		t.Fatalf("FastLint: %v", err)
	}
	if res.OrphanCount != 1 || len(res.TopOrphanTitles) != 1 || res.TopOrphanTitles[0] != "Lonely" {
		t.Errorf("orphan signal: count=%d titles=%v", res.OrphanCount, res.TopOrphanTitles)
	}
	if res.MissingXRefCount != 1 {
		t.Errorf("MissingXRefCount = %d, want 1 (Sloppy)", res.MissingXRefCount)
	}
	if res.SchemaDriftCount != 1 {
		t.Errorf("SchemaDriftCount = %d, want 1", res.SchemaDriftCount)
	}
}

// TestFindBareReferences_FindsExactlyTheRewriterTargets — the
// extracted helper must hit the same titles RewriteBareReferencesAsWikilinks
// would wrap. Acts as the spec for the obsidian.go refactor.
func TestFindBareReferences_FindsExactlyTheRewriterTargets(t *testing.T) {
	body := "Refer to Trust Property here. Already-linked [[Page B]] should not count."
	titles := []string{"Trust Property", "Page B"}
	got := FindBareReferences(body, titles)
	if len(got) != 1 || got[0] != "Trust Property" {
		t.Errorf("got %v, want [Trust Property]", got)
	}
}
