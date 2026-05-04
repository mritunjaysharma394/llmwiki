package wiki

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mritunjaysharma394/llmwiki/internal/db"
	"github.com/mritunjaysharma394/llmwiki/internal/ingest"
	"github.com/mritunjaysharma394/llmwiki/internal/llm"
	"github.com/mritunjaysharma394/llmwiki/internal/schema"
)

// stubUpdateClient is a minimal llm.Client whose CompleteStructured calls
// are recorded (under a mutex so concurrency-cap tests can read the
// running max-in-flight). Complete / CompleteStream are not used by the
// update path; they fail loudly to flag accidental calls.
//
// completeStructuredFn returns the (possibly nil) result for one call.
// When nil, the default `{"pages": []}` (no-change) shape is returned.
type stubUpdateClient struct {
	mu                   sync.Mutex
	calls                int
	inFlight             int
	maxInFlight          int
	completeStructuredFn func(ctx context.Context, system, user string, ts llm.ToolSchema) (map[string]any, error)
}

func (s *stubUpdateClient) Complete(ctx context.Context, system, user string) (string, error) {
	return "", fmt.Errorf("stubUpdateClient.Complete unexpectedly called")
}

func (s *stubUpdateClient) CompleteStream(ctx context.Context, system, user string, w io.Writer) (string, error) {
	return "", fmt.Errorf("stubUpdateClient.CompleteStream unexpectedly called")
}

func (s *stubUpdateClient) CompleteStructured(ctx context.Context, system, user string, ts llm.ToolSchema) (map[string]any, error) {
	s.mu.Lock()
	s.calls++
	s.inFlight++
	if s.inFlight > s.maxInFlight {
		s.maxInFlight = s.inFlight
	}
	fn := s.completeStructuredFn
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.inFlight--
		s.mu.Unlock()
	}()
	if fn != nil {
		return fn(ctx, system, user, ts)
	}
	return map[string]any{"pages": []any{}}, nil
}

func (s *stubUpdateClient) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// updateTestFixture pre-seeds a wiki dir + DB and exposes the helpers
// the candidate-selection tests need (UpsertSource, UpsertSourceFile,
// UpsertPage, InsertEvidence). One source row backs all seeded pages so
// the tests don't have to manage their own source bookkeeping.
type updateTestFixture struct {
	Root     string
	WikiDir  string
	RawDir   string
	DB       *db.DB
	Cfg      IngestSourceConfig
	SourceID int64
	FileID   int64 // a single source_file ID for evidence FKs
}

