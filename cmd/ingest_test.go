package cmd

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/mritunjaysharma394/llmwiki/internal/db"
	"github.com/mritunjaysharma394/llmwiki/internal/ingest"
	"github.com/mritunjaysharma394/llmwiki/internal/wiki"
)

func TestSlugifyForArchive(t *testing.T) {
	tests := []struct{ in, want string }{
		{"What dependencies?", "what-dependencies"},
		{"Hello,  World!", "hello-world"},
		{"a/b\\c", "a-b-c"},
		{"   ", ""},
	}
	for _, tc := range tests {
		if got := slugify(tc.in); got != tc.want {
			t.Errorf("slugify(%q) = %q want %q", tc.in, got, tc.want)
		}
	}
}

func TestPartitionByFileHash(t *testing.T) {
	incoming := []ingest.SourceFile{
		ingest.NewSourceFile("unchanged.go", []byte("u\n")),
		ingest.NewSourceFile("changed.go", []byte("new\n")),
		ingest.NewSourceFile("new.go", []byte("n\n")),
	}
	existing := map[string]db.SourceFile{
		"unchanged.go": {RelativePath: "unchanged.go", ContentHash: incoming[0].ContentHash},
		"changed.go":   {RelativePath: "changed.go", ContentHash: "old"},
		"gone.go":      {RelativePath: "gone.go", ContentHash: "irrelevant"},
	}
	p := partitionByFileHash(incoming, existing)
	if len(p.unchanged) != 1 || p.unchanged[0].RelativePath != "unchanged.go" {
		t.Errorf("unchanged = %v", p.unchanged)
	}
	if len(p.changed) != 1 || p.changed[0].RelativePath != "changed.go" {
		t.Errorf("changed = %v", p.changed)
	}
	if len(p.newFiles) != 1 || p.newFiles[0].RelativePath != "new.go" {
		t.Errorf("new = %v", p.newFiles)
	}
	if len(p.gone) != 1 || p.gone[0].RelativePath != "gone.go" {
		t.Errorf("gone = %v", p.gone)
	}
}

func TestPartitionByFileHashEmptyExisting(t *testing.T) {
	incoming := []ingest.SourceFile{
		ingest.NewSourceFile("a.md", []byte("a")),
		ingest.NewSourceFile("b.md", []byte("b")),
	}
	p := partitionByFileHash(incoming, map[string]db.SourceFile{})
	if len(p.newFiles) != 2 {
		t.Errorf("expected 2 new files, got %d", len(p.newFiles))
	}
	if len(p.unchanged) != 0 || len(p.changed) != 0 || len(p.gone) != 0 {
		t.Errorf("expected only newFiles populated, got %+v", p)
	}
}

func TestComputeWholeHashOrderIndependent(t *testing.T) {
	a := []ingest.SourceFile{
		ingest.NewSourceFile("a.md", []byte("x")),
		ingest.NewSourceFile("b.md", []byte("y")),
	}
	b := []ingest.SourceFile{a[1], a[0]}
	if computeWholeHash(a) != computeWholeHash(b) {
		t.Error("computeWholeHash should be order-independent")
	}
}

func TestComputeWholeHashChangesWithContent(t *testing.T) {
	a := []ingest.SourceFile{
		ingest.NewSourceFile("a.md", []byte("x")),
		ingest.NewSourceFile("b.md", []byte("y")),
	}
	c := []ingest.SourceFile{
		ingest.NewSourceFile("a.md", []byte("x")),
		ingest.NewSourceFile("b.md", []byte("Y")),
	}
	if computeWholeHash(a) == computeWholeHash(c) {
		t.Error("computeWholeHash should change when any file content changes")
	}
}

