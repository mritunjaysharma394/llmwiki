package wiki

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mritunjaysharma394/llmwiki/internal/db"
)

// retroLinkFixture sets up a wikiDir + raw db with N seeded pages. Each
// seed is written via WritePage (so on-disk + DB stay in sync the same
// way ingest does it) and gets a single evidence row so the body-only
// invariant has something to assert against.
type retroLinkFixture struct {
	WikiDir string
	DB      *db.DB
	SrcID   int64
}

func setupRetroLinkFixture(t *testing.T) *retroLinkFixture {
	t.Helper()
	root := t.TempDir()
	wikiDir := filepath.Join(root, "wiki")
	if err := os.MkdirAll(wikiDir, 0755); err != nil {
		t.Fatalf("mkdir wiki: %v", err)
	}
	database, err := db.Open(filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	srcID, err := database.UpsertSource("test://src", "deadbeef")
	if err != nil {
		t.Fatalf("UpsertSource: %v", err)
	}
	return &retroLinkFixture{WikiDir: wikiDir, DB: database, SrcID: srcID}
}

// seedPage writes a Page to disk, upserts its DB row, and inserts one
// evidence quote so retro-link's "evidence rows untouched" invariant has
// something concrete to compare. Returns the page ID.
func (f *retroLinkFixture) seedPage(t *testing.T, title, body string) int64 {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	p := Page{
		Title:       title,
		Body:        body,
		ContentHash: HashContent(body),
		UpdatedAt:   now,
		SourceIDs:   []int64{f.SrcID},
		Evidence: []Evidence{
			{Quote: "verbatim quote for " + title, LineStart: 1, LineEnd: 1, SourceFilePath: "src.md"},
		},
	}
	if err := WritePage(p, f.WikiDir); err != nil {
		t.Fatalf("WritePage %s: %v", title, err)
	}
	rec := db.PageRecord{
		Title:       title,
		Path:        PagePath(f.WikiDir, title),
		Body:        body,
		ContentHash: p.ContentHash,
		SourceIDs:   []int64{f.SrcID},
	}
	if err := f.DB.UpsertPage(rec); err != nil {
		t.Fatalf("UpsertPage %s: %v", title, err)
	}
	stored, err := f.DB.GetPage(title)
	if err != nil || stored == nil {
		t.Fatalf("GetPage %s: %v", title, err)
	}
	if err := f.DB.InsertEvidence(stored.ID, f.SrcID, []db.Evidence{
		{Quote: p.Evidence[0].Quote, LineStart: 1, LineEnd: 1},
	}); err != nil {
		t.Fatalf("InsertEvidence %s: %v", title, err)
	}
	return stored.ID
}

func mustReadBody(t *testing.T, wikiDir, title string) string {
	t.Helper()
	page, err := ReadPage(PagePath(wikiDir, title))
	if err != nil {
		t.Fatalf("ReadPage %s: %v", title, err)
	}
	return page.Body
}

func TestRetroLinkPages_RewritesPagesMatchingNewTitles(t *testing.T) {
	fx := setupRetroLinkFixture(t)
	fx.seedPage(t, "Alpha", "Alpha discusses Mutex Implementation in passing.\n")
	fx.seedPage(t, "Beta", "Beta's review of Mutex Implementation is detailed.\n")
	fx.seedPage(t, "Gamma", "Gamma tangentially touches Mutex Implementation here.\n")

	res, err := RetroLinkPages(fx.DB, fx.WikiDir, []string{"Mutex Implementation"})
	if err != nil {
		t.Fatalf("RetroLinkPages: %v", err)
	}
	if len(res.UpdatedTitles) != 3 {
		t.Errorf("UpdatedTitles count: got %d want 3 (%v)", len(res.UpdatedTitles), res.UpdatedTitles)
	}
	for _, title := range []string{"Alpha", "Beta", "Gamma"} {
		body := mustReadBody(t, fx.WikiDir, title)
		if !strings.Contains(body, "[[Mutex Implementation]]") {
			t.Errorf("%s body missing wikilink:\n%s", title, body)
		}
	}
}

func TestRetroLinkPages_Idempotent(t *testing.T) {
	fx := setupRetroLinkFixture(t)
	fx.seedPage(t, "Alpha", "Alpha discusses Mutex Implementation in passing.\n")
	fx.seedPage(t, "Beta", "Beta's review of Mutex Implementation is detailed.\n")

	if _, err := RetroLinkPages(fx.DB, fx.WikiDir, []string{"Mutex Implementation"}); err != nil {
		t.Fatalf("first RetroLinkPages: %v", err)
	}

	// Snapshot disk state after first call.
	pre := map[string][]byte{}
	for _, title := range []string{"Alpha", "Beta"} {
		data, err := os.ReadFile(PagePath(fx.WikiDir, title))
		if err != nil {
			t.Fatalf("read %s: %v", title, err)
		}
		pre[title] = data
	}

	res, err := RetroLinkPages(fx.DB, fx.WikiDir, []string{"Mutex Implementation"})
	if err != nil {
		t.Fatalf("second RetroLinkPages: %v", err)
	}
	if len(res.UpdatedTitles) != 0 {
		t.Errorf("second call should be no-op; got UpdatedTitles=%v", res.UpdatedTitles)
	}
	for _, title := range []string{"Alpha", "Beta"} {
		data, err := os.ReadFile(PagePath(fx.WikiDir, title))
		if err != nil {
			t.Fatalf("re-read %s: %v", title, err)
		}
		if string(data) != string(pre[title]) {
			t.Errorf("%s changed on second call:\nbefore:\n%s\nafter:\n%s", title, pre[title], data)
		}
	}
}

func TestRetroLinkPages_SkipsPagesWhoseTitlesAreInNewSet(t *testing.T) {
	fx := setupRetroLinkFixture(t)
	// "Mutex Implementation" is itself a seeded page (mimicking the just-
	// written page); it must NOT be re-rewritten. Its body mentions another
	// new title to make the assertion non-trivial.
	fx.seedPage(t, "Mutex Implementation", "This page is about itself; no Mutex Implementation rewrite please.\n")
	fx.seedPage(t, "Alpha", "Alpha mentions Mutex Implementation here.\n")

	res, err := RetroLinkPages(fx.DB, fx.WikiDir, []string{"Mutex Implementation"})
	if err != nil {
		t.Fatalf("RetroLinkPages: %v", err)
	}
	for _, title := range res.UpdatedTitles {
		if title == "Mutex Implementation" {
			t.Errorf("page in newTitles should not be rewritten, got %v", res.UpdatedTitles)
		}
	}
	// Mutex Implementation body unchanged.
	body := mustReadBody(t, fx.WikiDir, "Mutex Implementation")
	if strings.Contains(body, "[[Mutex Implementation]]") {
		t.Errorf("Mutex Implementation page was self-rewritten: %s", body)
	}
	// Alpha was rewritten.
	alpha := mustReadBody(t, fx.WikiDir, "Alpha")
	if !strings.Contains(alpha, "[[Mutex Implementation]]") {
		t.Errorf("Alpha not rewritten: %s", alpha)
	}
}

func TestRetroLinkPages_BodyOnly_EvidenceUntouched(t *testing.T) {
	fx := setupRetroLinkFixture(t)
	pageID := fx.seedPage(t, "Alpha", "Alpha discusses Mutex Implementation here.\n")

	preEv, err := fx.DB.GetEvidenceForPage(pageID)
	if err != nil {
		t.Fatalf("pre evidence: %v", err)
	}

	if _, err := RetroLinkPages(fx.DB, fx.WikiDir, []string{"Mutex Implementation"}); err != nil {
		t.Fatalf("RetroLinkPages: %v", err)
	}

	postEv, err := fx.DB.GetEvidenceForPage(pageID)
	if err != nil {
		t.Fatalf("post evidence: %v", err)
	}
	if len(preEv) != len(postEv) {
		t.Fatalf("evidence row count changed: pre=%d post=%d", len(preEv), len(postEv))
	}
	for i := range preEv {
		if preEv[i].ID != postEv[i].ID || preEv[i].Quote != postEv[i].Quote ||
			preEv[i].LineStart != postEv[i].LineStart || preEv[i].LineEnd != postEv[i].LineEnd {
			t.Errorf("evidence row %d mutated:\npre:  %+v\npost: %+v", i, preEv[i], postEv[i])
		}
	}
}

func TestRetroLinkPages_RecomputesContentHashAndUpdatedAt(t *testing.T) {
	fx := setupRetroLinkFixture(t)
	pageID := fx.seedPage(t, "Alpha", "Alpha discusses Mutex Implementation here.\n")
	pre, err := fx.DB.GetPageByID(pageID)
	if err != nil || pre == nil {
		t.Fatalf("GetPageByID pre: %v", err)
	}

	// Sleep a touch so updated_at can move forward visibly even at second
	// granularity.
	time.Sleep(1100 * time.Millisecond)

	if _, err := RetroLinkPages(fx.DB, fx.WikiDir, []string{"Mutex Implementation"}); err != nil {
		t.Fatalf("RetroLinkPages: %v", err)
	}

	post, err := fx.DB.GetPageByID(pageID)
	if err != nil || post == nil {
		t.Fatalf("GetPageByID post: %v", err)
	}
	if pre.ContentHash == post.ContentHash {
		t.Errorf("content_hash unchanged: %s", post.ContentHash)
	}
	if !post.UpdatedAt.After(pre.UpdatedAt) {
		t.Errorf("updated_at not advanced: pre=%s post=%s", pre.UpdatedAt, post.UpdatedAt)
	}

	// Disk frontmatter content_hash should match the new DB value.
	disk, err := ReadPage(PagePath(fx.WikiDir, "Alpha"))
	if err != nil {
		t.Fatalf("ReadPage: %v", err)
	}
	if disk.ContentHash != post.ContentHash {
		t.Errorf("disk content_hash %q != db content_hash %q", disk.ContentHash, post.ContentHash)
	}
}

func TestRetroLinkPages_SkipsPagesWithNoMention(t *testing.T) {
	fx := setupRetroLinkFixture(t)
	fx.seedPage(t, "Alpha", "Alpha mentions Mutex Implementation here.\n")
	fx.seedPage(t, "Beta", "Beta is unrelated, no relevant title here.\n")

	res, err := RetroLinkPages(fx.DB, fx.WikiDir, []string{"Mutex Implementation"})
	if err != nil {
		t.Fatalf("RetroLinkPages: %v", err)
	}
	if len(res.UpdatedTitles) != 1 || res.UpdatedTitles[0] != "Alpha" {
		t.Errorf("expected only Alpha rewritten, got %v", res.UpdatedTitles)
	}
}

func TestRetroLinkPages_FTSPreFilterAtThreshold(t *testing.T) {
	// Lower the FTS pre-filter threshold so a small fixture exercises the
	// FTS-narrowed path. Restore on exit.
	prev := retroLinkFTSThreshold
	retroLinkFTSThreshold = 2
	t.Cleanup(func() { retroLinkFTSThreshold = prev })

	fx := setupRetroLinkFixture(t)
	// Five pages, of which only two mention "Mutex Implementation". With
	// the FTS pre-filter on, only those two become candidates and only
	// those two get rewritten. The other three pages must remain
	// byte-identical on disk.
	fx.seedPage(t, "Alpha", "Alpha mentions Mutex Implementation in prose.\n")
	fx.seedPage(t, "Beta", "Beta references Mutex Implementation explicitly.\n")
	fx.seedPage(t, "Gamma", "Gamma is unrelated chatter.\n")
	fx.seedPage(t, "Delta", "Delta is also unrelated.\n")
	fx.seedPage(t, "Epsilon", "Epsilon, completely tangential.\n")

	preGamma, _ := os.ReadFile(PagePath(fx.WikiDir, "Gamma"))
	preDelta, _ := os.ReadFile(PagePath(fx.WikiDir, "Delta"))
	preEpsilon, _ := os.ReadFile(PagePath(fx.WikiDir, "Epsilon"))

	res, err := RetroLinkPages(fx.DB, fx.WikiDir, []string{"Mutex Implementation"})
	if err != nil {
		t.Fatalf("RetroLinkPages: %v", err)
	}
	if len(res.UpdatedTitles) != 2 {
		t.Errorf("expected 2 updates from FTS-narrowed candidates, got %v", res.UpdatedTitles)
	}
	updated := map[string]bool{}
	for _, t := range res.UpdatedTitles {
		updated[t] = true
	}
	if !updated["Alpha"] || !updated["Beta"] {
		t.Errorf("expected Alpha+Beta updated, got %v", res.UpdatedTitles)
	}

	// Non-mentioning pages must be byte-identical on disk.
	postGamma, _ := os.ReadFile(PagePath(fx.WikiDir, "Gamma"))
	postDelta, _ := os.ReadFile(PagePath(fx.WikiDir, "Delta"))
	postEpsilon, _ := os.ReadFile(PagePath(fx.WikiDir, "Epsilon"))
	if string(preGamma) != string(postGamma) {
		t.Errorf("Gamma touched by FTS-narrowed pass")
	}
	if string(preDelta) != string(postDelta) {
		t.Errorf("Delta touched by FTS-narrowed pass")
	}
	if string(preEpsilon) != string(postEpsilon) {
		t.Errorf("Epsilon touched by FTS-narrowed pass")
	}
}

func TestRetroLinkPages_CodeFenceStillSkipped(t *testing.T) {
	fx := setupRetroLinkFixture(t)
	fenced := "Intro line.\n\n```go\nMutex Implementation := struct{}\n```\n\nNo other prose here.\n"
	fx.seedPage(t, "Alpha", fenced)

	res, err := RetroLinkPages(fx.DB, fx.WikiDir, []string{"Mutex Implementation"})
	if err != nil {
		t.Fatalf("RetroLinkPages: %v", err)
	}
	if len(res.UpdatedTitles) != 0 {
		t.Errorf("fenced-only mention should be no-op, got %v", res.UpdatedTitles)
	}
	body := mustReadBody(t, fx.WikiDir, "Alpha")
	if strings.Contains(body, "[[Mutex Implementation]]") {
		t.Errorf("fenced occurrence was rewritten:\n%s", body)
	}
}

func TestRetroLinkPages_EmptyNewTitles(t *testing.T) {
	fx := setupRetroLinkFixture(t)
	fx.seedPage(t, "Alpha", "Alpha mentions Mutex Implementation here.\n")

	res, err := RetroLinkPages(fx.DB, fx.WikiDir, nil)
	if err != nil {
		t.Fatalf("RetroLinkPages: %v", err)
	}
	if len(res.UpdatedTitles) != 0 {
		t.Errorf("empty newTitles should be no-op, got %v", res.UpdatedTitles)
	}
}
