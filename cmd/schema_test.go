package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mritunjaysharma394/llmwiki/internal/cliutil"
	"github.com/mritunjaysharma394/llmwiki/internal/db"
	"github.com/mritunjaysharma394/llmwiki/internal/llm"
	"github.com/mritunjaysharma394/llmwiki/internal/schema"
	"github.com/mritunjaysharma394/llmwiki/internal/wiki"
)

// captureSchemaStdout redirects os.Stdout for the duration of fn and
// returns whatever fn wrote. The schema commands print directly to
// os.Stdout (not cmd.OutOrStdout), so we have to swap the FD-level
// stream rather than route through cobra's writer plumbing.
func captureSchemaStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	prev := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = prev }()
	runErr := fn()
	w.Close()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("copy: %v", err)
	}
	return buf.String(), runErr
}

// resetSchemaShowFlags zeroes the show subcommand's bool flags between
// tests — cobra retains flag state across SetArgs calls in a single
// process, so a previous test's --bundled would leak otherwise.
func resetSchemaShowFlags(t *testing.T) {
	t.Helper()
	for _, name := range []string{"bundled", "doc", "hash"} {
		if err := schemaShowCmd.Flags().Set(name, "false"); err != nil {
			t.Fatalf("resetting --%s: %v", name, err)
		}
	}
}

// loadActiveSchemaForTest populates the package-level activeSchema
// from the current working directory the same way loadSchemaSoft does
// at runtime. Tests that exercise the schema subcommands without
// going through cobra's PersistentPreRunE call this directly.
func loadActiveSchemaForTest(t *testing.T) {
	t.Helper()
	sch, err := schema.Load(".")
	if err != nil {
		t.Fatalf("schema.Load: %v", err)
	}
	activeSchema = sch
}

