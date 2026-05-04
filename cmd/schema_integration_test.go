// Package cmd — schema_integration_test.go
//
// Cassette-driven end-to-end tests for sub-project 7's user-editable
// schema layer. Both tests exercise the full ingest / migrate paths
// through the real loadConfig wiring (so the cassette wraps the
// configured Gemini client just as it does for ingest), then
// re-walk every produced page on disk and verify the trust property:
// every evidence quote substring-matches its named source file.
//
// Recording is a manual operator step:
//
//	LLMWIKI_RECORD=1 GEMINI_API_KEY=... go test ./cmd/ -run TestSchemaRenameRoundtrip -v
//	LLMWIKI_RECORD=1 GEMINI_API_KEY=... go test ./cmd/ -run TestSchemaMigrate -v
//
// In replay-skip mode (no cassette JSON on disk) the tests Skip
// cleanly so CI stays green without the fixtures. Same pattern Phase D
// established for TestIngestGemini / TestIngestOpenAICompat and
// sub-project 6b's Phase G recording-deferred shape (see
// TestUpdateExistingHappyPath / TestUpdateExistingValidationDrop).
//
// The cassette JSON files are NOT checked in by Phase K; recording
// requires a real GEMINI_API_KEY which the maintainer runs out of
// band. Until then the skip path keeps `go test ./...` green.

package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mritunjaysharma394/llmwiki/internal/wiki"
)

// renameSchemaDoc is the AGENTS.md body the rename-roundtrip test
// drops at the wiki root. It renames the bundled `evidence` field to
// `citations` (Karpathy-gist-aligned), keeps every other ontology
// field at its bundled DeclaredName, and reuses the bundled prompt
// bodies verbatim so the LLM still produces the writePagesTool shape
// MigratePage's downstream IngestSourceFilesToPages parses. The
// rename surfaces on disk (frontmatter key) and bounces back through
// ParsePageWithSchema to populate Page.Evidence — the rename is
// invisible at the struct level, which is the load-bearing claim.
const renameSchemaDoc = `---
schema_version: 1
generator: llmwiki
---

# llmwiki schema (rename-roundtrip fixture)

## Domain

Test fixture for TestSchemaRenameRoundtrip.

## Page ontology

  - title         (string)         the page's primary key; unique per wiki
  - updated_at    (RFC3339 ts)     last-write timestamp; date-only ` + "`updated:`" + ` twin emitted alongside
  - content_hash  (sha256)         body hash; recomputed at every write
  - source_ids    (list of int)    DB row IDs backing this page
  - tags          (list of strings) Obsidian/Dataview-friendly
  - sources       (list of paths)  derived from evidence; emitted by WritePage
  - created       (date)           first-ingest date
  - links         (list)           Obsidian wikilinks declared structurally
  - citations     (list of quotes) verbatim spans from sources; required, >= 1
  - body          (markdown)       the page's narrative; lives below the closing ---

## Ingest prompt

You write wiki pages strictly grounded in the SOURCE provided.

The SOURCE may contain multiple files, each delimited by a header line:
    === path/to/file.ext ===
For every evidence quote, set "source_file" to the exact path shown in the
header above the file the quote was copied from. Quotes from different files
must each have their own evidence entry naming the correct file.

RULES:
1. Every page MUST include "evidence" — verbatim spans copied character-for-character from one of the files in SOURCE that justify the page's claims.
2. Each evidence entry SHOULD set "source_file" to the path from the "=== path ===" marker above its quote.
3. Do NOT include general knowledge that is not in SOURCE.
4. If SOURCE doesn't contain enough material for a high-quality page on a topic, do NOT create that page.
5. Better to return one solid page than five thin ones. Aim for 1-4 pages per call.
6. Page bodies should synthesize and organize, but every claim must be defensible from the evidence quotes you provide.
7. When linking pages, only reference existing pages or pages you are creating in this same call.{{domain}}

Existing wiki pages (titles only):
{{existing_titles}}

## Update-existing prompt

You update an EXISTING wiki page in light of a NEW SOURCE.{{domain}}{{existing_page_body}}{{existing_evidence}}

## Ask prompt

You answer using the provided wiki pages and source quotes.{{domain}}

## Contradiction prompt

You are a contradiction detector for two wiki pages, A and B.

## Promote rewrite prompt

You rewrite an LLM-generated answer into a polished wiki page body.{{question}}{{answer_body}}{{evidence_quotes}}

## Lint contradictions prompt

You are a wiki consistency checker.

## Glossary

  - citation: a verbatim quote attached to a page, the renamed evidence field
`