func TestBuildIngestOptionsAppliesFlags(t *testing.T) {
	// Use a fresh cobra.Command so flag state from ingestCmd's package init
	// doesn't bleed across tests (cobra Flags() are per-command and parse-once
	// safe, but isolating mirrors how runIngest uses them).
	cmd := ingestCmd
	if err := cmd.ParseFlags([]string{
		"--max-file-bytes", "1024",
		"--exclude", "*.foo,*.bar",
		"--no-gitignore",
		"--include", ".md,.go",
		"--force",
	}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	t.Cleanup(func() {
		// Reset flags so other tests see defaults.
		cmd.Flags().Set("max-file-bytes", "0")
		cmd.Flags().Set("exclude", "")
		cmd.Flags().Set("include", "")
		cmd.Flags().Set("no-gitignore", "false")
		cmd.Flags().Set("force", "false")
	})

	walk, _ := buildIngestOptions(cmd, nil)
	if walk.MaxFileBytes != 1024 {
		t.Errorf("MaxFileBytes = %d", walk.MaxFileBytes)
	}
	if walk.RespectGitignore {
		t.Error("--no-gitignore should disable RespectGitignore")
	}
	if len(walk.ExtraSkipGlobs) != 2 || walk.ExtraSkipGlobs[0] != "*.foo" || walk.ExtraSkipGlobs[1] != "*.bar" {
		t.Errorf("ExtraSkipGlobs = %v", walk.ExtraSkipGlobs)
	}
	if len(walk.IncludeOnly) != 2 || walk.IncludeOnly[0] != ".md" || walk.IncludeOnly[1] != ".go" {
		t.Errorf("IncludeOnly = %v", walk.IncludeOnly)
	}
	if !forceFlag(cmd) {
		t.Error("--force not propagated")
	}
}

// TestBuildIngestOptionsAppliesConfigDefaults verifies that values from the
// [ingest] config block flow into WalkOptions / URLOptions when no flags
// override them.
func TestBuildIngestOptionsAppliesConfigDefaults(t *testing.T) {
	cmd := ingestCmd
	t.Cleanup(func() {
		cmd.Flags().Set("max-file-bytes", "0")
		cmd.Flags().Set("exclude", "")
		cmd.Flags().Set("include", "")
		cmd.Flags().Set("no-gitignore", "false")
		cmd.Flags().Set("force", "false")
	})
	// reset to clean state in case other tests left flags set
	cmd.Flags().Set("max-file-bytes", "0")
	cmd.Flags().Set("exclude", "")
	cmd.Flags().Set("include", "")
	cmd.Flags().Set("no-gitignore", "false")

	f := false
	c := &Config{
		Ingest: IngestConfig{
			MaxFileBytes:       2048,
			HTTPTimeoutSeconds: 7,
			HTTPMaxBytes:       1024 * 1024,
			ExtraSkipGlobs:     []string{"*.log"},
			RespectGitignore:   &f,
		},
	}
	walk, urlOpts := buildIngestOptions(cmd, c)
	if walk.MaxFileBytes != 2048 {
		t.Errorf("MaxFileBytes from config = %d, want 2048", walk.MaxFileBytes)
	}
	if walk.RespectGitignore {
		t.Error("RespectGitignore from config (false) not applied")
	}
	if len(walk.ExtraSkipGlobs) != 1 || walk.ExtraSkipGlobs[0] != "*.log" {
		t.Errorf("ExtraSkipGlobs from config = %v", walk.ExtraSkipGlobs)
	}
	if urlOpts.Timeout.Seconds() != 7 {
		t.Errorf("urlOpts.Timeout = %v, want 7s", urlOpts.Timeout)
	}
	if urlOpts.MaxBodyBytes != 1024*1024 {
		t.Errorf("urlOpts.MaxBodyBytes = %d", urlOpts.MaxBodyBytes)
	}
}

// TestBuildIngestOptionsFlagsOverrideConfig confirms that explicit CLI flags
// win over [ingest] config values, regardless of which the user set first.
func TestBuildIngestOptionsFlagsOverrideConfig(t *testing.T) {
	cmd := ingestCmd
	if err := cmd.ParseFlags([]string{"--max-file-bytes", "9999"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Flags().Set("max-file-bytes", "0")
	})
	c := &Config{Ingest: IngestConfig{MaxFileBytes: 1}}
	walk, _ := buildIngestOptions(cmd, c)
	if walk.MaxFileBytes != 9999 {
		t.Errorf("flag override = %d, want 9999", walk.MaxFileBytes)
	}
}

func TestSplitCSV(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", []string{}},
		{"a", []string{"a"}},
		{"a,b,c", []string{"a", "b", "c"}},
		{"  a , b ,, c ", []string{"a", "b", "c"}},
	}
	for _, tc := range cases {
		got := splitCSV(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("splitCSV(%q) = %v want %v", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("splitCSV(%q)[%d] = %q want %q", tc.in, i, got[i], tc.want[i])
			}
		}
	}
}