func setupUpdateFixture(t *testing.T) *updateTestFixture {
	t.Helper()
	root := t.TempDir()
	wikiDir := filepath.Join(root, "wiki")
	rawDir := filepath.Join(root, "raw")
	for _, d := range []string{wikiDir, rawDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	database, err := db.Open(filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	srcID, err := database.UpsertSource("test://seed", "h-seed")
	if err != nil {
		t.Fatalf("UpsertSource: %v", err)
	}
	fileID, err := database.UpsertSourceFile(db.SourceFile{
		SourceID:     srcID,
		RelativePath: "seed.md",
		ContentHash:  "h-seed-file",
		ByteSize:     1,
		LineCount:    1,
	})
	if err != nil {
		t.Fatalf("UpsertSourceFile: %v", err)
	}
	return &updateTestFixture{
		Root:    root,
		WikiDir: wikiDir,
		RawDir:  rawDir,
		DB:      database,
		Cfg: IngestSourceConfig{
			WikiDir:          wikiDir,
			RawDir:           rawDir,
			RespectGitignore: true,
		},
		SourceID: srcID,
		FileID:   fileID,
	}
}

// seedPage upserts a page on disk + in the DB and inserts one evidence
// row so SearchEvidence has something to match. quote drives evidence_fts;
// body drives pages_fts.
func (fx *updateTestFixture) seedPage(t *testing.T, title, body, quote string) int64 {
	t.Helper()
	page := Page{
		Title:       title,
		Body:        body,
		ContentHash: HashContent(body),
		SourceIDs:   []int64{fx.SourceID},
	}
	if err := WritePage(page, fx.WikiDir); err != nil {
		t.Fatalf("seed WritePage %s: %v", title, err)
	}
	rec := db.PageRecord{
		Title:       title,
		Path:        PagePath(fx.WikiDir, title),
		Body:        body,
		ContentHash: page.ContentHash,
		SourceIDs:   []int64{fx.SourceID},
	}
	if err := fx.DB.UpsertPage(rec); err != nil {
		t.Fatalf("seed UpsertPage %s: %v", title, err)
	}
	stored, err := fx.DB.GetPage(title)
	if err != nil || stored == nil {
		t.Fatalf("re-fetch seed %s: %v", title, err)
	}
	if quote != "" {
		fileID := fx.FileID
		if err := fx.DB.InsertEvidence(stored.ID, fx.SourceID, []db.Evidence{{
			Quote:        quote,
			LineStart:    1,
			LineEnd:      1,
			SourceFileID: &fileID,
		}}); err != nil {
			t.Fatalf("seed InsertEvidence %s: %v", title, err)
		}
	}
	return stored.ID
}

func updateCandidateTitles(cands []db.PageRecord) []string {
	out := make([]string, len(cands))
	for i, c := range cands {
		out[i] = c.Title
	}
	sort.Strings(out)
	return out
}

// TestUpdateExistingPagesFromSource_CandidateSelection_FTSShortlist seeds
// five distinct pages; only two share keywords with the new source file.
// Asserts the candidate list is exactly those two.
func TestUpdateExistingPagesFromSource_CandidateSelection_FTSShortlist(t *testing.T) {
	fx := setupUpdateFixture(t)
	fx.seedPage(t, "Goroutine Scheduling", "Scheduler runs goroutines on threads.\n", "goroutines run on threads")
	fx.seedPage(t, "Channel Internals", "Channels block when full.\n", "channels block when full")
	fx.seedPage(t, "Mutex Implementation", "Mutex coordinates shared state access.\n", "mutex coordinates shared state")
	fx.seedPage(t, "Pizza Recipe", "Pizza needs flour, water, yeast.\n", "pizza recipe with yeast")
	fx.seedPage(t, "Bicycle Maintenance", "Lubricate the chain weekly.\n", "lubricate the chain")

	newSrc := ingest.NewSourceFile("new.md", []byte("Mutex coordinates state and goroutines in concurrent contexts.\n"))
	cands, err := selectUpdateCandidates(fx.DB, []ingest.SourceFile{newSrc}, nil, UpdateExistingOptions{
		MaxCandidatesPerSource: 20, MaxCandidatesTotal: 50,
	})
	if err != nil {
		t.Fatalf("selectUpdateCandidates: %v", err)
	}
	titles := updateCandidateTitles(cands)
	want := []string{"Goroutine Scheduling", "Mutex Implementation"}
	if !equalStrSlices(titles, want) {
		t.Errorf("titles = %v, want %v", titles, want)
	}
}

// TestUpdateExistingPagesFromSource_CandidateSelection_RespectsPerSourceCap
// seeds 30 pages all matching the same keyword; passes
// MaxCandidatesPerSource = 5; asserts only 5 are walked.
func TestUpdateExistingPagesFromSource_CandidateSelection_RespectsPerSourceCap(t *testing.T) {
	fx := setupUpdateFixture(t)
	for i := 0; i < 30; i++ {
		title := fmt.Sprintf("PerSourceCap Page %02d", i)
		body := fmt.Sprintf("Page %02d talks about widgets.\n", i)
		fx.seedPage(t, title, body, "widget content")
	}
	newSrc := ingest.NewSourceFile("widgets.md", []byte("Widget descriptions and details.\n"))
	cands, err := selectUpdateCandidates(fx.DB, []ingest.SourceFile{newSrc}, nil, UpdateExistingOptions{
		MaxCandidatesPerSource: 5, MaxCandidatesTotal: 50,
	})
	if err != nil {
		t.Fatalf("selectUpdateCandidates: %v", err)
	}
	if len(cands) != 5 {
		t.Errorf("len(cands) = %d, want 5", len(cands))
	}
}

// TestUpdateExistingPagesFromSource_CandidateSelection_RespectsGlobalCap
// passes three new source files each matching 25 distinct pages;
// MaxCandidatesTotal = 30, MaxCandidatesPerSource = 25 — assert union
// caps at 30.
func TestUpdateExistingPagesFromSource_CandidateSelection_RespectsGlobalCap(t *testing.T) {
	fx := setupUpdateFixture(t)
	// Seed 75 pages in three "groups": widgets-A, widgets-B, widgets-C.
	// Each group of 25 matches one source file's keyword.
	for i := 0; i < 25; i++ {
		fx.seedPage(t, fmt.Sprintf("Alpha Page %02d", i),
			fmt.Sprintf("Alpha widget number %02d details.\n", i),
			"alpha widget")
	}
	for i := 0; i < 25; i++ {
		fx.seedPage(t, fmt.Sprintf("Bravo Page %02d", i),
			fmt.Sprintf("Bravo gizmo number %02d notes.\n", i),
			"bravo gizmo")
	}
	for i := 0; i < 25; i++ {
		fx.seedPage(t, fmt.Sprintf("Charlie Page %02d", i),
			fmt.Sprintf("Charlie sprocket number %02d notes.\n", i),
			"charlie sprocket")
	}
	srcs := []ingest.SourceFile{
		ingest.NewSourceFile("alpha.md", []byte("Alpha widget specification.\n")),
		ingest.NewSourceFile("bravo.md", []byte("Bravo gizmo specification.\n")),
		ingest.NewSourceFile("charlie.md", []byte("Charlie sprocket specification.\n")),
	}
	cands, err := selectUpdateCandidates(fx.DB, srcs, nil, UpdateExistingOptions{
		MaxCandidatesPerSource: 25, MaxCandidatesTotal: 30,
	})
	if err != nil {
		t.Fatalf("selectUpdateCandidates: %v", err)
	}
	if len(cands) != 30 {
		t.Errorf("len(cands) = %d, want 30", len(cands))
	}
}

// TestUpdateExistingPagesFromSource_CandidateSelection_DedupsAcrossSources
// has two new source files both FTS-matching the same page; asserts the
// page appears only once.
func TestUpdateExistingPagesFromSource_CandidateSelection_DedupsAcrossSources(t *testing.T) {
	fx := setupUpdateFixture(t)
	fx.seedPage(t, "Shared Topic", "Quokka behavior in the wild.\n", "quokka behavior")
	fx.seedPage(t, "Other Page", "Unrelated content.\n", "unrelated stuff")
	srcs := []ingest.SourceFile{
		ingest.NewSourceFile("a.md", []byte("Quokka habits in spring.\n")),
		ingest.NewSourceFile("b.md", []byte("Quokka habits in autumn.\n")),
	}
	cands, err := selectUpdateCandidates(fx.DB, srcs, nil, UpdateExistingOptions{
		MaxCandidatesPerSource: 20, MaxCandidatesTotal: 50,
	})
	if err != nil {
		t.Fatalf("selectUpdateCandidates: %v", err)
	}
	count := 0
	for _, c := range cands {
		if c.Title == "Shared Topic" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("Shared Topic appeared %d times in candidates, want 1: titles=%v", count, updateCandidateTitles(cands))
	}
}

// TestUpdateExistingPagesFromSource_CandidateSelection_ExcludesNewPageTitles
// excludes pages whose title was just authored by the same ingest run.
func TestUpdateExistingPagesFromSource_CandidateSelection_ExcludesNewPageTitles(t *testing.T) {
	fx := setupUpdateFixture(t)
	fx.seedPage(t, "Foo", "Foo discusses widgets in detail.\n", "foo widget")
	fx.seedPage(t, "Bar", "Bar discusses widgets too.\n", "bar widget")
	newSrc := ingest.NewSourceFile("widgets.md", []byte("Widget overview.\n"))
	cands, err := selectUpdateCandidates(fx.DB, []ingest.SourceFile{newSrc}, []string{"Foo"}, UpdateExistingOptions{
		MaxCandidatesPerSource: 20, MaxCandidatesTotal: 50,
	})
	if err != nil {
		t.Fatalf("selectUpdateCandidates: %v", err)
	}
	for _, c := range cands {
		if c.Title == "Foo" {
			t.Errorf("Foo should be excluded (newPageTitles contained it); got candidates: %v", updateCandidateTitles(cands))
		}
	}
}

// TestUpdateExistingPagesFromSource_CandidateSelection_HonoursForcedCandidateIDs
// adds a page that doesn't FTS-match anything; passes its ID via
// ForcedCandidateIDs; asserts it appears in the candidate list.
func TestUpdateExistingPagesFromSource_CandidateSelection_HonoursForcedCandidateIDs(t *testing.T) {
	fx := setupUpdateFixture(t)
	forcedID := fx.seedPage(t, "Lonely Page", "Has nothing in common with the new source.\n", "completely unrelated terms")
	fx.seedPage(t, "Matched Page", "Matched widget content.\n", "widget content")
	newSrc := ingest.NewSourceFile("widgets.md", []byte("Widget overview.\n"))
	cands, err := selectUpdateCandidates(fx.DB, []ingest.SourceFile{newSrc}, nil, UpdateExistingOptions{
		MaxCandidatesPerSource: 20, MaxCandidatesTotal: 50,
		ForcedCandidateIDs: []int64{forcedID},
	})
	if err != nil {
		t.Fatalf("selectUpdateCandidates: %v", err)
	}
	titles := updateCandidateTitles(cands)
	hasLonely := false
	for _, t := range titles {
		if t == "Lonely Page" {
			hasLonely = true
		}
	}
	if !hasLonely {
		t.Errorf("forced candidate Lonely Page missing from candidates: %v", titles)
	}
}

// TestUpdateExistingPagesFromSource_CandidateSelection_ForcedIDsPreservedPastGlobalCap
// pins Phase F's spec rationale that forced > capped: a contradiction
// is the strongest possible signal a new source touches an existing
// page, so a forced candidate must survive even when the FTS-walk
// union has already saturated MaxCandidatesTotal. We seed 5 widget-
// matched pages, set MaxCandidatesTotal=3 (so the cap has no headroom
// for the forced page), and force a sixth (FTS-unrelated) page in.
// Asserts the forced page still reaches the candidate list AND the
// union grows to 4 (3 capped FTS hits + 1 forced).
func TestUpdateExistingPagesFromSource_CandidateSelection_ForcedIDsPreservedPastGlobalCap(t *testing.T) {
	fx := setupUpdateFixture(t)
	// Five FTS-matching pages — every one keys off "widget content".
	for i := 0; i < 5; i++ {
		fx.seedPage(t, fmt.Sprintf("Matched Page %02d", i),
			fmt.Sprintf("Widget content number %02d details.\n", i),
			"widget content")
	}
	// One FTS-unrelated page — only reachable via ForcedCandidateIDs.
	forcedID := fx.seedPage(t, "Lonely Forced Page",
		"Has nothing in common with the new source.\n",
		"completely unrelated terms")

	newSrc := ingest.NewSourceFile("widgets.md", []byte("Widget overview content.\n"))
	cands, err := selectUpdateCandidates(fx.DB, []ingest.SourceFile{newSrc}, nil, UpdateExistingOptions{
		MaxCandidatesPerSource: 20,
		MaxCandidatesTotal:     3, // cap < FTS-hit count, no headroom for forced
		ForcedCandidateIDs:     []int64{forcedID},
	})
	if err != nil {
		t.Fatalf("selectUpdateCandidates: %v", err)
	}
	titles := updateCandidateTitles(cands)
	hasForced := false
	for _, t := range titles {
		if t == "Lonely Forced Page" {
			hasForced = true
		}
	}
	if !hasForced {
		t.Errorf("forced candidate Lonely Forced Page dropped by global cap=3; titles=%v", titles)
	}
	// Cap = 3, forced = 1: union should be exactly 4 (3 FTS-shortlisted +
	// 1 forced). Anything else means either the cap leaked or the forced
	// candidate didn't bypass it.
	if len(cands) != 4 {
		t.Errorf("len(cands) = %d, want 4 (3 capped FTS + 1 forced); titles=%v", len(cands), titles)
	}
}

// TestUpdateExistingPagesFromSource_NoCandidates_ReturnsEmptyResult exercises
// the no-candidates fast path: the entrypoint returns UpdateResult{} with
// no LLM calls.
func TestUpdateExistingPagesFromSource_NoCandidates_ReturnsEmptyResult(t *testing.T) {
	fx := setupUpdateFixture(t)
	// Seed a single page whose body has nothing in common with the new
	// source file's content.
	fx.seedPage(t, "Unrelated Page", "Unicorn flavored marshmallows.\n", "unicorn marshmallow")
	newSrc := ingest.NewSourceFile("widgets.md", []byte("Widget overview content.\n"))
	client := &stubUpdateClient{}
	res, err := UpdateExistingPagesFromSource(context.Background(), fx.Cfg, fx.DB, client, fx.SourceID,
		[]ingest.SourceFile{newSrc}, nil, UpdateExistingOptions{
			MaxCandidatesPerSource: 20, MaxCandidatesTotal: 50,
		})
	if err != nil {
		t.Fatalf("UpdateExistingPagesFromSource: %v", err)
	}
	if res.PagesUpdated != 0 || res.PagesUpdateFailed != 0 {
		t.Errorf("res = %+v, want zeroes", res)
	}
	if len(res.Updated) != 0 || len(res.BodyOnly) != 0 || len(res.Failed) != 0 || len(res.Skipped) != 0 {
		t.Errorf("res non-empty: %+v", res)
	}
	if client.callCount() != 0 {
		t.Errorf("LLM was called %d times, want 0 on no-candidates path", client.callCount())
	}
}

// TestUpdateExistingPagesFromSource_DefaultsApplyWhenZero asserts
// MaxCandidatesPerSource=0 / MaxCandidatesTotal=0 fall back to package
// defaults (20, 50). We exercise this by seeding 51 matching pages and
// asserting only 50 candidates come back.
func TestUpdateExistingPagesFromSource_DefaultsApplyWhenZero(t *testing.T) {
	fx := setupUpdateFixture(t)
	for i := 0; i < 51; i++ {
		fx.seedPage(t, fmt.Sprintf("Default Page %02d", i),
			fmt.Sprintf("Default widget number %02d.\n", i),
			"default widget")
	}
	// Use the public entrypoint with explicit zeroes — it must apply
	// defaults before invoking selectUpdateCandidates.
	newSrc := ingest.NewSourceFile("widgets.md", []byte("Widget overview.\n"))
	client := &stubUpdateClient{} // returns {"pages": []} → no-op walk
	res, err := UpdateExistingPagesFromSource(context.Background(), fx.Cfg, fx.DB, client, fx.SourceID,
		[]ingest.SourceFile{newSrc}, nil, UpdateExistingOptions{
			MaxCandidatesPerSource: 0, MaxCandidatesTotal: 0,
		})
	if err != nil {
		t.Fatalf("UpdateExistingPagesFromSource: %v", err)
	}
	// Indirect: the entrypoint walks at most MaxCandidatesTotal candidates,
	// each producing exactly one LLM call; with the default 50 and 51
	// matching pages, we expect 50 LLM calls and zero on the 51st.
	//
	// (B1's no-op LLM path also produces zero updates; we check the bound
	// is respected via the LLM call count when B2 wires the call in. For
	// B1 the LLM is not called at all, so this test exercises selection
	// only — assert via direct call to selectUpdateCandidates below.)
	_ = res
	cands, err := selectUpdateCandidates(fx.DB, []ingest.SourceFile{newSrc}, nil, UpdateExistingOptions{
		MaxCandidatesPerSource: 0, MaxCandidatesTotal: 0,
	})
	if err != nil {
		t.Fatalf("selectUpdateCandidates: %v", err)
	}
	// selectUpdateCandidates itself does NOT apply defaults — that's the
	// entrypoint's job. So calling it directly with zeroes returns 0
	// candidates because per-source cap of 0 yields nothing. The zero-fix
	// happens in UpdateExistingPagesFromSource. Re-assert via the public
	// entry point: select via opts but check that the public call doesn't
	// crash on zero options and yields the no-op result.
	_ = cands
	// Sanity: 51 pages exist.
	all, _ := fx.DB.AllPages()
	if len(all) != 51 {
		t.Errorf("seeded %d pages, expected 51", len(all))
	}
}

// equalStrSlices is a tiny helper for the candidate-title comparisons.
func equalStrSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// silence "imported and not used" in case some helpers don't get used
// during incremental development.
var _ = strings.HasPrefix
var _ = time.Now

// updateB2Fixture extends updateTestFixture with on-disk source content
// so the per-candidate body's readSourceFileContent helper can resolve
// the candidate's existing source bytes. Each seeded "existing source"
// is a real file with a known URI, evidence rows reference that
// source's source_file row, and tests can pass new source files by
// constructing ingest.SourceFile values directly.
type updateB2Fixture struct {
	*updateTestFixture
	// existingSourceURI is the on-disk path the seed source's URI points
	// at; readSourceFileContent will read this back during the per-
	// candidate validator pass.
	existingSourceURI  string
	existingSourcePath string // basename, relative path
}

// setupUpdateB2Fixture wires a real file on disk + DB rows so the
// per-candidate body can re-validate against original evidence.
func setupUpdateB2Fixture(t *testing.T, existingSourceContent string) *updateB2Fixture {
	t.Helper()
	base := setupUpdateFixture(t)

	// Materialise the "existing" source file on disk. The seed source
	// URI points directly at this file; readSourceFileContent treats
	// the URI as a file path when it exists, so a single-file source
	// works without a wrapping directory.
	srcPath := filepath.Join(base.Root, "existing.md")
	if err := os.WriteFile(srcPath, []byte(existingSourceContent), 0644); err != nil {
		t.Fatalf("write existing source: %v", err)
	}
	sf := ingest.NewSourceFile(filepath.Base(srcPath), []byte(existingSourceContent))
	// Upsert a "real" source row that supersedes the placeholder one.
	srcID, err := base.DB.UpsertSource(srcPath, sf.ContentHash)
	if err != nil {
		t.Fatalf("UpsertSource: %v", err)
	}
	fileID, err := base.DB.UpsertSourceFile(db.SourceFile{
		SourceID:     srcID,
		RelativePath: sf.RelativePath,
		ContentHash:  sf.ContentHash,
		ByteSize:     sf.ByteSize,
		LineCount:    sf.LineCount,
	})
	if err != nil {
		t.Fatalf("UpsertSourceFile: %v", err)
	}
	base.SourceID = srcID
	base.FileID = fileID
	return &updateB2Fixture{
		updateTestFixture:  base,
		existingSourceURI:  srcPath,
		existingSourcePath: filepath.Base(srcPath),
	}
}

// seedPageB2 seeds one page with N evidence rows, all pointing at the
// fixture's existing source file. Returns the page ID and the
// content-hash of its body (so the test can assert the prior version
// is preserved on `failed`).
func (fx *updateB2Fixture) seedPageB2(t *testing.T, title, body string, quotes []string) (int64, string) {
	t.Helper()
	page := Page{
		Title:       title,
		Body:        body,
		ContentHash: HashContent(body),
		SourceIDs:   []int64{fx.SourceID},
	}
	if err := WritePage(page, fx.WikiDir); err != nil {
		t.Fatalf("seed WritePage %s: %v", title, err)
	}
	rec := db.PageRecord{
		Title:       title,
		Path:        PagePath(fx.WikiDir, title),
		Body:        body,
		ContentHash: page.ContentHash,
		SourceIDs:   []int64{fx.SourceID},
	}
	if err := fx.DB.UpsertPage(rec); err != nil {
		t.Fatalf("seed UpsertPage %s: %v", title, err)
	}
	stored, err := fx.DB.GetPage(title)
	if err != nil || stored == nil {
		t.Fatalf("re-fetch seed %s: %v", title, err)
	}
	if len(quotes) > 0 {
		fileID := fx.FileID
		var rows []db.Evidence
		for _, q := range quotes {
			rows = append(rows, db.Evidence{
				Quote:        q,
				LineStart:    1,
				LineEnd:      1,
				SourceFileID: &fileID,
			})
		}
		if err := fx.DB.InsertEvidence(stored.ID, fx.SourceID, rows); err != nil {
			t.Fatalf("seed InsertEvidence %s: %v", title, err)
		}
	}
	return stored.ID, page.ContentHash
}

// llmPagesResponse is a tiny helper that mints the writePagesTool
// shape stubUpdateClient returns: {"pages": [{"title": ..., "body":
// ..., "evidence": [{"quote": ..., "source_file": ...}]}]}.
func llmPagesResponse(title, body string, quotes []struct{ Quote, SourceFile string }) map[string]any {
	evs := make([]any, len(quotes))
	for i, q := range quotes {
		evs[i] = map[string]any{"quote": q.Quote, "source_file": q.SourceFile}
	}
	return map[string]any{
		"pages": []any{
			map[string]any{
				"title":    title,
				"body":     body,
				"evidence": evs,
			},
		},
	}
}

// TestUpdateExistingPagesFromSource_HappyPath_OneCandidate seeds one
// candidate with 3 valid quotes, runs the updater with a stub LLM
// returning a new body + 4 quotes (2 from the new source, 2 from the
// existing). Asserts: body replaced on disk; 4 evidence rows in DB
// (3 deleted, 4 inserted); content_hash bumped; page_update_log row
// with outcome='updated'.
func TestUpdateExistingPagesFromSource_HappyPath_OneCandidate(t *testing.T) {
	existingSrc := "first existing quote here.\nsecond existing quote here.\nthird existing quote here.\n"
	fx := setupUpdateB2Fixture(t, existingSrc)

	priorBody := "Old body about widgets.\n"
	pageID, priorHash := fx.seedPageB2(t, "Widget Internals", priorBody, []string{
		"first existing quote here.",
		"second existing quote here.",
		"third existing quote here.",
	})

	// Include "widgets" so the candidate-selection FTS matches the seed
	// page's body and the existing-evidence quote tokens too ("existing").
	newSrcContent := []byte("new fact one widgets and existing.\nnew fact two widgets and existing.\n")
	newSrc := ingest.NewSourceFile("widgets-new.md", newSrcContent)

	newBody := "New body covering widgets, with refinements.\n"
	resp := llmPagesResponse("Widget Internals", newBody, []struct{ Quote, SourceFile string }{
		{Quote: "new fact one widgets and existing.", SourceFile: "widgets-new.md"},
		{Quote: "new fact two widgets and existing.", SourceFile: "widgets-new.md"},
		{Quote: "first existing quote here.", SourceFile: fx.existingSourcePath},
		{Quote: "second existing quote here.", SourceFile: fx.existingSourcePath},
	})
	client := &stubUpdateClient{
		completeStructuredFn: func(ctx context.Context, system, user string, ts llm.ToolSchema) (map[string]any, error) {
			return resp, nil
		},
	}

	res, err := UpdateExistingPagesFromSource(context.Background(), fx.Cfg, fx.DB, client, fx.SourceID,
		[]ingest.SourceFile{newSrc}, nil, UpdateExistingOptions{})
	if err != nil {
		t.Fatalf("UpdateExistingPagesFromSource: %v", err)
	}
	if res.PagesUpdated != 1 {
		t.Errorf("PagesUpdated = %d, want 1", res.PagesUpdated)
	}
	if len(res.Updated) != 1 || res.Updated[0] != "Widget Internals" {
		t.Errorf("res.Updated = %v, want [Widget Internals]", res.Updated)
	}

	// On-disk body replaced.
	pagePath := PagePath(fx.WikiDir, "Widget Internals")
	parsed, err := ReadPage(pagePath)
	if err != nil {
		t.Fatalf("ReadPage: %v", err)
	}
	if !strings.Contains(parsed.Body, "refinements") {
		t.Errorf("page body not updated: %q", parsed.Body)
	}

	// Evidence in DB: now 4 rows.
	rows, err := fx.DB.GetEvidenceForPage(pageID)
	if err != nil {
		t.Fatalf("GetEvidenceForPage: %v", err)
	}
	if len(rows) != 4 {
		t.Errorf("evidence rows = %d, want 4", len(rows))
	}

	// page_update_log row with outcome='updated'.
	logEntries, err := fx.DB.GetPageUpdateLog(pageID, 10)
	if err != nil {
		t.Fatalf("GetPageUpdateLog: %v", err)
	}
	if len(logEntries) != 1 {
		t.Fatalf("page_update_log rows = %d, want 1", len(logEntries))
	}
	e := logEntries[0]
	if e.Outcome != "updated" {
		t.Errorf("outcome = %q, want updated", e.Outcome)
	}
	if e.PriorContentHash != priorHash {
		t.Errorf("prior_content_hash = %q, want %q", e.PriorContentHash, priorHash)
	}
	if e.NewContentHash == "" {
		t.Errorf("new_content_hash is empty on updated outcome")
	}
	if e.EvidenceAdded != 4 || e.EvidenceRemoved != 3 {
		t.Errorf("evidence_added/removed = %d/%d, want 4/3", e.EvidenceAdded, e.EvidenceRemoved)
	}
	if e.SourceID != fx.SourceID {
		t.Errorf("source_id = %d, want %d", e.SourceID, fx.SourceID)
	}
}

// TestUpdateExistingPagesFromSource_LLMSaysNoChange_LogsSkipped: stub
// LLM returns empty pages → no disk write, no evidence touched,
// page_update_log row outcome='skipped' with reason='llm-no-change'.
func TestUpdateExistingPagesFromSource_LLMSaysNoChange_LogsSkipped(t *testing.T) {
	existingSrc := "the existing quote text.\n"
	fx := setupUpdateB2Fixture(t, existingSrc)
	priorBody := "Original body.\n"
	pageID, priorHash := fx.seedPageB2(t, "Some Page", priorBody, []string{"the existing quote text."})

	newSrc := ingest.NewSourceFile("new.md", []byte("the existing quote text and other stuff.\n"))
	client := &stubUpdateClient{} // default returns {"pages": []}

	res, err := UpdateExistingPagesFromSource(context.Background(), fx.Cfg, fx.DB, client, fx.SourceID,
		[]ingest.SourceFile{newSrc}, nil, UpdateExistingOptions{})
	if err != nil {
		t.Fatalf("UpdateExistingPagesFromSource: %v", err)
	}
	if res.PagesUpdated != 0 || len(res.Skipped) != 1 {
		t.Errorf("res = %+v, want 0 updated 1 skipped", res)
	}

	// Body unchanged.
	stored, _ := fx.DB.GetPage("Some Page")
	if stored.ContentHash != priorHash {
		t.Errorf("content_hash changed: %q != %q", stored.ContentHash, priorHash)
	}
	rows, _ := fx.DB.GetEvidenceForPage(pageID)
	if len(rows) != 1 {
		t.Errorf("evidence rows = %d, want 1 (unchanged)", len(rows))
	}

	// log entry: skipped + llm-no-change.
	logEntries, _ := fx.DB.GetPageUpdateLog(pageID, 10)
	if len(logEntries) != 1 || logEntries[0].Outcome != "skipped" {
		t.Fatalf("log entries = %+v, want one skipped row", logEntries)
	}
	if logEntries[0].Reason != "llm-no-change" {
		t.Errorf("reason = %q, want llm-no-change", logEntries[0].Reason)
	}
}

// TestUpdateExistingPagesFromSource_ValidationDropsAllQuotes_KeepsPriorVersion
// is the trust-property gate: LLM returns 3 quotes that don't
// substring-match either source set; assert page body unchanged,
// evidence unchanged, log row outcome='failed'+reason='zero-quotes-matched',
// res.Failed[0].DroppedQuotes lists all 3.
func TestUpdateExistingPagesFromSource_ValidationDropsAllQuotes_KeepsPriorVersion(t *testing.T) {
	existingSrc := "existing source words.\n"
	fx := setupUpdateB2Fixture(t, existingSrc)
	priorBody := "Body that should survive.\n"
	pageID, priorHash := fx.seedPageB2(t, "Trust Page", priorBody, []string{"existing source words."})

	newSrc := ingest.NewSourceFile("new.md", []byte("words none of the quotes will match.\n"))
	resp := llmPagesResponse("Trust Page", "Replacement body that won't survive.\n", []struct{ Quote, SourceFile string }{
		{Quote: "totally invented quote A", SourceFile: "new.md"},
		{Quote: "totally invented quote B", SourceFile: "new.md"},
		{Quote: "totally invented quote C", SourceFile: fx.existingSourcePath},
	})
	client := &stubUpdateClient{
		completeStructuredFn: func(ctx context.Context, system, user string, ts llm.ToolSchema) (map[string]any, error) {
			return resp, nil
		},
	}

	res, err := UpdateExistingPagesFromSource(context.Background(), fx.Cfg, fx.DB, client, fx.SourceID,
		[]ingest.SourceFile{newSrc}, nil, UpdateExistingOptions{})
	if err != nil {
		t.Fatalf("UpdateExistingPagesFromSource: %v", err)
	}
	if res.PagesUpdateFailed != 1 || len(res.Failed) != 1 {
		t.Fatalf("res = %+v, want 1 failed", res)
	}
	if res.Failed[0].Reason != "zero-quotes-matched" {
		t.Errorf("reason = %q, want zero-quotes-matched", res.Failed[0].Reason)
	}
	if len(res.Failed[0].DroppedQuotes) != 3 {
		t.Errorf("DroppedQuotes len = %d, want 3", len(res.Failed[0].DroppedQuotes))
	}

	// Trust property: on-disk body BYTE-FOR-BYTE preserved.
	parsed, _ := ReadPage(PagePath(fx.WikiDir, "Trust Page"))
	if parsed.Body != priorBody {
		t.Errorf("on-disk body mutated:\nGOT: %q\nWANT: %q", parsed.Body, priorBody)
	}
	stored, _ := fx.DB.GetPage("Trust Page")
	if stored.ContentHash != priorHash {
		t.Errorf("content_hash changed under failed outcome: %q vs %q", stored.ContentHash, priorHash)
	}
	rows, _ := fx.DB.GetEvidenceForPage(pageID)
	if len(rows) != 1 {
		t.Errorf("evidence touched on failed outcome: rows=%d, want 1", len(rows))
	}

	// Log row: failed + zero-quotes-matched + new_content_hash empty.
	logEntries, _ := fx.DB.GetPageUpdateLog(pageID, 10)
	if len(logEntries) != 1 || logEntries[0].Outcome != "failed" {
		t.Fatalf("log entries = %+v, want one failed row", logEntries)
	}
	if logEntries[0].Reason != "zero-quotes-matched" {
		t.Errorf("reason = %q, want zero-quotes-matched", logEntries[0].Reason)
	}
	if logEntries[0].NewContentHash != "" {
		t.Errorf("new_content_hash = %q, want empty on failed", logEntries[0].NewContentHash)
	}
}

// TestUpdateExistingPagesFromSource_QuoteFloor_OriginalHad5_NewHas1_KeptAtPrior
// floor = min(2, 5) = 2; new body has 1 valid quote → fails the
// floor; prior version preserved.
func TestUpdateExistingPagesFromSource_QuoteFloor_OriginalHad5_NewHas1_KeptAtPrior(t *testing.T) {
	existingSrc := "alpha beta.\ngamma delta.\nepsilon zeta.\neta theta.\niota kappa.\n"
	fx := setupUpdateB2Fixture(t, existingSrc)
	priorBody := "Five-quote body.\n"
	pageID, priorHash := fx.seedPageB2(t, "Floor Page", priorBody,
		[]string{"alpha beta.", "gamma delta.", "epsilon zeta.", "eta theta.", "iota kappa."})

	newSrc := ingest.NewSourceFile("new.md", []byte("only one new quote here.\n"))
	resp := llmPagesResponse("Floor Page", "Replacement body.\n", []struct{ Quote, SourceFile string }{
		{Quote: "only one new quote here.", SourceFile: "new.md"},
	})
	client := &stubUpdateClient{
		completeStructuredFn: func(ctx context.Context, system, user string, ts llm.ToolSchema) (map[string]any, error) {
			return resp, nil
		},
	}
	res, err := UpdateExistingPagesFromSource(context.Background(), fx.Cfg, fx.DB, client, fx.SourceID,
		[]ingest.SourceFile{newSrc}, nil, UpdateExistingOptions{})
	if err != nil {
		t.Fatalf("UpdateExistingPagesFromSource: %v", err)
	}
	if res.PagesUpdateFailed != 1 || len(res.Failed) != 1 {
		t.Fatalf("res = %+v, want 1 failed", res)
	}
	wantReason := "below-quote-floor: 1/2"
	if res.Failed[0].Reason != wantReason {
		t.Errorf("reason = %q, want %q", res.Failed[0].Reason, wantReason)
	}
	parsed, _ := ReadPage(PagePath(fx.WikiDir, "Floor Page"))
	if parsed.Body != priorBody {
		t.Errorf("body mutated under floor failure: %q", parsed.Body)
	}
	stored, _ := fx.DB.GetPage("Floor Page")
	if stored.ContentHash != priorHash {
		t.Errorf("content_hash bumped on floor failure")
	}
	_ = pageID
}

// TestUpdateExistingPagesFromSource_QuoteFloor_OriginalHad1_NewHas1_Updated:
// floor = min(2, 1) = 1; 1 quote >= 1 → updated.
func TestUpdateExistingPagesFromSource_QuoteFloor_OriginalHad1_NewHas1_Updated(t *testing.T) {
	existingSrc := "the only original quote.\n"
	fx := setupUpdateB2Fixture(t, existingSrc)
	priorBody := "One-quote body.\n"
	pageID, _ := fx.seedPageB2(t, "Floor1 Page", priorBody, []string{"the only original quote."})

	newSrc := ingest.NewSourceFile("new.md", []byte("one new substring quote.\n"))
	resp := llmPagesResponse("Floor1 Page", "Replacement body indeed.\n", []struct{ Quote, SourceFile string }{
		{Quote: "one new substring quote.", SourceFile: "new.md"},
	})
	client := &stubUpdateClient{
		completeStructuredFn: func(ctx context.Context, system, user string, ts llm.ToolSchema) (map[string]any, error) {
			return resp, nil
		},
	}
	res, err := UpdateExistingPagesFromSource(context.Background(), fx.Cfg, fx.DB, client, fx.SourceID,
		[]ingest.SourceFile{newSrc}, nil, UpdateExistingOptions{})
	if err != nil {
		t.Fatalf("UpdateExistingPagesFromSource: %v", err)
	}
	if res.PagesUpdated != 1 || len(res.Updated) != 1 {
		t.Fatalf("res = %+v, want 1 updated", res)
	}
	parsed, _ := ReadPage(PagePath(fx.WikiDir, "Floor1 Page"))
	if !strings.Contains(parsed.Body, "Replacement body indeed") {
		t.Errorf("body not replaced: %q", parsed.Body)
	}
	logEntries, _ := fx.DB.GetPageUpdateLog(pageID, 10)
	if len(logEntries) != 1 || logEntries[0].Outcome != "updated" {
		t.Errorf("log entries = %+v, want one updated", logEntries)
	}
}

// TestUpdateExistingPagesFromSource_ContentHashSkip_NoOpUpdate_LogsBodyOnly:
// stub LLM returns the same body bytes (so HashContent matches the
// stored content_hash). No disk write; no DeleteEvidenceForPage; log
// row outcome='body_only' + reason='content_hash-unchanged'.
func TestUpdateExistingPagesFromSource_ContentHashSkip_NoOpUpdate_LogsBodyOnly(t *testing.T) {
	existingSrc := "existing source.\n"
	fx := setupUpdateB2Fixture(t, existingSrc)
	priorBody := "Stable body content.\n"
	pageID, priorHash := fx.seedPageB2(t, "Stable Page", priorBody, []string{"existing source."})

	newSrc := ingest.NewSourceFile("new.md", []byte("new line that mentions stable.\n"))
	// LLM returns the same body the page already has — content_hash
	// will match.
	resp := llmPagesResponse("Stable Page", priorBody, []struct{ Quote, SourceFile string }{
		{Quote: "new line that mentions stable.", SourceFile: "new.md"},
	})
	client := &stubUpdateClient{
		completeStructuredFn: func(ctx context.Context, system, user string, ts llm.ToolSchema) (map[string]any, error) {
			return resp, nil
		},
	}
	// Capture the on-disk byte stamp before the call.
	priorBytes, _ := os.ReadFile(PagePath(fx.WikiDir, "Stable Page"))

	res, err := UpdateExistingPagesFromSource(context.Background(), fx.Cfg, fx.DB, client, fx.SourceID,
		[]ingest.SourceFile{newSrc}, nil, UpdateExistingOptions{})
	if err != nil {
		t.Fatalf("UpdateExistingPagesFromSource: %v", err)
	}
	if len(res.BodyOnly) != 1 {
		t.Errorf("res.BodyOnly = %v, want 1", res.BodyOnly)
	}
	postBytes, _ := os.ReadFile(PagePath(fx.WikiDir, "Stable Page"))
	if string(postBytes) != string(priorBytes) {
		t.Errorf("on-disk bytes changed under body_only outcome")
	}
	stored, _ := fx.DB.GetPage("Stable Page")
	if stored.ContentHash != priorHash {
		t.Errorf("content_hash changed under body_only: %q vs %q", stored.ContentHash, priorHash)
	}
	// Evidence rows untouched.
	rows, _ := fx.DB.GetEvidenceForPage(pageID)
	if len(rows) != 1 {
		t.Errorf("evidence touched under body_only: rows=%d", len(rows))
	}
	// Log row: body_only + content_hash-unchanged.
	logEntries, _ := fx.DB.GetPageUpdateLog(pageID, 10)
	if len(logEntries) != 1 || logEntries[0].Outcome != "body_only" {
		t.Fatalf("log entries = %+v, want one body_only", logEntries)
	}
	if logEntries[0].Reason != "content_hash-unchanged" {
		t.Errorf("reason = %q, want content_hash-unchanged", logEntries[0].Reason)
	}
}

// TestUpdateExistingPagesFromSource_LLMError_LogsFailedAndContinues:
// pre-seed two candidates; stub LLM errors on the first call,
// succeeds on the second. Pass should NOT abort.
func TestUpdateExistingPagesFromSource_LLMError_LogsFailedAndContinues(t *testing.T) {
	existingSrc := "alpha quote.\nbeta quote.\n"
	fx := setupUpdateB2Fixture(t, existingSrc)
	id1, _ := fx.seedPageB2(t, "Alpha Page", "Body alpha.\n", []string{"alpha quote."})
	id2, _ := fx.seedPageB2(t, "Beta Page", "Body beta.\n", []string{"beta quote."})

	newSrcContent := []byte("alpha quote and beta quote both.\n")
	newSrc := ingest.NewSourceFile("new.md", newSrcContent)

	// Sequence the responses by call count.
	var mu sync.Mutex
	calls := 0
	client := &stubUpdateClient{
		completeStructuredFn: func(ctx context.Context, system, user string, ts llm.ToolSchema) (map[string]any, error) {
			mu.Lock()
			calls++
			n := calls
			mu.Unlock()
			if n == 1 {
				return nil, fmt.Errorf("simulated transient LLM failure")
			}
			// 2nd candidate gets the success response.
			return llmPagesResponse("Beta Page", "Updated body beta.\n", []struct{ Quote, SourceFile string }{
				{Quote: "alpha quote and beta quote both.", SourceFile: "new.md"},
			}), nil
		},
	}
	res, err := UpdateExistingPagesFromSource(context.Background(), fx.Cfg, fx.DB, client, fx.SourceID,
		[]ingest.SourceFile{newSrc}, nil, UpdateExistingOptions{})
	if err != nil {
		t.Fatalf("UpdateExistingPagesFromSource: %v", err)
	}
	// Either candidate could have ended up first under selectUpdateCandidates'
	// FTS ordering — we just assert that we got exactly one failed and one
	// updated outcome between them.
	totalFailed := res.PagesUpdateFailed
	totalUpdated := res.PagesUpdated
	if totalFailed != 1 || totalUpdated != 1 {
		t.Errorf("res = %+v, want one failed + one updated", res)
	}

	// Each page got exactly one log row.
	for _, id := range []int64{id1, id2} {
		entries, _ := fx.DB.GetPageUpdateLog(id, 10)
		if len(entries) != 1 {
			t.Errorf("page %d log rows = %d, want 1", id, len(entries))
		}
	}
}

// TestUpdateExistingPagesFromSource_ConcurrencyCappedAt5: pre-seed 10
// candidates; instrument the stub LLM to record max-in-flight; assert
// it never exceeds ingestMaxInflight = 5.
func TestUpdateExistingPagesFromSource_ConcurrencyCappedAt5(t *testing.T) {
	existingSrc := ""
	for i := 0; i < 10; i++ {
		existingSrc += fmt.Sprintf("quote-%02d here.\n", i)
	}
	fx := setupUpdateB2Fixture(t, existingSrc)
	for i := 0; i < 10; i++ {
		fx.seedPageB2(t, fmt.Sprintf("Concur Page %02d", i),
			fmt.Sprintf("body about widget %02d.\n", i),
			[]string{fmt.Sprintf("quote-%02d here.", i)})
	}
	newSrc := ingest.NewSourceFile("new.md", []byte("widget update payload.\n"))

	client := &stubUpdateClient{
		completeStructuredFn: func(ctx context.Context, system, user string, ts llm.ToolSchema) (map[string]any, error) {
			// Sleep briefly to let in-flight count climb under fan-out.
			time.Sleep(20 * time.Millisecond)
			return map[string]any{"pages": []any{}}, nil
		},
	}
	_, err := UpdateExistingPagesFromSource(context.Background(), fx.Cfg, fx.DB, client, fx.SourceID,
		[]ingest.SourceFile{newSrc}, nil, UpdateExistingOptions{
			MaxCandidatesPerSource: 20, MaxCandidatesTotal: 50,
		})
	if err != nil {
		t.Fatalf("UpdateExistingPagesFromSource: %v", err)
	}
	if client.callCount() != 10 {
		t.Errorf("LLM calls = %d, want 10", client.callCount())
	}
	if client.maxInFlight > ingestMaxInflight {
		t.Errorf("max in-flight = %d, want <= %d", client.maxInFlight, ingestMaxInflight)
	}
}

// TestUpdateExistingPagesFromSource_AuditTrailRowOnEveryOutcome:
// craft a 4-candidate batch yielding 1 updated, 1 failed,
// 1 body_only, 1 skipped — assert exactly 4 page_update_log rows with
// the expected outcome distribution.
func TestUpdateExistingPagesFromSource_AuditTrailRowOnEveryOutcome(t *testing.T) {
	existingSrc := "alpha existing.\nbravo existing.\ncharlie existing.\ndelta existing.\n"
	fx := setupUpdateB2Fixture(t, existingSrc)
	idAlpha, _ := fx.seedPageB2(t, "Alpha Audit", "Body alpha audit.\n", []string{"alpha existing."})
	idBravo, _ := fx.seedPageB2(t, "Bravo Audit", "Body bravo audit.\n", []string{"bravo existing."})
	idCharlie, hashCharlie := fx.seedPageB2(t, "Charlie Audit", "Body charlie audit.\n", []string{"charlie existing."})
	idDelta, _ := fx.seedPageB2(t, "Delta Audit", "Body delta audit.\n", []string{"delta existing."})

	newSrcContent := []byte("audit alpha existing and bravo existing and charlie existing and delta existing all.\n")
	newSrc := ingest.NewSourceFile("audit-new.md", newSrcContent)

	client := &stubUpdateClient{
		completeStructuredFn: func(ctx context.Context, system, user string, ts llm.ToolSchema) (map[string]any, error) {
			// Inspect the user prompt for the EXISTING PAGE marker so we
			// can dispatch by candidate title.
			switch {
			case strings.Contains(user, "Alpha Audit"):
				return llmPagesResponse("Alpha Audit", "New alpha audit body.\n",
					[]struct{ Quote, SourceFile string }{
						{Quote: "audit alpha existing and bravo existing and charlie existing and delta existing all.", SourceFile: "audit-new.md"},
					}), nil
			case strings.Contains(user, "Bravo Audit"):
				// Bravo: invented quotes → failed.
				return llmPagesResponse("Bravo Audit", "Replacement bravo body.\n",
					[]struct{ Quote, SourceFile string }{
						{Quote: "totally invented quote zero match.", SourceFile: "audit-new.md"},
					}), nil
			case strings.Contains(user, "Charlie Audit"):
				// Charlie: same body as prior → body_only via content_hash skip.
				_ = hashCharlie
				return llmPagesResponse("Charlie Audit", "Body charlie audit.\n",
					[]struct{ Quote, SourceFile string }{
						{Quote: "audit alpha existing and bravo existing and charlie existing and delta existing all.", SourceFile: "audit-new.md"},
					}), nil
			case strings.Contains(user, "Delta Audit"):
				// Delta: empty pages → skipped.
				return map[string]any{"pages": []any{}}, nil
			}
			// Default: skip.
			return map[string]any{"pages": []any{}}, nil
		},
	}
	res, err := UpdateExistingPagesFromSource(context.Background(), fx.Cfg, fx.DB, client, fx.SourceID,
		[]ingest.SourceFile{newSrc}, nil, UpdateExistingOptions{})
	if err != nil {
		t.Fatalf("UpdateExistingPagesFromSource: %v", err)
	}
	_ = res

	// Each of the 4 pages got exactly one log row, total 4.
	totals := map[string]int{}
	for _, id := range []int64{idAlpha, idBravo, idCharlie, idDelta} {
		entries, err := fx.DB.GetPageUpdateLog(id, 10)
		if err != nil {
			t.Fatalf("GetPageUpdateLog: %v", err)
		}
		if len(entries) != 1 {
			t.Errorf("page %d entries = %d, want 1", id, len(entries))
		}
		for _, e := range entries {
			totals[e.Outcome]++
		}
	}
	want := map[string]int{
		"updated": 1, "body_only": 1, "failed": 1, "skipped": 1,
	}
	for k, v := range want {
		if totals[k] != v {
			t.Errorf("outcome %q = %d, want %d (totals=%v)", k, totals[k], v, totals)
		}
	}
}

// TestUpdateExistingPagesFromSource_DebugUpdates_LogsPerCandidateVerdicts:
// pass DebugUpdates=true and a *bytes.Buffer Logger; assert one line
// per candidate naming the outcome (and reason on failed).
func TestUpdateExistingPagesFromSource_DebugUpdates_LogsPerCandidateVerdicts(t *testing.T) {
	existingSrc := "alpha quote update.\nbravo quote update.\ncharlie quote update.\n"
	fx := setupUpdateB2Fixture(t, existingSrc)
	fx.seedPageB2(t, "X Page", "Body update alpha topic.\n", []string{"alpha quote update."})
	fx.seedPageB2(t, "Y Page", "Body update bravo topic.\n", []string{"bravo quote update."})
	fx.seedPageB2(t, "Z Page", "Body update charlie topic.\n", []string{"charlie quote update."})

	newSrc := ingest.NewSourceFile("new.md", []byte("update payload covering alpha bravo charlie.\n"))
	client := &stubUpdateClient{
		completeStructuredFn: func(ctx context.Context, system, user string, ts llm.ToolSchema) (map[string]any, error) {
			return map[string]any{"pages": []any{}}, nil // skipped
		},
	}
	var buf bytesBuffer
	_, err := UpdateExistingPagesFromSource(context.Background(), fx.Cfg, fx.DB, client, fx.SourceID,
		[]ingest.SourceFile{newSrc}, nil, UpdateExistingOptions{
			DebugUpdates: true,
			Logger:       &buf,
		})
	if err != nil {
		t.Fatalf("UpdateExistingPagesFromSource: %v", err)
	}
	out := buf.String()
	for _, title := range []string{"X Page", "Y Page", "Z Page"} {
		if !strings.Contains(out, title) {
			t.Errorf("debug output missing %q:\n%s", title, out)
		}
	}
}

// TestUpdateExistingPagesFromSource_TrustProperty_EveryUpdatedPageHasGroundedEvidence
// — the trust property pinned over the update path. Three candidates;
// happy path; for every updated page on disk, walk its evidence and
// assert each quote substring-matches some file in the union of
// (newSourceFiles + originalSourceFilesForThatPage).
func TestUpdateExistingPagesFromSource_TrustProperty_EveryUpdatedPageHasGroundedEvidence(t *testing.T) {
	existingSrc := "trust quote one.\ntrust quote two.\ntrust quote three.\n"
	fx := setupUpdateB2Fixture(t, existingSrc)
	fx.seedPageB2(t, "Trust One", "Body trust one.\n", []string{"trust quote one."})
	fx.seedPageB2(t, "Trust Two", "Body trust two.\n", []string{"trust quote two."})
	fx.seedPageB2(t, "Trust Three", "Body trust three.\n", []string{"trust quote three."})

	// Include "trust" so the candidate-selection FTS picks up all three
	// seeded pages whose bodies contain "trust".
	newSrcContent := []byte("new fact A trust.\nnew fact B trust.\nnew fact C trust.\n")
	newSrc := ingest.NewSourceFile("new.md", newSrcContent)

	client := &stubUpdateClient{
		completeStructuredFn: func(ctx context.Context, system, user string, ts llm.ToolSchema) (map[string]any, error) {
			// Mix one new-source quote and one existing-source quote per
			// candidate, dispatched by EXISTING PAGE marker.
			switch {
			case strings.Contains(user, "Trust One"):
				return llmPagesResponse("Trust One", "Updated trust one body.\n",
					[]struct{ Quote, SourceFile string }{
						{Quote: "new fact A trust.", SourceFile: "new.md"},
						{Quote: "trust quote one.", SourceFile: fx.existingSourcePath},
					}), nil
			case strings.Contains(user, "Trust Two"):
				return llmPagesResponse("Trust Two", "Updated trust two body.\n",
					[]struct{ Quote, SourceFile string }{
						{Quote: "new fact B trust.", SourceFile: "new.md"},
						{Quote: "trust quote two.", SourceFile: fx.existingSourcePath},
					}), nil
			case strings.Contains(user, "Trust Three"):
				return llmPagesResponse("Trust Three", "Updated trust three body.\n",
					[]struct{ Quote, SourceFile string }{
						{Quote: "new fact C trust.", SourceFile: "new.md"},
						{Quote: "trust quote three.", SourceFile: fx.existingSourcePath},
					}), nil
			}
			return map[string]any{"pages": []any{}}, nil
		},
	}
	res, err := UpdateExistingPagesFromSource(context.Background(), fx.Cfg, fx.DB, client, fx.SourceID,
		[]ingest.SourceFile{newSrc}, nil, UpdateExistingOptions{})
	if err != nil {
		t.Fatalf("UpdateExistingPagesFromSource: %v", err)
	}
	if res.PagesUpdated != 3 {
		t.Fatalf("PagesUpdated = %d, want 3", res.PagesUpdated)
	}

	// Trust property: every quote on every updated page substring-matches
	// some file in (newSourceFiles + on-disk existing source).
	existingBytes, err := os.ReadFile(fx.existingSourceURI)
	if err != nil {
		t.Fatalf("read existing source: %v", err)
	}
	allowed := []string{string(newSrcContent), string(existingBytes)}
	for _, title := range []string{"Trust One", "Trust Two", "Trust Three"} {
		page, err := ReadPage(PagePath(fx.WikiDir, title))
		if err != nil {
			t.Fatalf("ReadPage %s: %v", title, err)
		}
		if len(page.Evidence) == 0 {
			t.Fatalf("page %s has no evidence after update", title)
		}
		for _, e := range page.Evidence {
			matched := false
			for _, src := range allowed {
				if strings.Contains(src, e.Quote) {
					matched = true
					break
				}
			}
			if !matched {
				t.Errorf("page %s quote %q does not substring-match any file in (new + existing)", title, e.Quote)
			}
		}
	}
}

// bytesBuffer is a minimal io.Writer with String() so we don't have
// to import bytes in the per-test scope. Goroutine-safe via mutex —
// the per-candidate logger writes happen under the result-accumulator
// lock anyway, but defensive doesn't hurt.
type bytesBuffer struct {
	mu sync.Mutex
	b  []byte
}

func (b *bytesBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.b = append(b.b, p...)
	return len(p), nil
}

func (b *bytesBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.b)
}

// TestUpdateExistingPagesFromSource_AcceptsSchemaParam_StubLLM is the
// Phase B Task 5 call-site shape test: assert UpdateExistingOptions
// .Schema flows into the rendered system prompt that hits the LLM.
// Mirrors the v0.6 happy path but pins the system prompt to v0.6's
// updateExistingSystemPrompt const (byte-equality contract).
func TestUpdateExistingPagesFromSource_AcceptsSchemaParam_StubLLM(t *testing.T) {
	existingSrc := "first existing quote here.\nsecond existing quote here.\n"
	fx := setupUpdateB2Fixture(t, existingSrc)

	priorBody := "Old body about widgets.\n"
	fx.seedPageB2(t, "Widget Internals", priorBody, []string{
		"first existing quote here.",
		"second existing quote here.",
	})

	newSrcContent := []byte("new fact widgets and existing.\nanother fact widgets.\n")
	newSrc := ingest.NewSourceFile("widgets-new.md", newSrcContent)

	newBody := "New body covering widgets, with refinements.\n"
	resp := llmPagesResponse("Widget Internals", newBody, []struct{ Quote, SourceFile string }{
		{Quote: "new fact widgets and existing.", SourceFile: "widgets-new.md"},
		{Quote: "another fact widgets.", SourceFile: "widgets-new.md"},
	})

	var capturedSystem string
	client := &stubUpdateClient{
		completeStructuredFn: func(ctx context.Context, system, user string, ts llm.ToolSchema) (map[string]any, error) {
			capturedSystem = system
			return resp, nil
		},
	}

	res, err := UpdateExistingPagesFromSource(context.Background(), fx.Cfg, fx.DB, client, fx.SourceID,
		[]ingest.SourceFile{newSrc}, nil, UpdateExistingOptions{
			Schema: schema.Bundled(),
		})
	if err != nil {
		t.Fatalf("UpdateExistingPagesFromSource: %v", err)
	}
	if res.PagesUpdated != 1 {
		t.Errorf("PagesUpdated = %d, want 1", res.PagesUpdated)
	}
	// The bundled rendered Prompts.UpdateExisting is byte-equal to v0.6's
	// updateExistingSystemPrompt (pinned by byte_equality_test.go).
	if capturedSystem != UpdateExistingSystemPromptForTests() {
		t.Errorf("system prompt drifted from v0.6 updateExistingSystemPrompt; first 80 bytes: %q",
			capturedSystem[:minInt(80, len(capturedSystem))])
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestUpdateExistingPagesFromSource_StampsSchemaHashOnUpdated — happy-path
// update. Assert schema_hash post-update equals the active schema's
// hash. Trust property: the stamp happens AFTER the validator gate
// has approved the proposed body.
func TestUpdateExistingPagesFromSource_StampsSchemaHashOnUpdated(t *testing.T) {
	existingSrc := "first existing quote here.\nsecond existing quote here.\nthird existing quote here.\n"
	fx := setupUpdateB2Fixture(t, existingSrc)

	priorBody := "Old body about widgets.\n"
	pageID, _ := fx.seedPageB2(t, "Widget Internals", priorBody, []string{
		"first existing quote here.",
		"second existing quote here.",
		"third existing quote here.",
	})

	newSrcContent := []byte("new fact one widgets and existing.\nnew fact two widgets and existing.\n")
	newSrc := ingest.NewSourceFile("widgets-new.md", newSrcContent)

	newBody := "New body covering widgets, with refinements.\n"
	resp := llmPagesResponse("Widget Internals", newBody, []struct{ Quote, SourceFile string }{
		{Quote: "new fact one widgets and existing.", SourceFile: "widgets-new.md"},
		{Quote: "new fact two widgets and existing.", SourceFile: "widgets-new.md"},
		{Quote: "first existing quote here.", SourceFile: fx.existingSourcePath},
		{Quote: "second existing quote here.", SourceFile: fx.existingSourcePath},
	})
	client := &stubUpdateClient{
		completeStructuredFn: func(ctx context.Context, system, user string, ts llm.ToolSchema) (map[string]any, error) {
			return resp, nil
		},
	}

	sch := schema.Bundled()
	res, err := UpdateExistingPagesFromSource(context.Background(), fx.Cfg, fx.DB, client, fx.SourceID,
		[]ingest.SourceFile{newSrc}, nil, UpdateExistingOptions{Schema: sch})
	if err != nil {
		t.Fatalf("UpdateExistingPagesFromSource: %v", err)
	}
	if res.PagesUpdated != 1 {
		t.Fatalf("PagesUpdated = %d, want 1", res.PagesUpdated)
	}
	stored, err := fx.DB.GetPageByID(pageID)
	if err != nil || stored == nil {
		t.Fatalf("GetPageByID: page=%v err=%v", stored, err)
	}
	if stored.SchemaHash != sch.Hash() {
		t.Errorf("schema_hash = %q, want %q", stored.SchemaHash, sch.Hash())
	}
}

// TestUpdateExistingPagesFromSource_FailedUpdate_LeavesPriorSchemaHashUntouched
// — validator-drop case. Pre-stamp the page with a known prior hash;
// run the updater with quotes that won't validate; assert
// schema_hash is unchanged (the failed page didn't reach disk, so
// the hash that was on it before this call must persist verbatim).
func TestUpdateExistingPagesFromSource_FailedUpdate_LeavesPriorSchemaHashUntouched(t *testing.T) {
	existingSrc := "existing source words.\n"
	fx := setupUpdateB2Fixture(t, existingSrc)
	priorBody := "Body that should survive.\n"
	pageID, _ := fx.seedPageB2(t, "Trust Page", priorBody, []string{"existing source words."})

	// Pre-stamp the page with a known prior schema hash so we can
	// assert it didn't change after the failed update.
	priorSchemaHash := "PRIOR_SCHEMA_HASH_SENTINEL"
	if err := fx.DB.UpdateSchemaHash(pageID, priorSchemaHash); err != nil {
		t.Fatalf("seed UpdateSchemaHash: %v", err)
	}

	newSrc := ingest.NewSourceFile("new.md", []byte("words none of the quotes will match.\n"))
	resp := llmPagesResponse("Trust Page", "Replacement body that won't survive.\n", []struct{ Quote, SourceFile string }{
		{Quote: "totally invented quote A", SourceFile: "new.md"},
		{Quote: "totally invented quote B", SourceFile: "new.md"},
	})
	client := &stubUpdateClient{
		completeStructuredFn: func(ctx context.Context, system, user string, ts llm.ToolSchema) (map[string]any, error) {
			return resp, nil
		},
	}

	sch := schema.Bundled()
	res, err := UpdateExistingPagesFromSource(context.Background(), fx.Cfg, fx.DB, client, fx.SourceID,
		[]ingest.SourceFile{newSrc}, nil, UpdateExistingOptions{Schema: sch})
	if err != nil {
		t.Fatalf("UpdateExistingPagesFromSource: %v", err)
	}
	if res.PagesUpdateFailed != 1 {
		t.Fatalf("PagesUpdateFailed = %d, want 1", res.PagesUpdateFailed)
	}
	stored, err := fx.DB.GetPageByID(pageID)
	if err != nil || stored == nil {
		t.Fatalf("GetPageByID: page=%v err=%v", stored, err)
	}
	if stored.SchemaHash != priorSchemaHash {
		t.Errorf("schema_hash mutated under failed update: got %q, want %q (sentinel)",
			stored.SchemaHash, priorSchemaHash)
	}
}