// migrateSchemaDoc is the AGENTS.md body the schema-migrate test
// drops at the wiki root in step 3. It is the bundled default with a
// cosmetically edited Domain section so the schema hash diverges from
// schema.Bundled().Hash() — which is what activates the migrate path.
// The ontology + prompt bodies stay bundled so the migrate LLM call's
// writePagesTool response shape is unchanged.
const migrateSchemaDoc = `---
schema_version: 1
generator: llmwiki
---

# llmwiki schema (TestSchemaMigrate fixture)

## Domain

This wiki collects notes for the TestSchemaMigrate fixture.

## Page ontology

  - title         (string)         the page's primary key; unique per wiki
  - updated_at    (RFC3339 ts)     last-write timestamp; date-only ` + "`updated:`" + ` twin emitted alongside
  - content_hash  (sha256)         body hash; recomputed at every write
  - source_ids    (list of int)    DB row IDs backing this page
  - tags          (list of strings) Obsidian/Dataview-friendly
  - sources       (list of paths)  derived from evidence; emitted by WritePage
  - created       (date)           first-ingest date
  - links         (list)           Obsidian wikilinks declared structurally
  - evidence      (list of quotes) verbatim spans from sources; required, >= 1
  - body          (markdown)       the page's narrative; lives below the closing ---

## Ingest prompt

You write wiki pages strictly grounded in the SOURCE provided.{{domain}}

Existing wiki pages (titles only):
{{existing_titles}}

## Update-existing prompt

You update an EXISTING wiki page in light of a NEW SOURCE.{{domain}}{{existing_page_body}}{{existing_evidence}}

## Ask prompt

You answer using the provided wiki pages and source quotes.{{domain}}

## Contradiction prompt

You are a contradiction detector for two wiki pages, A and B.

## Promote rewrite prompt

You rewrite an LLM-generated answer.{{question}}{{answer_body}}{{evidence_quotes}}

## Lint contradictions prompt

You are a wiki consistency checker.

## Glossary

  - migrate: re-ingest a page under a new schema hash
`