func TestIngestFlagNoRechunkRegistered(t *testing.T) {
	if ingestCmd.Flags().Lookup("no-rechunk") == nil {
		t.Fatal("--no-rechunk flag not registered")
	}
}

// realCassetteDir returns the absolute path to the canonical cassette dir
// (internal/llm/testdata/cassettes), resolved while the test process is still
// in its package working directory (cmd/). Callers chdir afterwards.
func realCassetteDir(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", "internal", "llm", "testdata", "cassettes"))
	if err != nil {
		t.Fatalf("resolving cassette dir: %v", err)
	}
	return abs
}

// linkCassettesIntoCwd makes the relative path "internal/llm/testdata/cassettes"
// (which loadConfig hardcodes when it constructs CassetteClient via the
// LLMWIKI_CASSETTE env var) resolve to the real cassette dir from inside a
// chdir'd tempdir. Lets runIngest go through loadConfig unmodified while still
// reading/writing the canonical fixture path.
func linkCassettesIntoCwd(t *testing.T, realDir string) {
	t.Helper()
	parent := filepath.Join("internal", "llm", "testdata")
	if err := os.MkdirAll(parent, 0755); err != nil {
		t.Fatalf("mkdir cassette parent: %v", err)
	}
	link := filepath.Join(parent, "cassettes")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatalf("symlink cassettes: %v", err)
	}
}

// runIngestThroughLoadConfig wires up the test to invoke runIngest the same
// way the CLI does: write a config.toml, set env, call loadConfig (which
// builds the LLM client + opens the db + wraps with CassetteClient), then run
// runIngest on a synthetic source file. Assertions live in the caller.
//
// The cassette layer wraps the parsed CompleteStructured output (a
// map[string]any), not raw HTTP — so a "cheap provider" cassette is byte-for-
// byte interchangeable with the Anthropic one. The point of these tests is to
// prove the new wiring (provider plumbing, env-var resolution, OpenAI-compat
// base_url config) doesn't break the validator contract that evidence quotes
// substring-match the source.
func runIngestThroughLoadConfig(t *testing.T, source string, configBody string) []wiki.Page {
	t.Helper()
	chdirTemp(t)
	resetProviderFlags(t)
	linkCassettesIntoCwd(t, realCassetteDir(t))
	writeMinimalConfig(t, configBody)

	srcPath := filepath.Join(t.TempDir(), "source.md")
	if err := os.WriteFile(srcPath, []byte(source), 0644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	if err := loadConfig(); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	// Snapshot --force so other tests aren't affected.
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

	entries, err := os.ReadDir(cfg.Wiki.WikiDir)
	if err != nil {
		t.Fatalf("read wiki dir: %v", err)
	}
	var pages []wiki.Page
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		p, err := wiki.ReadPage(filepath.Join(cfg.Wiki.WikiDir, e.Name()))
		if err != nil {
			t.Fatalf("read page %s: %v", e.Name(), err)
		}
		pages = append(pages, p)
	}
	return pages
}

// TestIngestGemini exercises the full ingest pipeline through the Gemini
// provider, using a recorded cassette for replay. The validator contract is
// provider-agnostic: every page's evidence quote must substring-match the
// source content, regardless of which model produced the structured output.
//
// The cassette is replayed by the CassetteClient that loadConfig wraps around
// the real GeminiClient when LLMWIKI_CASSETTE is set; the API key is a
// sentinel value the cassette layer ignores in replay mode.
func TestIngestGemini(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cassette test in -short mode")
	}
	cassette := filepath.Join(realCassetteDir(t), "TestIngestGemini__001.json")
	if _, err := os.Stat(cassette); os.IsNotExist(err) {
		t.Skip("cassette not recorded; run with LLMWIKI_RECORD=1 GEMINI_API_KEY=... to record")
	}
	t.Setenv("LLMWIKI_CASSETTE", "TestIngestGemini")
	t.Setenv("GEMINI_API_KEY", "test-key-for-replay")

	source := "Goroutines are lightweight threads of execution managed by the Go runtime.\nThe `go` keyword starts a goroutine.\nGoroutines communicate via channels.\n"
	configBody := `[llm]
provider = "gemini"
model = "gemini-2.0-flash"

[wiki]
wiki_dir = ".llmwiki/wiki"
raw_dir = ".llmwiki/raw"
db_path = ".llmwiki/wiki.db"
`
	pages := runIngestThroughLoadConfig(t, source, configBody)
	if len(pages) == 0 {
		t.Fatal("got 0 pages")
	}
	for _, p := range pages {
		if len(p.Evidence) == 0 {
			t.Errorf("page %q has no evidence", p.Title)
		}
		for _, e := range p.Evidence {
			if !strings.Contains(source, e.Quote) {
				t.Errorf("page %q evidence quote not in source: %q", p.Title, e.Quote)
			}
		}
	}
}