// TestSchemaShow_PrintsMergedEffective_ByDefault — fresh wiki, no
// AGENTS.md / CLAUDE.md; `schema show` must surface the bundled
// content with the "schema: bundled" header.
func TestSchemaShow_PrintsMergedEffective_ByDefault(t *testing.T) {
	chdirTemp(t)
	resetSchemaShowFlags(t)
	loadActiveSchemaForTest(t)
	out, err := captureSchemaStdout(t, func() error {
		return runSchemaShow(schemaShowCmd, nil)
	})
	if err != nil {
		t.Fatalf("runSchemaShow: %v", err)
	}
	if !strings.Contains(out, "schema: bundled (no AGENTS.md or CLAUDE.md)") {
		t.Errorf("output missing bundled header line:\n%s", out)
	}
	for _, want := range []string{"## Domain", "## Ingest prompt", "## Page ontology"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// TestSchemaShow_DocFlag_PrintsAGENTSMdVerbatim — the --doc flag must
// emit the on-disk file byte-for-byte (no leading header), so users
// can pipe it into a diff against the bundled or another wiki's copy.
func TestSchemaShow_DocFlag_PrintsAGENTSMdVerbatim(t *testing.T) {
	chdirTemp(t)
	resetSchemaShowFlags(t)
	if err := os.WriteFile("AGENTS.md", []byte(validSchemaDoc), 0644); err != nil {
		t.Fatalf("writing AGENTS.md: %v", err)
	}
	loadActiveSchemaForTest(t)
	if err := schemaShowCmd.Flags().Set("doc", "true"); err != nil {
		t.Fatalf("setting --doc: %v", err)
	}
	out, err := captureSchemaStdout(t, func() error {
		return runSchemaShow(schemaShowCmd, nil)
	})
	if err != nil {
		t.Fatalf("runSchemaShow: %v", err)
	}
	if out != validSchemaDoc {
		t.Errorf("--doc output != fixture bytes\ngot len=%d, want len=%d", len(out), len(validSchemaDoc))
	}
}

// TestSchemaShow_DocFlag_NoAGENTSMd_PrintsBundledNotice — the --doc
// flag with no on-disk file must print the "no AGENTS.md or CLAUDE.md
// present" notice, not the bundled body.
func TestSchemaShow_DocFlag_NoAGENTSMd_PrintsBundledNotice(t *testing.T) {
	chdirTemp(t)
	resetSchemaShowFlags(t)
	loadActiveSchemaForTest(t)
	if err := schemaShowCmd.Flags().Set("doc", "true"); err != nil {
		t.Fatalf("setting --doc: %v", err)
	}
	out, err := captureSchemaStdout(t, func() error {
		return runSchemaShow(schemaShowCmd, nil)
	})
	if err != nil {
		t.Fatalf("runSchemaShow: %v", err)
	}
	for _, want := range []string{
		"no AGENTS.md or CLAUDE.md present",
		"bundled defaults are in effect",
		"llmwiki init --rewrite-schema",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// TestSchemaShow_BundledFlag_IgnoresAGENTSMd — write a custom
// AGENTS.md with a non-default Domain; --bundled must print the
// embedded default, not the custom file.
func TestSchemaShow_BundledFlag_IgnoresAGENTSMd(t *testing.T) {
	chdirTemp(t)
	resetSchemaShowFlags(t)
	if err := os.WriteFile("AGENTS.md", []byte(validSchemaDoc), 0644); err != nil {
		t.Fatalf("writing AGENTS.md: %v", err)
	}
	loadActiveSchemaForTest(t)
	if err := schemaShowCmd.Flags().Set("bundled", "true"); err != nil {
		t.Fatalf("setting --bundled: %v", err)
	}
	out, err := captureSchemaStdout(t, func() error {
		return runSchemaShow(schemaShowCmd, nil)
	})
	if err != nil {
		t.Fatalf("runSchemaShow: %v", err)
	}
	// validSchemaDoc's Domain is the test-fixture text; the bundled
	// default's Domain is empty / boilerplate. The fixture's marker
	// string must NOT appear in --bundled output.
	if strings.Contains(out, "Test domain for activeSchema unit tests") {
		t.Errorf("--bundled emitted the user-edited Domain text:\n%s", out)
	}
	// Sanity: --bundled must include a recognizable bundled-default
	// marker (the H1 line is the same in both, so check the prompt).
	if !strings.Contains(out, "## Ingest prompt") {
		t.Errorf("--bundled output missing '## Ingest prompt':\n%s", out)
	}
}

// TestSchemaValidate_OK_ExitZero — fresh wiki (bundled defaults);
// `schema validate` must succeed with the structured success block.
func TestSchemaValidate_OK_ExitZero(t *testing.T) {
	chdirTemp(t)
	loadActiveSchemaForTest(t)
	out, err := captureSchemaStdout(t, func() error {
		return runSchemaValidate(schemaValidateCmd, nil)
	})
	if err != nil {
		t.Fatalf("runSchemaValidate: %v", err)
	}
	for _, want := range []string{
		"bundled (schema_version 1)",
		"✓ all 6 required prompts present",
		"✓ all required placeholders present",
		"✓ page ontology has required fields: title, body, evidence",
		"✓ glossary has",
		"trust property: enforced by bundled validator",
		"substring-match against source files; not configurable from this doc",
		"OK",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("validate success block missing %q:\n%s", want, out)
		}
	}
}

// TestSchemaValidate_MissingRequiredSection_ExitOne_FileLineError —
// fixture AGENTS.md missing `## Ingest prompt`; `schema validate`
// must return a UserError whose rendered text mentions the missing
// section. The Parse step (called by schema.Load in the test setup)
// is what surfaces "required section missing", since the parser
// rejects a doc lacking required H2s before Validate runs.
func TestSchemaValidate_MissingRequiredSection_ExitOne_FileLineError(t *testing.T) {
	chdirTemp(t)
	const start = "## Ingest prompt"
	const end = "## Update-existing prompt"
	si := strings.Index(validSchemaDoc, start)
	ei := strings.Index(validSchemaDoc, end)
	if si < 0 || ei < 0 || ei <= si {
		t.Fatalf("fixture sanity: cannot locate Ingest/Update sections in validSchemaDoc")
	}
	malformed := validSchemaDoc[:si] + validSchemaDoc[ei:]
	if err := os.WriteFile("AGENTS.md", []byte(malformed), 0644); err != nil {
		t.Fatalf("writing malformed AGENTS.md: %v", err)
	}
	// schema.Load(".") returns a Parse error for a missing required
	// section — that's the "structurally malformed" branch
	// loadSchemaSoft surfaces. We mirror that branch here so the
	// test exercises the same code path the runtime would.
	_, loadErr := schema.Load(".")
	if loadErr == nil {
		t.Fatal("expected schema.Load to surface missing-section error; got nil")
	}
	rendered := cliutil.Render(cliutil.Wrap(
		"loading schema doc (AGENTS.md or CLAUDE.md)", loadErr,
		"the file is structurally malformed (frontmatter / section split). Fix the listed problem and re-run."))
	if !strings.Contains(rendered, "Ingest prompt") {
		t.Errorf("rendered error does not mention missing 'Ingest prompt' section:\n%s", rendered)
	}
}

// TestSchemaValidate_MissingRequiredPlaceholder_ExitOne_FileLineError —
// fixture with `## Ingest prompt` text but missing `{{domain}}`;
// `schema validate` must surface a structured error pointing at the
// section. Validate (not Parse) is the layer that catches missing
// placeholders, so this exercises the runSchemaValidate path with a
// schema that parses cleanly.
func TestSchemaValidate_MissingRequiredPlaceholder_ExitOne_FileLineError(t *testing.T) {
	chdirTemp(t)
	// Strip {{domain}} from the Ingest prompt body so Parse still
	// produces a non-empty body but Validate fires the missing-
	// placeholder error.
	mutated := strings.Replace(validSchemaDoc,
		"Test ingest prompt body. {{domain}} {{existing_titles}}",
		"Test ingest prompt body. {{existing_titles}}",
		1)
	if mutated == validSchemaDoc {
		t.Fatal("fixture mutation failed: the Ingest-prompt-without-domain replacement did not match")
	}
	if err := os.WriteFile("AGENTS.md", []byte(mutated), 0644); err != nil {
		t.Fatalf("writing AGENTS.md: %v", err)
	}
	loadActiveSchemaForTest(t)
	err := runSchemaValidate(schemaValidateCmd, nil)
	if err == nil {
		t.Fatal("runSchemaValidate succeeded; expected validation error")
	}
	rendered := cliutil.Render(err)
	for _, want := range []string{"Ingest prompt", "{{domain}}"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered error missing %q:\n%s", want, rendered)
		}
	}
}

// TestSchemaValidate_AllErrorsAtOnce — fixture with multiple problems;
// every error must surface in one run. The MultiError from
// internal/schema concatenates each ValidationError on its own line,
// so the rendered output should mention all three.
func TestSchemaValidate_AllErrorsAtOnce(t *testing.T) {
	chdirTemp(t)
	// Three problems at once:
	//   1. Missing {{domain}} in Ingest prompt.
	//   2. Missing {{existing_page_body}} in Update-existing prompt.
	//   3. Missing {{question}} in Promote rewrite prompt.
	// All three should surface from Validate's MultiError.
	mutated := validSchemaDoc
	mutated = strings.Replace(mutated,
		"Test ingest prompt body. {{domain}} {{existing_titles}}",
		"Test ingest prompt body. {{existing_titles}}",
		1)
	mutated = strings.Replace(mutated,
		"Test update prompt body. {{domain}} {{existing_page_body}} {{existing_evidence}}",
		"Test update prompt body. {{domain}} {{existing_evidence}}",
		1)
	mutated = strings.Replace(mutated,
		"Test promote rewrite prompt. {{question}} {{answer_body}} {{evidence_quotes}}",
		"Test promote rewrite prompt. {{answer_body}} {{evidence_quotes}}",
		1)
	if mutated == validSchemaDoc {
		t.Fatal("fixture mutation failed: no replacements applied")
	}
	if err := os.WriteFile("AGENTS.md", []byte(mutated), 0644); err != nil {
		t.Fatalf("writing AGENTS.md: %v", err)
	}
	loadActiveSchemaForTest(t)
	err := runSchemaValidate(schemaValidateCmd, nil)
	if err == nil {
		t.Fatal("runSchemaValidate succeeded; expected validation error")
	}
	rendered := cliutil.Render(err)
	for _, want := range []string{
		"Ingest prompt",
		"{{domain}}",
		"Update-existing prompt",
		"{{existing_page_body}}",
		"Promote rewrite prompt",
		"{{question}}",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered error missing %q (MultiError should surface all problems at once):\n%s", want, rendered)
		}
	}
}

// TestSchemaShow_HashFlag_PrintsActiveHashOnly — write fixture
// AGENTS.md; `schema show --hash` must emit exactly the active hex
// hash + newline (so users can scriptably compare across wikis
// sharing a schema, per spec Risk #3).
func TestSchemaShow_HashFlag_PrintsActiveHashOnly(t *testing.T) {
	chdirTemp(t)
	resetSchemaShowFlags(t)
	if err := os.WriteFile("AGENTS.md", []byte(validSchemaDoc), 0644); err != nil {
		t.Fatalf("writing AGENTS.md: %v", err)
	}
	loadActiveSchemaForTest(t)
	if err := schemaShowCmd.Flags().Set("hash", "true"); err != nil {
		t.Fatalf("setting --hash: %v", err)
	}
	out, err := captureSchemaStdout(t, func() error {
		return runSchemaShow(schemaShowCmd, nil)
	})
	if err != nil {
		t.Fatalf("runSchemaShow: %v", err)
	}
	want := fmt.Sprintf("%x\n", sha256.Sum256([]byte(validSchemaDoc)))
	if out != want {
		t.Errorf("--hash output = %q, want %q", out, want)
	}
}

// TestSchemaShow_HashFlag_NoAGENTSMd_PrintsBundledHash — no on-disk
// schema doc; `schema show --hash` must emit the bundled hash, not
// an empty string or sentinel.
func TestSchemaShow_HashFlag_NoAGENTSMd_PrintsBundledHash(t *testing.T) {
	chdirTemp(t)
	resetSchemaShowFlags(t)
	loadActiveSchemaForTest(t)
	if err := schemaShowCmd.Flags().Set("hash", "true"); err != nil {
		t.Fatalf("setting --hash: %v", err)
	}
	out, err := captureSchemaStdout(t, func() error {
		return runSchemaShow(schemaShowCmd, nil)
	})
	if err != nil {
		t.Fatalf("runSchemaShow: %v", err)
	}
	want := schema.Bundled().Hash() + "\n"
	if out != want {
		t.Errorf("--hash bundled output = %q, want %q", out, want)
	}
}

// ---------------------------------------------------------------------
// Phase F — `llmwiki schema migrate` tests
// ---------------------------------------------------------------------

// stubMigrateClient is a minimal llm.Client whose CompleteStructured is
// captured per-call. The default response is `{"pages": []}` (the LLM
// said "no change") which surfaces as a `failed` outcome in MigratePage
// because the title can't be matched. Tests that want a happy path
// install a per-call function.
type stubMigrateClient struct {
	mu                   sync.Mutex
	calls                int
	completeStructuredFn func(ctx context.Context, system, user string, ts llm.ToolSchema) (map[string]any, error)
}

func (s *stubMigrateClient) Complete(ctx context.Context, system, user string) (string, error) {
	return "", errors.New("stubMigrateClient.Complete unexpectedly called")
}
func (s *stubMigrateClient) CompleteStream(ctx context.Context, system, user string, w io.Writer) (string, error) {
	return "", errors.New("stubMigrateClient.CompleteStream unexpectedly called")
}
func (s *stubMigrateClient) CompleteStructured(ctx context.Context, system, user string, ts llm.ToolSchema) (map[string]any, error) {
	s.mu.Lock()
	s.calls++
	fn := s.completeStructuredFn
	s.mu.Unlock()
	if fn != nil {
		return fn(ctx, system, user, ts)
	}
	return map[string]any{"pages": []any{}}, nil
}
func (s *stubMigrateClient) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// migrateFixture wires a real wiki dir + DB + source file on disk so the
// migrate path's per-page re-ingest can read source bytes back. Pages
// are seeded with one evidence row each (so SourceIDs is populated).
type migrateFixture struct {
	Root      string
	WikiDir   string
	DB        *db.DB
	SourceID  int64
	FileID    int64
	SourceURI string
	SrcRel    string
}

// setupMigrateFixture creates a temp root with a wiki/ dir, a DB, and a
// single on-disk source file containing sourceContent. The seeded
// source row's URI points at the file path so readSourceFileContent
// (called from MigratePage's LoadSourceFilesForPage) can read it back.
//
// Also installs the cmd package-level globals (cfg, database, llmClient,
// activeSchema) the runSchemaMigrate path expects, restoring them via
// t.Cleanup.
func setupMigrateFixture(t *testing.T, sourceContent string) *migrateFixture {
	t.Helper()
	root := t.TempDir()
	wikiDir := filepath.Join(root, "wiki")
	if err := os.MkdirAll(wikiDir, 0755); err != nil {
		t.Fatalf("mkdir wikiDir: %v", err)
	}
	srcPath := filepath.Join(root, "src.md")
	if err := os.WriteFile(srcPath, []byte(sourceContent), 0644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	database, err := db.Open(filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	srcID, err := database.UpsertSource(srcPath, "h-src")
	if err != nil {
		t.Fatalf("UpsertSource: %v", err)
	}
	srcRel := filepath.Base(srcPath)
	fileID, err := database.UpsertSourceFile(db.SourceFile{
		SourceID:     srcID,
		RelativePath: srcRel,
		ContentHash:  "h-file",
		ByteSize:     int64(len(sourceContent)),
		LineCount:    1,
	})
	if err != nil {
		t.Fatalf("UpsertSourceFile: %v", err)
	}

	prevCfg, prevDB, prevClient, prevSchema := cfg, dbVar(), llmClient, activeSchema
	cfg = &Config{Wiki: WikiConfig{WikiDir: wikiDir}}
	setDBVar(database)
	loadActiveSchemaForTest(t)
	t.Cleanup(func() {
		cfg = prevCfg
		setDBVar(prevDB)
		llmClient = prevClient
		activeSchema = prevSchema
	})
	return &migrateFixture{
		Root:      root,
		WikiDir:   wikiDir,
		DB:        database,
		SourceID:  srcID,
		FileID:    fileID,
		SourceURI: srcPath,
		SrcRel:    srcRel,
	}
}

// dbVar / setDBVar isolate the package-level `database` variable for
// the snapshot/restore in setupMigrateFixture without exposing the
// global directly to every test that touches it.
func dbVar() *db.DB { return database }
func setDBVar(d *db.DB) { database = d }

// seedMigratePage materialises a page on disk + DB with a known
// schema_hash and one evidence row pointing at the fixture's source
// file. Returns the page ID.
func (fx *migrateFixture) seedMigratePage(t *testing.T, title, body, quote, schemaHash string) int64 {
	t.Helper()
	page := wiki.Page{
		Title:       title,
		Body:        body,
		ContentHash: wiki.HashContent(body),
		SourceIDs:   []int64{fx.SourceID},
	}
	if err := wiki.WritePage(page, fx.WikiDir); err != nil {
		t.Fatalf("WritePage %s: %v", title, err)
	}
	rec := db.PageRecord{
		Title:       title,
		Path:        wiki.PagePath(fx.WikiDir, title),
		Body:        body,
		ContentHash: page.ContentHash,
		SourceIDs:   []int64{fx.SourceID},
	}
	if err := fx.DB.UpsertPage(rec); err != nil {
		t.Fatalf("UpsertPage %s: %v", title, err)
	}
	stored, err := fx.DB.GetPage(title)
	if err != nil || stored == nil {
		t.Fatalf("re-fetch %s: %v", title, err)
	}
	if quote != "" {
		fileID := fx.FileID
		if err := fx.DB.InsertEvidence(stored.ID, fx.SourceID, []db.Evidence{{
			Quote:        quote,
			LineStart:    1,
			LineEnd:      1,
			SourceFileID: &fileID,
		}}); err != nil {
			t.Fatalf("InsertEvidence %s: %v", title, err)
		}
	}
	if schemaHash != "" {
		if err := fx.DB.UpdateSchemaHash(stored.ID, schemaHash); err != nil {
			t.Fatalf("UpdateSchemaHash %s: %v", title, err)
		}
	}
	return stored.ID
}

// migrateLLMResponse mints the writePagesTool shape MigratePage's
// downstream IngestSourceFilesToPages parses: a single page with
// title/body/evidence.
func migrateLLMResponse(title, body string, quotes []struct{ Quote, SourceFile string }) map[string]any {
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

// resetMigrateFlags zeroes the migrate subcommand's flags between tests.
func resetMigrateFlags(t *testing.T) {
	t.Helper()
	for _, name := range []string{"yes", "dry-run"} {
		if err := schemaMigrateCmd.Flags().Set(name, "false"); err != nil {
			t.Fatalf("resetting --%s: %v", name, err)
		}
	}
}

// withStdin replaces os.Stdin with a pipe whose write end carries the
// given bytes; restored on cleanup. Used by the without-`--yes` test.
func withStdin(t *testing.T, input string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if _, err := w.WriteString(input); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	w.Close()
	prev := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = prev })
}

// TestSchemaMigrate_NoDriftedPages_NoOp pre-seeds a wiki where every
// page is already at the active hash; assert "no pages on prior
// schema; nothing to do" and zero LLM calls.
func TestSchemaMigrate_NoDriftedPages_NoOp(t *testing.T) {
	chdirTemp(t)
	resetMigrateFlags(t)
	fx := setupMigrateFixture(t, "shared text content here.\n")
	activeHash := activeSchema.Hash()
	fx.seedMigratePage(t, "Already Current", "body.\n", "shared text", activeHash)

	stub := &stubMigrateClient{}
	llmClient = stub
	if err := schemaMigrateCmd.Flags().Set("yes", "true"); err != nil {
		t.Fatalf("set --yes: %v", err)
	}
	out, err := captureSchemaStdout(t, func() error {
		return runSchemaMigrate(schemaMigrateCmd, nil)
	})
	if err != nil {
		t.Fatalf("runSchemaMigrate: %v", err)
	}
	if !strings.Contains(out, "no pages on prior schema; nothing to do") {
		t.Errorf("expected no-op message, got:\n%s", out)
	}
	if stub.callCount() != 0 {
		t.Errorf("expected 0 LLM calls, got %d", stub.callCount())
	}
}

// TestSchemaMigrate_DryRunDoesNotWriteDisk seeds 5 pages at a prior
// hash, runs migrate --dry-run --yes, asserts every page's body bytes
// on disk are byte-identical and schema_hash is unchanged.
func TestSchemaMigrate_DryRunDoesNotWriteDisk(t *testing.T) {
	chdirTemp(t)
	resetMigrateFlags(t)
	srcContent := "alpha line.\nbravo line.\ncharlie line.\ndelta line.\necho line.\n"
	fx := setupMigrateFixture(t, srcContent)
	priorHash := "h-prior"

	type pageSpec struct{ title, body, quote string }
	specs := []pageSpec{
		{"Page A", "Body A.\n", "alpha line."},
		{"Page B", "Body B.\n", "bravo line."},
		{"Page C", "Body C.\n", "charlie line."},
		{"Page D", "Body D.\n", "delta line."},
		{"Page E", "Body E.\n", "echo line."},
	}
	priorBytes := map[string][]byte{}
	for _, s := range specs {
		fx.seedMigratePage(t, s.title, s.body, s.quote, priorHash)
		b, err := os.ReadFile(wiki.PagePath(fx.WikiDir, s.title))
		if err != nil {
			t.Fatalf("read prior bytes %s: %v", s.title, err)
		}
		priorBytes[s.title] = b
	}

	stub := &stubMigrateClient{
		completeStructuredFn: func(ctx context.Context, system, user string, ts llm.ToolSchema) (map[string]any, error) {
			// Pull the title hint from the user prompt to shape a
			// per-page response. Easiest: for each call return a
			// generic "Page X" body with a quote that substring-
			// matches the source.
			for _, s := range specs {
				if strings.Contains(system, s.title) {
					return migrateLLMResponse(s.title, "Migrated body for "+s.title+".\n", []struct{ Quote, SourceFile string }{
						{Quote: s.quote, SourceFile: fx.SrcRel},
					}), nil
				}
			}
			return map[string]any{"pages": []any{}}, nil
		},
	}
	llmClient = stub
	if err := schemaMigrateCmd.Flags().Set("yes", "true"); err != nil {
		t.Fatalf("set --yes: %v", err)
	}
	if err := schemaMigrateCmd.Flags().Set("dry-run", "true"); err != nil {
		t.Fatalf("set --dry-run: %v", err)
	}
	if _, err := captureSchemaStdout(t, func() error {
		return runSchemaMigrate(schemaMigrateCmd, nil)
	}); err != nil {
		t.Fatalf("runSchemaMigrate: %v", err)
	}

	if stub.callCount() != len(specs) {
		t.Errorf("expected %d LLM calls (one per drifted page), got %d", len(specs), stub.callCount())
	}
	for _, s := range specs {
		gotBytes, err := os.ReadFile(wiki.PagePath(fx.WikiDir, s.title))
		if err != nil {
			t.Fatalf("read post-dry-run %s: %v", s.title, err)
		}
		if !bytes.Equal(gotBytes, priorBytes[s.title]) {
			t.Errorf("dry-run mutated page %q on disk", s.title)
		}
		stored, _ := fx.DB.GetPage(s.title)
		if stored.SchemaHash != priorHash {
			t.Errorf("dry-run mutated schema_hash for %q: %q != %q", s.title, stored.SchemaHash, priorHash)
		}
	}
}

// TestSchemaMigrate_HappyPath_RemapsAllPagesToActiveHash seeds 3 pages
// at a prior hash; stub LLM returns valid updated bodies for each;
// asserts all 3 pages' schema_hash is now active and bodies were
// rewritten.
func TestSchemaMigrate_HappyPath_RemapsAllPagesToActiveHash(t *testing.T) {
	chdirTemp(t)
	resetMigrateFlags(t)
	srcContent := "first valid quote.\nsecond valid quote.\nthird valid quote.\n"
	fx := setupMigrateFixture(t, srcContent)
	activeHash := activeSchema.Hash()
	priorHash := "h-prior"

	type pageSpec struct{ title, body, quote string }
	specs := []pageSpec{
		{"Alpha", "Old Alpha.\n", "first valid quote."},
		{"Bravo", "Old Bravo.\n", "second valid quote."},
		{"Charlie", "Old Charlie.\n", "third valid quote."},
	}
	for _, s := range specs {
		fx.seedMigratePage(t, s.title, s.body, s.quote, priorHash)
	}

	stub := &stubMigrateClient{
		completeStructuredFn: func(ctx context.Context, system, user string, ts llm.ToolSchema) (map[string]any, error) {
			for _, s := range specs {
				if strings.Contains(system, s.title) {
					return migrateLLMResponse(s.title,
						"NEW body for "+s.title+" with refinements.\n",
						[]struct{ Quote, SourceFile string }{{Quote: s.quote, SourceFile: fx.SrcRel}}), nil
				}
			}
			return map[string]any{"pages": []any{}}, nil
		},
	}
	llmClient = stub
	if err := schemaMigrateCmd.Flags().Set("yes", "true"); err != nil {
		t.Fatalf("set --yes: %v", err)
	}
	out, err := captureSchemaStdout(t, func() error {
		return runSchemaMigrate(schemaMigrateCmd, nil)
	})
	if err != nil {
		t.Fatalf("runSchemaMigrate: %v", err)
	}
	if !strings.Contains(out, "3 page(s) brought to active schema.") {
		t.Errorf("summary missing migrated count:\n%s", out)
	}
	for _, s := range specs {
		stored, _ := fx.DB.GetPage(s.title)
		if stored.SchemaHash != activeHash {
			t.Errorf("page %q schema_hash = %q, want %q", s.title, stored.SchemaHash, activeHash)
		}
		parsed, err := wiki.ReadPage(wiki.PagePath(fx.WikiDir, s.title))
		if err != nil {
			t.Fatalf("ReadPage %s: %v", s.title, err)
		}
		if !strings.Contains(parsed.Body, "refinements") {
			t.Errorf("page %q body not rewritten:\n%s", s.title, parsed.Body)
		}
	}
}

// TestSchemaMigrate_ResumabilityViaPerPageHashCheck seeds 5 pages: 2 at
// the active hash already, 3 at a prior hash. Asserts only 3 LLM calls
// fire (the already-migrated pages are filtered out by ListPagesNotAtHash).
func TestSchemaMigrate_ResumabilityViaPerPageHashCheck(t *testing.T) {
	chdirTemp(t)
	resetMigrateFlags(t)
	srcContent := "first.\nsecond.\nthird.\nfourth.\nfifth.\n"
	fx := setupMigrateFixture(t, srcContent)
	activeHash := activeSchema.Hash()
	priorHash := "h-prior"

	// Pre-stamped at active hash → migrate skips them.
	fx.seedMigratePage(t, "Done 1", "Done 1 body.\n", "first.", activeHash)
	fx.seedMigratePage(t, "Done 2", "Done 2 body.\n", "second.", activeHash)
	// Drifted at prior hash → migrate touches these.
	fx.seedMigratePage(t, "Drift 1", "Drift 1 body.\n", "third.", priorHash)
	fx.seedMigratePage(t, "Drift 2", "Drift 2 body.\n", "fourth.", priorHash)
	fx.seedMigratePage(t, "Drift 3", "Drift 3 body.\n", "fifth.", priorHash)

	stub := &stubMigrateClient{
		completeStructuredFn: func(ctx context.Context, system, user string, ts llm.ToolSchema) (map[string]any, error) {
			for _, title := range []string{"Drift 1", "Drift 2", "Drift 3"} {
				if strings.Contains(system, title) {
					var quote string
					switch title {
					case "Drift 1":
						quote = "third."
					case "Drift 2":
						quote = "fourth."
					case "Drift 3":
						quote = "fifth."
					}
					return migrateLLMResponse(title, "Updated "+title+".\n",
						[]struct{ Quote, SourceFile string }{{Quote: quote, SourceFile: fx.SrcRel}}), nil
				}
			}
			return map[string]any{"pages": []any{}}, nil
		},
	}
	llmClient = stub
	if err := schemaMigrateCmd.Flags().Set("yes", "true"); err != nil {
		t.Fatalf("set --yes: %v", err)
	}
	if _, err := captureSchemaStdout(t, func() error {
		return runSchemaMigrate(schemaMigrateCmd, nil)
	}); err != nil {
		t.Fatalf("runSchemaMigrate: %v", err)
	}
	if stub.callCount() != 3 {
		t.Errorf("expected 3 LLM calls (skip the 2 pre-stamped), got %d", stub.callCount())
	}
}

// TestSchemaMigrate_ValidatorDropsPage_KeepsPriorVersion seeds 1 page
// at prior hash; stub LLM returns body with quotes that don't
// substring-match. Asserts page bytes byte-identical to prior,
// schema_hash unchanged, summary mentions "1 page(s) update FAILED".
func TestSchemaMigrate_ValidatorDropsPage_KeepsPriorVersion(t *testing.T) {
	chdirTemp(t)
	resetMigrateFlags(t)
	fx := setupMigrateFixture(t, "real source content here.\n")
	priorHash := "h-prior"
	fx.seedMigratePage(t, "Trust", "Original body.\n", "real source content here.", priorHash)
	priorBytes, _ := os.ReadFile(wiki.PagePath(fx.WikiDir, "Trust"))

	stub := &stubMigrateClient{
		completeStructuredFn: func(ctx context.Context, system, user string, ts llm.ToolSchema) (map[string]any, error) {
			return migrateLLMResponse("Trust", "Replacement body.\n", []struct{ Quote, SourceFile string }{
				{Quote: "totally invented quote A", SourceFile: fx.SrcRel},
				{Quote: "totally invented quote B", SourceFile: fx.SrcRel},
			}), nil
		},
	}
	llmClient = stub
	if err := schemaMigrateCmd.Flags().Set("yes", "true"); err != nil {
		t.Fatalf("set --yes: %v", err)
	}
	out, err := captureSchemaStdout(t, func() error {
		return runSchemaMigrate(schemaMigrateCmd, nil)
	})
	if err != nil {
		t.Fatalf("runSchemaMigrate: %v", err)
	}

	gotBytes, _ := os.ReadFile(wiki.PagePath(fx.WikiDir, "Trust"))
	if !bytes.Equal(gotBytes, priorBytes) {
		t.Errorf("validator-drop should preserve disk bytes; got mutation\nGOT:  %q\nWANT: %q", gotBytes, priorBytes)
	}
	stored, _ := fx.DB.GetPage("Trust")
	if stored.SchemaHash != priorHash {
		t.Errorf("schema_hash mutated under failed outcome: %q != %q", stored.SchemaHash, priorHash)
	}
	if !strings.Contains(out, "1 page(s) update FAILED") {
		t.Errorf("summary missing FAILED line:\n%s", out)
	}
}

// TestSchemaMigrate_PromptsForConfirmation_Without_Yes feeds a fake
// stdin returning "n"; asserts "aborted" message and zero LLM calls.
func TestSchemaMigrate_PromptsForConfirmation_Without_Yes(t *testing.T) {
	chdirTemp(t)
	resetMigrateFlags(t)
	fx := setupMigrateFixture(t, "content.\n")
	fx.seedMigratePage(t, "Drift", "body.\n", "content.", "h-prior")

	stub := &stubMigrateClient{}
	llmClient = stub
	withStdin(t, "n\n")

	out, err := captureSchemaStdout(t, func() error {
		return runSchemaMigrate(schemaMigrateCmd, nil)
	})
	if err != nil {
		t.Fatalf("runSchemaMigrate: %v", err)
	}
	if !strings.Contains(out, "aborted") {
		t.Errorf("expected 'aborted' message:\n%s", out)
	}
	if stub.callCount() != 0 {
		t.Errorf("expected 0 LLM calls after abort, got %d", stub.callCount())
	}
}

// TestSchemaMigrate_AppendsLogEntry: happy path, .llmwiki/log.md gains
// a `**schema_migrate**` entry naming the count + hash transition.
func TestSchemaMigrate_AppendsLogEntry(t *testing.T) {
	chdirTemp(t)
	resetMigrateFlags(t)
	fx := setupMigrateFixture(t, "valid quote text.\n")
	fx.seedMigratePage(t, "Logged", "old body.\n", "valid quote text.", "h-prior")

	stub := &stubMigrateClient{
		completeStructuredFn: func(ctx context.Context, system, user string, ts llm.ToolSchema) (map[string]any, error) {
			return migrateLLMResponse("Logged", "new body refinements.\n",
				[]struct{ Quote, SourceFile string }{{Quote: "valid quote text.", SourceFile: fx.SrcRel}}), nil
		},
	}
	llmClient = stub
	if err := schemaMigrateCmd.Flags().Set("yes", "true"); err != nil {
		t.Fatalf("set --yes: %v", err)
	}
	if _, err := captureSchemaStdout(t, func() error {
		return runSchemaMigrate(schemaMigrateCmd, nil)
	}); err != nil {
		t.Fatalf("runSchemaMigrate: %v", err)
	}

	logBytes, err := os.ReadFile(filepath.Join(fx.WikiDir, "log.md"))
	if err != nil {
		t.Fatalf("read log.md: %v", err)
	}
	logText := string(logBytes)
	if !strings.Contains(logText, "**schema_migrate**") {
		t.Errorf("log.md missing schema_migrate kind:\n%s", logText)
	}
	if !strings.Contains(logText, "1 migrated") {
		t.Errorf("log.md missing migrated count:\n%s", logText)
	}
}

// TestSchemaMigrate_TrustPropertyReaffirmed: happy path, every migrated
// page on disk has every evidence quote substring-matching some file in
// its source_files. The validator is the gatekeeper over the migrate
// path too — no quote reaches disk that wasn't substring-validated.
func TestSchemaMigrate_TrustPropertyReaffirmed(t *testing.T) {
	chdirTemp(t)
	resetMigrateFlags(t)
	srcContent := "alpha verified line.\nbravo verified line.\n"
	fx := setupMigrateFixture(t, srcContent)
	priorHash := "h-prior"
	fx.seedMigratePage(t, "Alpha", "Alpha old.\n", "alpha verified line.", priorHash)
	fx.seedMigratePage(t, "Bravo", "Bravo old.\n", "bravo verified line.", priorHash)

	stub := &stubMigrateClient{
		completeStructuredFn: func(ctx context.Context, system, user string, ts llm.ToolSchema) (map[string]any, error) {
			if strings.Contains(system, "Alpha") {
				return migrateLLMResponse("Alpha", "Alpha refined.\n",
					[]struct{ Quote, SourceFile string }{{Quote: "alpha verified line.", SourceFile: fx.SrcRel}}), nil
			}
			if strings.Contains(system, "Bravo") {
				return migrateLLMResponse("Bravo", "Bravo refined.\n",
					[]struct{ Quote, SourceFile string }{{Quote: "bravo verified line.", SourceFile: fx.SrcRel}}), nil
			}
			return map[string]any{"pages": []any{}}, nil
		},
	}
	llmClient = stub
	if err := schemaMigrateCmd.Flags().Set("yes", "true"); err != nil {
		t.Fatalf("set --yes: %v", err)
	}
	if _, err := captureSchemaStdout(t, func() error {
		return runSchemaMigrate(schemaMigrateCmd, nil)
	}); err != nil {
		t.Fatalf("runSchemaMigrate: %v", err)
	}

	for _, title := range []string{"Alpha", "Bravo"} {
		parsed, err := wiki.ReadPage(wiki.PagePath(fx.WikiDir, title))
		if err != nil {
			t.Fatalf("ReadPage %s: %v", title, err)
		}
		if len(parsed.Evidence) == 0 {
			t.Errorf("page %q migrated with zero evidence — trust property would have kept the prior version", title)
			continue
		}
		for _, e := range parsed.Evidence {
			if !strings.Contains(srcContent, e.Quote) {
				t.Errorf("page %q quote %q not in source_files (trust property violated)", title, e.Quote)
			}
		}
	}
}

// TestSchemaMigrate_PageWithNoSourceFiles_Skipped seeds a page with
// empty source_ids (mimicking a hand-written or promoted-answer page);
// asserts it's skipped without error and counted in the skipped tally.
func TestSchemaMigrate_PageWithNoSourceFiles_Skipped(t *testing.T) {
	chdirTemp(t)
	resetMigrateFlags(t)
	fx := setupMigrateFixture(t, "irrelevant.\n")

	// Hand-rolled page with empty source_ids — bypass seedMigratePage,
	// which always wires fx.SourceID in.
	page := wiki.Page{
		Title:       "HandWritten",
		Body:        "I am hand-authored.\n",
		ContentHash: wiki.HashContent("I am hand-authored.\n"),
		SourceIDs:   nil,
	}
	if err := wiki.WritePage(page, fx.WikiDir); err != nil {
		t.Fatalf("WritePage: %v", err)
	}
	rec := db.PageRecord{
		Title:       "HandWritten",
		Path:        wiki.PagePath(fx.WikiDir, "HandWritten"),
		Body:        page.Body,
		ContentHash: page.ContentHash,
		SourceIDs:   nil,
	}
	if err := fx.DB.UpsertPage(rec); err != nil {
		t.Fatalf("UpsertPage: %v", err)
	}
	stored, _ := fx.DB.GetPage("HandWritten")
	priorHash := "h-prior"
	if err := fx.DB.UpdateSchemaHash(stored.ID, priorHash); err != nil {
		t.Fatalf("UpdateSchemaHash: %v", err)
	}

	stub := &stubMigrateClient{}
	llmClient = stub
	if err := schemaMigrateCmd.Flags().Set("yes", "true"); err != nil {
		t.Fatalf("set --yes: %v", err)
	}
	out, err := captureSchemaStdout(t, func() error {
		return runSchemaMigrate(schemaMigrateCmd, nil)
	})
	if err != nil {
		t.Fatalf("runSchemaMigrate: %v", err)
	}
	if stub.callCount() != 0 {
		t.Errorf("expected 0 LLM calls (page skipped), got %d", stub.callCount())
	}
	if !strings.Contains(out, "1 page(s) skipped") {
		t.Errorf("summary missing skipped line:\n%s", out)
	}
	// schema_hash unchanged — we only stamp on migrated/unchanged.
	stored2, _ := fx.DB.GetPage("HandWritten")
	if stored2.SchemaHash != priorHash {
		t.Errorf("skipped page's schema_hash mutated: %q != %q", stored2.SchemaHash, priorHash)
	}
}