// TestSchemaRenameRoundtrip drives v0.7's ontology-rename surface
// end-to-end against a recorded Gemini Flash cassette. Pre-seeds an
// AGENTS.md renaming `evidence` -> `citations`; ingests one synthetic
// source; asserts:
//
//   - the produced page on disk carries the renamed `citations:`
//     frontmatter key (not `evidence:`);
//   - re-parsing via wiki.ParsePageWithSchema(activeSchema) repopulates
//     Page.Evidence (rename invisible at the struct level);
//   - the DB row's schema_hash equals sha256(AGENTS.md bytes);
//   - every evidence quote substring-matches the source file (the
//     trust property is bundled and pinned to the canonical struct
//     field — the validator sees source bytes either way).
//
// Recording target is Gemini Flash (cassette refresh stays free per
// spec risk #2). The cassette JSON is NOT checked in by Phase K;
// until the maintainer records it, the test skips cleanly via the
// cassette-on-disk gate.
func TestSchemaRenameRoundtrip(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cassette test in -short mode")
	}
	cassette := filepath.Join(realCassetteDir(t), "TestSchemaRenameRoundtrip__001.json")
	if _, err := os.Stat(cassette); os.IsNotExist(err) && os.Getenv("LLMWIKI_RECORD") == "" {
		t.Skip("cassette not recorded; run with LLMWIKI_RECORD=1 GEMINI_API_KEY=... go test ./cmd/ -run TestSchemaRenameRoundtrip to record")
	}
	if os.Getenv("LLMWIKI_RECORD") != "" && os.Getenv("GEMINI_API_KEY") == "" {
		t.Fatal("LLMWIKI_RECORD set but GEMINI_API_KEY missing")
	}
	t.Setenv("LLMWIKI_CASSETTE", "TestSchemaRenameRoundtrip")
	if os.Getenv("GEMINI_API_KEY") == "" {
		t.Setenv("GEMINI_API_KEY", "test-key-for-replay")
	}

	source := "Goroutines are lightweight threads of execution managed by the Go runtime.\n" +
		"The `go` keyword starts a goroutine.\n" +
		"Goroutines communicate via channels.\n"
	configBody := `[llm]
provider = "gemini"
model = "gemini-2.0-flash"

[wiki]
wiki_dir = ".llmwiki/wiki"
raw_dir = ".llmwiki/raw"
db_path = ".llmwiki/wiki.db"
`

	// runIngestThroughLoadConfig chdirs into a fresh tempdir, symlinks
	// the cassette dir into cwd, writes the config, and runs runIngest
	// the way cobra would. We drop AGENTS.md at the wiki-root before
	// ingest so loadConfig's schema.Load picks it up.
	chdirTemp(t)
	resetProviderFlags(t)
	linkCassettesIntoCwd(t, realCassetteDir(t))
	writeMinimalConfig(t, configBody)

	// The wiki-root directory is the cwd (loadConfig anchors to it for
	// schema.Load). Drop AGENTS.md at cwd before runIngest fires.
	if err := os.WriteFile("AGENTS.md", []byte(renameSchemaDoc), 0644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}
	wantHash := sha256.Sum256([]byte(renameSchemaDoc))
	wantHashHex := hex.EncodeToString(wantHash[:])

	srcPath := filepath.Join(t.TempDir(), "source.md")
	if err := os.WriteFile(srcPath, []byte(source), 0644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	if err := loadConfig(); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	t.Cleanup(func() {
		ingestCmd.Flags().Set("force", "false")
		ingestCmd.Flags().Set("max-file-bytes", "0")
		ingestCmd.Flags().Set("include", "")
		ingestCmd.Flags().Set("exclude", "")
		ingestCmd.Flags().Set("no-gitignore", "false")
	})
	if err := runIngest(ingestCmd, []string{srcPath}); err != nil {
		t.Fatalf("runIngest: %v", err)
	}

	// Walk the wiki dir; expect at least one page.
	wikiDir := cfg.Wiki.WikiDir
	entries, err := os.ReadDir(wikiDir)
	if err != nil {
		t.Fatalf("read wiki dir: %v", err)
	}
	var mdFiles []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			mdFiles = append(mdFiles, filepath.Join(wikiDir, e.Name()))
		}
	}
	if len(mdFiles) == 0 {
		t.Fatal("ingest produced no .md pages; cassette may be stale")
	}

	// Disk-level assertion: every page's frontmatter must carry
	// `citations:` (the renamed key) and not `evidence:`.
	for _, path := range mdFiles {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		body := string(raw)
		if !strings.Contains(body, "\ncitations:\n") {
			t.Errorf("page %s frontmatter missing renamed key `citations:`\n%s", path, body)
		}
		if strings.Contains(body, "\nevidence:\n") {
			t.Errorf("page %s frontmatter still has bundled key `evidence:` despite rename\n%s", path, body)
		}

		// Round-trip via the active schema: ParsePageWithSchema must
		// repopulate Page.Evidence even though the on-disk key is
		// `citations`. The struct-level field is canonical; the
		// declared-name rename is a presentation layer.
		page, err := wiki.ParsePageWithSchema(body, activeSchema)
		if err != nil {
			t.Fatalf("ParsePageWithSchema %s: %v", path, err)
		}
		if len(page.Evidence) == 0 {
			t.Errorf("page %s: ParsePageWithSchema returned 0 evidence entries despite citations: present", path)
		}

		// Trust property: every quote substring-matches the source.
		for _, e := range page.Evidence {
			if !strings.Contains(source, e.Quote) {
				t.Errorf("page %s: evidence quote %q does not substring-match source", path, e.Quote)
			}
		}

		// DB-row schema_hash equals sha256(AGENTS.md).
		rec, err := database.GetPage(page.Title)
		if err != nil || rec == nil {
			t.Fatalf("GetPage %q: %v (rec=%v)", page.Title, err, rec)
		}
		if rec.SchemaHash != wantHashHex {
			t.Errorf("page %q: schema_hash = %q, want sha256(AGENTS.md) = %q",
				page.Title, rec.SchemaHash, wantHashHex)
		}
	}
}

