// Package cmd — schema_integration_test.go
//
// Cassette-driven end-to-end tests for sub-project 7's user-editable
// schema layer. The tests exercise the full ingest / migrate paths
// through the real loadConfig wiring (so the cassette wraps the
// configured Gemini client just as it does for ingest), then
// re-walk every produced page on disk and verify the trust property:
// every evidence quote substring-matches its named source file.
//
// Recording is a manual operator step:
//
//	LLMWIKI_RECORD=1 GEMINI_API_KEY=... go test ./cmd/ -run TestSchemaRenameRoundtrip -v
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
// IngestSourceFilesToPages parses. The rename surfaces on disk
// (frontmatter key) and bounces back through ParsePageWithSchema to
// populate Page.Evidence — the rename is invisible at the struct
// level, which is the load-bearing claim.
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

	// Mirror runIngestThroughLoadConfig's wiring: chdir into a fresh
	// tempdir, reset provider flags, symlink the cassette dir into
	// cwd (so loadConfig's relative cassette lookup resolves), write
	// the config. We drop AGENTS.md at the wiki-root before ingest
	// so loadConfig's schema.Load picks it up.
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