// TestIngestOpenAICompat exercises the full ingest pipeline through the
// OpenAI-compatible client (targeting OpenRouter's free tier as the canonical
// cheap-provider endpoint), using a recorded cassette for replay. Same
// validator contract as TestIngestGemini: every evidence quote must
// substring-match the source content.
//
// The plan originally called for hand-editing one recorded chunk to drop
// tool_calls and wrap JSON in prose, forcing the CompleteStructured
// JSON-extraction fallback. That can't be done at the cassette layer in
// practice — the cassette captures the *parsed* map[string]any output of
// CompleteStructured, after OpenAICompatClient has already either parsed
// tool_calls, fallen back to JSON-extraction, or errored out. The fallback
// path is exercised directly by the unit test
// TestOpenAICompatCompleteStructured_FallbackJSONExtraction in
// internal/llm/openai_compat_test.go (Phase A); this cassette test verifies
// the end-to-end happy path through the OpenAI-compat wiring.
func TestIngestOpenAICompat(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cassette test in -short mode")
	}
	cassette := filepath.Join(realCassetteDir(t), "TestIngestOpenAICompat__001.json")
	if _, err := os.Stat(cassette); os.IsNotExist(err) {
		t.Skip("cassette not recorded; run with LLMWIKI_RECORD=1 OPENROUTER_API_KEY=... to record")
	}
	t.Setenv("LLMWIKI_CASSETTE", "TestIngestOpenAICompat")
	t.Setenv("OPENROUTER_API_KEY", "test-key-for-replay")

	source := "Goroutines are lightweight threads of execution managed by the Go runtime.\nThe `go` keyword starts a goroutine.\nGoroutines communicate via channels.\n"
	configBody := `[llm]
provider = "openai-compatible"
model = "meta-llama-3.1-8b-instruct:free"

[wiki]
wiki_dir = ".llmwiki/wiki"
raw_dir = ".llmwiki/raw"
db_path = ".llmwiki/wiki.db"

[providers.openai_compat]
base_url = "https://openrouter.ai/api/v1"
api_key_env = "OPENROUTER_API_KEY"
`
	pages := runIngestThroughLoadConfig(t, source, configBody)
	if len(pages) == 0 {
		t.Fatal("got 0 pages")
	}
	for _, p := range pages {
		if len(p.Evidence) == 0 {
			t.Errorf("page %q has no evidence", p.Title)
		}
		for _, e := range p.Evidence {
			if !strings.Contains(source, e.Quote) {
				t.Errorf("page %q evidence quote not in source: %q", p.Title, e.Quote)
			}
		}
	}
}