// TestSchemaMigrate drives the eager `llmwiki schema migrate --yes`
// flow end-to-end against a recorded Gemini Flash cassette.
//
// Pre-seeds 5 pages under bundled defaults (5 ingest LLM calls);
// cosmetically edits AGENTS.md to bump the active hash; runs
// runSchemaMigrate (5 more LLM calls — one per page); asserts:
//
//   - status counts: 5 pages on prior hash before migrate, 0 after;
//   - all 5 pages now carry the active schema hash;
//   - .llmwiki/log.md contains a `**schema_migrate**` entry;
//   - trust property: every evidence quote on every migrated page
//     substring-matches its source file.
//
// Recording target is Gemini Flash. The cassette JSON is not checked
// in by Phase K; until the maintainer records, the test skips.
func TestSchemaMigrate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cassette test in -short mode")
	}
	cassette := filepath.Join(realCassetteDir(t), "TestSchemaMigrate__001.json")
	if _, err := os.Stat(cassette); os.IsNotExist(err) && os.Getenv("LLMWIKI_RECORD") == "" {
		t.Skip("cassette not recorded; run with LLMWIKI_RECORD=1 GEMINI_API_KEY=... go test ./cmd/ -run TestSchemaMigrate to record")
	}
	if os.Getenv("LLMWIKI_RECORD") != "" && os.Getenv("GEMINI_API_KEY") == "" {
		t.Fatal("LLMWIKI_RECORD set but GEMINI_API_KEY missing")
	}
	t.Setenv("LLMWIKI_CASSETTE", "TestSchemaMigrate")
	if os.Getenv("GEMINI_API_KEY") == "" {
		t.Setenv("GEMINI_API_KEY", "test-key-for-replay")
	}
	resetMigrateFlags(t)

	configBody := `[llm]
provider = "gemini"
model = "gemini-2.0-flash"

[wiki]
wiki_dir = ".llmwiki/wiki"
raw_dir = ".llmwiki/raw"
db_path = ".llmwiki/wiki.db"
`

	// Phase 1: tmp wiki + provider=gemini (no AGENTS.md yet — pages
	// land at the bundled hash). chdirTemp + writeMinimalConfig +
	// linkCassettesIntoCwd are the same wiring runIngestThroughLoadConfig
	// uses; we inline them here to keep step ordering explicit (we need
	// AGENTS.md NOT present for the seed phase, then drop it before
	// the migrate phase).
	chdirTemp(t)
	resetProviderFlags(t)
	linkCassettesIntoCwd(t, realCassetteDir(t))
	writeMinimalConfig(t, configBody)

	if err := loadConfig(); err != nil {
		t.Fatalf("loadConfig (seed phase): %v", err)
	}
	bundledHash := activeSchema.Hash()

	// Phase 2: seed 5 pages by ingesting 5 small synthetic sources
	// under bundled defaults. Each ingest call is one cassette
	// segment; the cassette pins all 10 segments (5 seed + 5 migrate).
	type seed struct {
		filename string
		body     string
	}
	seeds := []seed{
		{"goroutine.md", "Goroutines are lightweight threads managed by the Go runtime.\nThe go keyword starts a goroutine.\n"},
		{"channel.md", "Channels in Go are typed conduits for communication between goroutines.\nUnbuffered channels rendezvous sender and receiver.\n"},
		{"mutex.md", "A Mutex protects shared state from concurrent access.\nsync.Mutex provides Lock and Unlock primitives.\n"},
		{"context.md", "Context propagates deadlines, cancellation, and request-scoped values.\nA cancelled parent cancels every child context.\n"},
		{"select.md", "The select statement waits on multiple channel operations.\nA default case makes select non-blocking.\n"},
	}
	t.Cleanup(func() {
		ingestCmd.Flags().Set("force", "false")
		ingestCmd.Flags().Set("max-file-bytes", "0")
		ingestCmd.Flags().Set("include", "")
		ingestCmd.Flags().Set("exclude", "")
		ingestCmd.Flags().Set("no-gitignore", "false")
	})
	for _, s := range seeds {
		path := filepath.Join(t.TempDir(), s.filename)
		if err := os.WriteFile(path, []byte(s.body), 0644); err != nil {
			t.Fatalf("write seed %s: %v", s.filename, err)
		}
		if err := runIngest(ingestCmd, []string{path}); err != nil {
			t.Fatalf("seed runIngest %s: %v", s.filename, err)
		}
	}

	// Step 3: write the custom AGENTS.md that bumps the schema hash.
	if err := os.WriteFile("AGENTS.md", []byte(migrateSchemaDoc), 0644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}
	wantHash := sha256.Sum256([]byte(migrateSchemaDoc))
	wantHashHex := hex.EncodeToString(wantHash[:])
	if wantHashHex == bundledHash {
		t.Fatalf("custom AGENTS.md hash %q matches bundled %q; cosmetic edit must change bytes",
			wantHashHex, bundledHash)
	}

	// Step 4: re-load schema so activeSchema picks up the on-disk
	// AGENTS.md (loadConfig caches it once at process start, but the
	// migrate path uses the package-level activeSchema). We reload
	// via loadConfig → schema.Load to mirror what would happen if a
	// fresh `llmwiki schema migrate` invocation booted right now.
	if err := loadConfig(); err != nil {
		t.Fatalf("loadConfig (migrate phase): %v", err)
	}
	if got := activeSchema.Hash(); got != wantHashHex {
		t.Fatalf("after AGENTS.md write: activeSchema.Hash() = %q, want %q", got, wantHashHex)
	}

	// Pre-migrate: status reports 5 pages on prior hash (the bundled
	// one), 0 on active. We sample the DB directly via the same
	// helper status uses.
	activeCount, priorCount, err := database.CountPagesByHashState(wantHashHex)
	if err != nil {
		t.Fatalf("CountPagesByHashState pre: %v", err)
	}
	if activeCount != 0 || priorCount != 5 {
		t.Errorf("pre-migrate counts: active=%d prior=%d, want active=0 prior=5", activeCount, priorCount)
	}
	_ = bundledHash // kept above as documentation of the prior hash

	// Step 5: run schema migrate --yes (5 LLM calls, one per page).
	if err := schemaMigrateCmd.Flags().Set("yes", "true"); err != nil {
		t.Fatalf("set --yes: %v", err)
	}
	if err := runSchemaMigrate(schemaMigrateCmd, nil); err != nil {
		t.Fatalf("runSchemaMigrate: %v", err)
	}

	// Step 6: post-migrate counts: 0 prior, 5 active.
	activeCount2, priorCount2, err := database.CountPagesByHashState(wantHashHex)
	if err != nil {
		t.Fatalf("CountPagesByHashState post: %v", err)
	}
	if activeCount2 != 5 || priorCount2 != 0 {
		t.Errorf("post-migrate counts: active=%d prior=%d, want active=5 prior=0", activeCount2, priorCount2)
	}

	// Step 7: log.md contains a **schema_migrate** chronicle line.
	logBytes, err := os.ReadFile(filepath.Join(".llmwiki", "log.md"))
	if err != nil {
		t.Fatalf("read log.md: %v", err)
	}
	if !strings.Contains(string(logBytes), "**schema_migrate**") {
		t.Errorf("log.md missing **schema_migrate** chronicle line:\n%s", logBytes)
	}

	// Step 8: trust property — every quote on every migrated page
	// substring-matches its source file. We re-walk the wiki dir and
	// build a corpus of source bodies from the seeds.
	sourceCorpus := ""
	for _, s := range seeds {
		sourceCorpus += s.body
	}
	wikiDir := cfg.Wiki.WikiDir
	wEntries, err := os.ReadDir(wikiDir)
	if err != nil {
		t.Fatalf("read wiki dir post-migrate: %v", err)
	}
	for _, e := range wEntries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(wikiDir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		page, err := wiki.ParsePageWithSchema(string(raw), activeSchema)
		if err != nil {
			t.Fatalf("ParsePageWithSchema %s: %v", e.Name(), err)
		}
		for _, ev := range page.Evidence {
			if !strings.Contains(sourceCorpus, ev.Quote) {
				t.Errorf("trust property violated: page %s quote %q does not substring-match any seed source",
					e.Name(), ev.Quote)
			}
		}
	}
}