// TestDistinctSourceFiles is a quick guard on the helper Phase F added to
// stamp Page.Sources before WritePage. Mirrors the wiki package's
// distinctEvidenceSources contract: distinct, first-occurrence-ordered,
// non-empty paths only.
func TestDistinctSourceFiles(t *testing.T) {
	cases := []struct {
		name string
		in   []wiki.Evidence
		want []string
	}{
		{"empty", nil, nil},
		{"all-empty-paths", []wiki.Evidence{{Quote: "x"}, {Quote: "y"}}, nil},
		{"distinct order", []wiki.Evidence{
			{Quote: "a", SourceFilePath: "x.go"},
			{Quote: "b", SourceFilePath: "y.go"},
			{Quote: "c", SourceFilePath: "x.go"},
		}, []string{"x.go", "y.go"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := distinctSourceFiles(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("len got %d want %d (%v)", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("[%d] got %q want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestIngest_GeneratesIndexAndLog asserts that after a successful ingest,
// cfg.Wiki.WikiDir/index.md lists every written page via [[Title]] and
// cfg.Wiki.WikiDir/log.md ends with an **ingest** chronicle line. Skips
// when no cassette is recorded — same gating as TestIngestGemini, since
// the integration path needs a deterministic LLM response.
func TestIngest_GeneratesIndexAndLog(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cassette test in -short mode")
	}
	cassette := filepath.Join(realCassetteDir(t), "TestIngestGemini__001.json")
	if _, err := os.Stat(cassette); os.IsNotExist(err) {
		t.Skip("cassette not recorded; reuses TestIngestGemini__001.json — record that first")
	}
	t.Setenv("LLMWIKI_CASSETTE", "TestIngestGemini")
	t.Setenv("GEMINI_API_KEY", "test-key-for-replay")

	source := "Goroutines are lightweight threads of execution managed by the Go runtime.\nThe `go` keyword starts a goroutine.\nGoroutines communicate via channels.\n"
	configBody := `[llm]
provider = "gemini"
model = "gemini-2.0-flash"

[wiki]
wiki_dir = ".llmwiki/wiki"
raw_dir = ".llmwiki/raw"
db_path = ".llmwiki/wiki.db"
`
	pages := runIngestThroughLoadConfig(t, source, configBody)
	if len(pages) == 0 {
		t.Fatal("got 0 pages")
	}

	indexPath := filepath.Join(cfg.Wiki.WikiDir, "index.md")
	idxBody, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("reading index.md: %v", err)
	}
	idx := string(idxBody)
	if !strings.Contains(idx, "title: index") {
		t.Errorf("index.md missing frontmatter title:\n%s", idx)
	}
	if !strings.Contains(idx, "generator: llmwiki") {
		t.Errorf("index.md missing generator marker:\n%s", idx)
	}
	for _, p := range pages {
		// Skip the index file itself — it's discovered by the wiki dir read
		// in runIngestThroughLoadConfig and round-trips through ParsePage.
		if p.Title == "index" {
			continue
		}
		want := "[[" + p.Title + "]]"
		if !strings.Contains(idx, want) {
			t.Errorf("index.md missing wikilink %q for ingested page", want)
		}
	}

	logPath := filepath.Join(cfg.Wiki.WikiDir, "log.md")
	logBody, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading log.md: %v", err)
	}
	logStr := strings.TrimRight(string(logBody), "\n")
	lines := strings.Split(logStr, "\n")
	if len(lines) == 0 {
		t.Fatal("log.md has no lines")
	}
	last := lines[len(lines)-1]
	if !strings.Contains(last, "**ingest**") {
		t.Errorf("last log line not an ingest entry: %q", last)
	}
}

// resetUpdateExistingFlags clears the two sub-project 6b flags both at
// test entry and on Cleanup so flag state doesn't bleed across the
// cmd-package's shared ingestCmd singleton. cobra's pflag.Flag tracks
// Changed as a sticky bit (Set bumps it), so we reach in via Lookup to
// reset both the value AND the Changed bit.
func resetUpdateExistingFlags(t *testing.T) {
	t.Helper()
	clear := func() {
		for _, name := range []string{"update-existing", "debug-updates"} {
			f := ingestCmd.Flags().Lookup(name)
			if f == nil {
				continue
			}
			_ = f.Value.Set("false")
			f.Changed = false
		}
	}
	clear()
	t.Cleanup(clear)
}

// TestIngest_UpdateExistingFlagDefaultsOff: with no --update-existing
// flag and no [ingest] update_existing config key, the IngestOptions
// reaching wiki.IngestSource must have UpdateExisting == false (Q11).
func TestIngest_UpdateExistingFlagDefaultsOff(t *testing.T) {
	resetUpdateExistingFlags(t)
	opts := buildWikiIngestOptions(ingestCmd, nil)
	if opts.UpdateExisting {
		t.Errorf("UpdateExisting = true with no flag/config; want false")
	}
}

// TestIngest_UpdateExistingFlagOverridesConfigOff: when the config has
// update_existing = false but CLI passes --update-existing, the CLI flag
// wins (layered precedence).
func TestIngest_UpdateExistingFlagOverridesConfigOff(t *testing.T) {
	resetUpdateExistingFlags(t)
	if err := ingestCmd.ParseFlags([]string{"--update-existing"}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	f := false
	c := &Config{Ingest: IngestConfig{UpdateExisting: &f}}
	opts := buildWikiIngestOptions(ingestCmd, c)
	if !opts.UpdateExisting {
		t.Errorf("UpdateExisting = false; CLI --update-existing should win over config off")
	}
}

// TestIngest_UpdateExistingFlagOverridesConfigOn: when the config has
// update_existing = true and CLI passes --update-existing=false, the CLI
// flag wins. Tests the "explicit opt-out via CLI" path.
func TestIngest_UpdateExistingFlagOverridesConfigOn(t *testing.T) {
	resetUpdateExistingFlags(t)
	if err := ingestCmd.ParseFlags([]string{"--update-existing=false"}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	tr := true
	c := &Config{Ingest: IngestConfig{UpdateExisting: &tr}}
	opts := buildWikiIngestOptions(ingestCmd, c)
	if opts.UpdateExisting {
		t.Errorf("UpdateExisting = true; CLI --update-existing=false should win over config on")
	}
}

// TestIngest_UpdateExistingFlagFromConfig: no CLI flag; config has
// update_existing = true; the config value reaches IngestOptions.
func TestIngest_UpdateExistingFlagFromConfig(t *testing.T) {
	resetUpdateExistingFlags(t)
	tr := true
	c := &Config{Ingest: IngestConfig{UpdateExisting: &tr}}
	opts := buildWikiIngestOptions(ingestCmd, c)
	if !opts.UpdateExisting {
		t.Errorf("UpdateExisting = false; config update_existing=true should propagate")
	}
}

// TestIngest_DebugUpdatesFlag: --debug-updates must propagate into
// IngestOptions.DebugUpdates.
func TestIngest_DebugUpdatesFlag(t *testing.T) {
	resetUpdateExistingFlags(t)
	if err := ingestCmd.ParseFlags([]string{"--debug-updates"}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	opts := buildWikiIngestOptions(ingestCmd, nil)
	if !opts.DebugUpdates {
		t.Errorf("DebugUpdates = false; --debug-updates should set it true")
	}
}

// TestIngest_TunablesPropagateFromConfig: the three integer tunables in
// [ingest] (max_candidates_per_source, max_candidates_total, quote_floor)
// reach IngestOptions unchanged when the config sets non-default values.
func TestIngest_TunablesPropagateFromConfig(t *testing.T) {
	resetUpdateExistingFlags(t)
	c := &Config{
		Ingest: IngestConfig{
			UpdateExistingMaxCandidatesPerSource: 7,
			UpdateExistingMaxCandidatesTotal:     11,
			UpdateExistingQuoteFloor:             5,
		},
	}
	opts := buildWikiIngestOptions(ingestCmd, c)
	if opts.UpdateExistingMaxCandidatesPerSource != 7 {
		t.Errorf("MaxCandidatesPerSource = %d, want 7",
			opts.UpdateExistingMaxCandidatesPerSource)
	}
	if opts.UpdateExistingMaxCandidatesTotal != 11 {
		t.Errorf("MaxCandidatesTotal = %d, want 11",
			opts.UpdateExistingMaxCandidatesTotal)
	}
	if opts.UpdateExistingQuoteFloor != 5 {
		t.Errorf("QuoteFloor = %d, want 5", opts.UpdateExistingQuoteFloor)
	}
}

// sanity assertion: paths in `gone` are returned in arbitrary order; tests
// shouldn't depend on map iteration order.
func TestPartitionGoneSortable(t *testing.T) {
	existing := map[string]db.SourceFile{
		"z": {RelativePath: "z", ContentHash: "z"},
		"a": {RelativePath: "a", ContentHash: "a"},
	}
	p := partitionByFileHash(nil, existing)
	paths := make([]string, len(p.gone))
	for i, g := range p.gone {
		paths[i] = g.RelativePath
	}
	sort.Strings(paths)
	if len(paths) != 2 || paths[0] != "a" || paths[1] != "z" {
		t.Errorf("got %v", paths)
	}
}
