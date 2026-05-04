package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mritunjaysharma394/llmwiki/internal/cliutil"
	"github.com/mritunjaysharma394/llmwiki/internal/db"
	"github.com/mritunjaysharma394/llmwiki/internal/wiki"
)

// promoteTestEnv mirrors the fixture pattern used by ingest tests but
// scoped to what `llmwiki promote` needs: a config.toml on disk, a
// sibling .llmwiki/answers/ dir for slug/filename resolution, and a
// pre-ingested synthetic source so PromoteAnswer can find a row in
// source_files and re-validate quotes.
type promoteTestEnv struct {
	root       string
	wikiDir    string
	answersDir string
	srcPath    string
}

func setupPromoteEnv(t *testing.T, sourceContent string) *promoteTestEnv {
	t.Helper()
	chdirTemp(t)
	resetProviderFlags(t)
	t.Setenv("ANTHROPIC_API_KEY", "test-key-not-used")
	writeMinimalConfig(t, `[llm]
provider = "anthropic"
model = "claude-haiku-4-5"

[wiki]
wiki_dir = ".llmwiki/wiki"
raw_dir = ".llmwiki/raw"
db_path = ".llmwiki/wiki.db"
`)
	root, _ := os.Getwd()
	wikiDir := filepath.Join(root, ".llmwiki", "wiki")
	answersDir := filepath.Join(root, ".llmwiki", "answers")
	for _, d := range []string{wikiDir, answersDir, filepath.Join(root, ".llmwiki", "raw")} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	srcPath := filepath.Join(root, "src.md")
	if err := os.WriteFile(srcPath, []byte(sourceContent), 0644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := loadConfig(); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	// Ingest the synthetic source through IngestSource so source_files
	// row exists with the expected relative path.
	if err := ingestSyntheticSource(srcPath); err != nil {
		t.Fatalf("ingest source: %v", err)
	}
	return &promoteTestEnv{
		root:       root,
		wikiDir:    wikiDir,
		answersDir: answersDir,
		srcPath:    srcPath,
	}
}

// ingestSyntheticSource records a `sources` row + `source_files` row
// for the given on-disk file without going through the LLM. It does
// only what's needed for PromoteAnswer's byPath lookup to find the
// file (and read its bytes off disk via ReadSourceFileContent).
func ingestSyntheticSource(srcPath string) error {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return err
	}
	hash := wiki.HashContent(string(data))
	sourceID, err := database.UpsertSource(srcPath, hash)
	if err != nil {
		return err
	}
	if _, err := database.UpsertSourceFile(db.SourceFile{
		SourceID:     sourceID,
		RelativePath: filepath.Base(srcPath),
		ContentHash:  hash,
		ByteSize:     int64(len(data)),
		LineCount:    countLines(data),
	}); err != nil {
		return err
	}
	return nil
}

// countLines mirrors ingest.NewSourceFile's line-count semantics so the
// promote tests don't have to import internal/ingest directly.
func countLines(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	n := 1
	for _, b := range data {
		if b == '\n' {
			n++
		}
	}
	if data[len(data)-1] == '\n' {
		n--
	}
	return n
}

// writeAnswerFileForPromote drops a saved-answer file in the answers
// dir and returns its absolute path.
func writeAnswerFileForPromote(t *testing.T, env *promoteTestEnv, slug string, in wiki.SavedAnswerInput) string {
	t.Helper()
	body := wiki.FormatSavedAnswer(in)
	ts := in.At.UTC().Format("2006-01-02-150405")
	name := fmt.Sprintf("%s-%s.md", ts, slug)
	path := filepath.Join(env.answersDir, name)
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("write answer: %v", err)
	}
	return path
}

// resetPromoteFlags restores promoteCmd's flags to defaults so the next
// test starts clean.
func resetPromoteFlags(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		promoteCmd.Flags().Set("title", "")
		promoteCmd.Flags().Set("rewrite", "false")
		promoteCmd.Flags().Set("no-save", "false")
	})
}

// TestPromote_ResolvesAbsolutePath asserts an absolute path argument is
// read directly and PromoteAnswer is invoked against it.
func TestPromote_ResolvesAbsolutePath(t *testing.T) {
	source := "alpha\nthe validator drops unverified quotes\ngamma\n"
	env := setupPromoteEnv(t, source)
	resetPromoteFlags(t)
	in := wiki.SavedAnswerInput{
		Question: "absolute path test",
		Answer:   "Yes.",
		Model:    "m",
		Pages: []wiki.Page{{
			Title: "T",
			Evidence: []wiki.Evidence{{
				Quote:          "the validator drops unverified quotes",
				LineStart:      2, LineEnd: 2,
				SourceFilePath: filepath.Base(env.srcPath),
			}},
		}},
		At: time.Date(2026, 5, 4, 15, 2, 8, 0, time.UTC),
	}
	answerPath := writeAnswerFileForPromote(t, env, "absolute-path-test", in)
	promoteCmd.Flags().Set("title", "Abs Path Page")
	if err := runPromote(promoteCmd, []string{answerPath}); err != nil {
		t.Fatalf("runPromote: %v", err)
	}
	if _, err := os.Stat(filepath.Join(env.wikiDir, "Abs Path Page.md")); err != nil {
		t.Errorf("page not created: %v", err)
	}
}

// TestPromote_ResolvesBaseFilename asserts a bare filename is resolved
// against <wiki-parent>/answers.
func TestPromote_ResolvesBaseFilename(t *testing.T) {
	source := "alpha\nthe validator drops unverified quotes\ngamma\n"
	env := setupPromoteEnv(t, source)
	resetPromoteFlags(t)
	in := wiki.SavedAnswerInput{
		Question: "bare filename test",
		Answer:   "Yes.",
		Model:    "m",
		Pages: []wiki.Page{{
			Title: "T",
			Evidence: []wiki.Evidence{{
				Quote:          "the validator drops unverified quotes",
				LineStart:      2, LineEnd: 2,
				SourceFilePath: filepath.Base(env.srcPath),
			}},
		}},
		At: time.Date(2026, 5, 4, 15, 2, 8, 0, time.UTC),
	}
	answerPath := writeAnswerFileForPromote(t, env, "bare-filename", in)
	promoteCmd.Flags().Set("title", "Bare Filename Page")
	if err := runPromote(promoteCmd, []string{filepath.Base(answerPath)}); err != nil {
		t.Fatalf("runPromote: %v", err)
	}
	if _, err := os.Stat(filepath.Join(env.wikiDir, "Bare Filename Page.md")); err != nil {
		t.Errorf("page not created: %v", err)
	}
}

// TestPromote_ResolvesSlug asserts a bare slug is matched against
// .llmwiki/answers/*-<slug>.md (latest wins on ambiguous match).
func TestPromote_ResolvesSlug(t *testing.T) {
	source := "alpha\nthe validator drops unverified quotes\ngamma\n"
	env := setupPromoteEnv(t, source)
	resetPromoteFlags(t)
	in := wiki.SavedAnswerInput{
		Question: "how does validator work",
		Answer:   "Yes.",
		Model:    "m",
		Pages: []wiki.Page{{
			Title: "T",
			Evidence: []wiki.Evidence{{
				Quote:          "the validator drops unverified quotes",
				LineStart:      2, LineEnd: 2,
				SourceFilePath: filepath.Base(env.srcPath),
			}},
		}},
		At: time.Date(2026, 5, 4, 15, 2, 8, 0, time.UTC),
	}
	_ = writeAnswerFileForPromote(t, env, "how-does-validator-work", in)
	promoteCmd.Flags().Set("title", "Slug Resolved Page")
	if err := runPromote(promoteCmd, []string{"how-does-validator-work"}); err != nil {
		t.Fatalf("runPromote: %v", err)
	}
	if _, err := os.Stat(filepath.Join(env.wikiDir, "Slug Resolved Page.md")); err != nil {
		t.Errorf("page not created: %v", err)
	}
}

// TestPromote_ResolvesSlug_AmbiguityErrors writes two answer files with
// the same slug; the resolver should error and name both candidates.
func TestPromote_ResolvesSlug_AmbiguityErrors(t *testing.T) {
	source := "alpha\nthe validator drops unverified quotes\ngamma\n"
	env := setupPromoteEnv(t, source)
	resetPromoteFlags(t)
	in := wiki.SavedAnswerInput{
		Question: "ambig",
		Answer:   "yes",
		Model:    "m",
		Pages: []wiki.Page{{Title: "T",
			Evidence: []wiki.Evidence{{
				Quote: "the validator drops unverified quotes", LineStart: 2, LineEnd: 2,
				SourceFilePath: filepath.Base(env.srcPath),
			}},
		}},
		At: time.Date(2026, 5, 4, 15, 2, 8, 0, time.UTC),
	}
	_ = writeAnswerFileForPromote(t, env, "ambig-slug", in)
	in.At = time.Date(2026, 5, 4, 16, 0, 0, 0, time.UTC)
	_ = writeAnswerFileForPromote(t, env, "ambig-slug", in)
	err := runPromote(promoteCmd, []string{"ambig-slug"})
	if err == nil {
		t.Fatal("expected ambiguity error")
	}
	if !strings.Contains(err.Error(), "ambig-slug") {
		t.Errorf("error should name the slug; got: %v", err)
	}
}

// TestPromote_TitleFlagOverrides asserts --title is plumbed into
// PromoteOptions.Title.
func TestPromote_TitleFlagOverrides(t *testing.T) {
	source := "alpha\nthe validator drops unverified quotes\ngamma\n"
	env := setupPromoteEnv(t, source)
	resetPromoteFlags(t)
	in := wiki.SavedAnswerInput{
		Question: "default title would be different",
		Answer:   "yes",
		Model:    "m",
		Pages: []wiki.Page{{Title: "T",
			Evidence: []wiki.Evidence{{
				Quote: "the validator drops unverified quotes", LineStart: 2, LineEnd: 2,
				SourceFilePath: filepath.Base(env.srcPath),
			}},
		}},
		At: time.Date(2026, 5, 4, 15, 2, 8, 0, time.UTC),
	}
	answerPath := writeAnswerFileForPromote(t, env, "default-title", in)
	promoteCmd.Flags().Set("title", "Custom Title From Flag")
	if err := runPromote(promoteCmd, []string{answerPath}); err != nil {
		t.Fatalf("runPromote: %v", err)
	}
	if _, err := os.Stat(filepath.Join(env.wikiDir, "Custom Title From Flag.md")); err != nil {
		t.Errorf("title flag not honored: %v", err)
	}
}

// TestPromote_RewriteFlagOff asserts --rewrite defaults false.
func TestPromote_RewriteFlagOff(t *testing.T) {
	if promoteCmd.Flags().Lookup("rewrite") == nil {
		t.Fatal("--rewrite flag not registered")
	}
	v, _ := promoteCmd.Flags().GetBool("rewrite")
	if v {
		t.Errorf("--rewrite default = true; want false")
	}
}

// TestPromote_NoSaveFlag asserts --no-save maps to NoSave=true.
func TestPromote_NoSaveFlag(t *testing.T) {
	source := "alpha\nthe validator drops unverified quotes\ngamma\n"
	env := setupPromoteEnv(t, source)
	resetPromoteFlags(t)
	in := wiki.SavedAnswerInput{
		Question: "no save",
		Answer:   "yes",
		Model:    "m",
		Pages: []wiki.Page{{Title: "T",
			Evidence: []wiki.Evidence{{
				Quote: "the validator drops unverified quotes", LineStart: 2, LineEnd: 2,
				SourceFilePath: filepath.Base(env.srcPath),
			}},
		}},
		At: time.Date(2026, 5, 4, 15, 2, 8, 0, time.UTC),
	}
	answerPath := writeAnswerFileForPromote(t, env, "no-save", in)
	promoteCmd.Flags().Set("title", "No Save CLI Page")
	promoteCmd.Flags().Set("no-save", "true")
	if err := runPromote(promoteCmd, []string{answerPath}); err != nil {
		t.Fatalf("runPromote: %v", err)
	}
	logPath := filepath.Join(env.wikiDir, "log.md")
	if data, err := os.ReadFile(logPath); err == nil {
		if strings.Contains(string(data), "**promote**") {
			t.Errorf("--no-save should suppress log entry; got log:\n%s", data)
		}
	}
}

// TestPromote_PrintsHumanReadableSummary captures stdout and asserts the
// success summary mirrors the format the plan describes.
func TestPromote_PrintsHumanReadableSummary(t *testing.T) {
	source := "alpha\nthe validator drops unverified quotes\ngamma\n"
	env := setupPromoteEnv(t, source)
	resetPromoteFlags(t)
	in := wiki.SavedAnswerInput{
		Question: "summary test",
		Answer:   "verbatim",
		Model:    "m",
		Pages: []wiki.Page{{Title: "T",
			Evidence: []wiki.Evidence{{
				Quote: "the validator drops unverified quotes", LineStart: 2, LineEnd: 2,
				SourceFilePath: filepath.Base(env.srcPath),
			}},
		}},
		At: time.Date(2026, 5, 4, 15, 2, 8, 0, time.UTC),
	}
	answerPath := writeAnswerFileForPromote(t, env, "summary-test", in)
	promoteCmd.Flags().Set("title", "Summary Page")

	origStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })
	err := runPromote(promoteCmd, []string{answerPath})
	w.Close()
	out, _ := io.ReadAll(r)
	os.Stdout = origStdout
	if err != nil {
		t.Fatalf("runPromote: %v", err)
	}
	got := string(out)
	for _, want := range []string{
		`wrote page "Summary Page"`,
		"saved:",
		"Summary Page.md",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("stdout missing %q; got:\n%s", want, got)
		}
	}
}

// TestPromote_EvidenceInvalidRendersStructuredError mutates the source
// file before promote so re-validation drops every quote; the runner
// should surface an evidence_invalid UserError with a remediation hint.
func TestPromote_EvidenceInvalidRendersStructuredError(t *testing.T) {
	source := "alpha\nthe validator drops unverified quotes\ngamma\n"
	env := setupPromoteEnv(t, source)
	resetPromoteFlags(t)
	in := wiki.SavedAnswerInput{
		Question: "evidence invalid",
		Answer:   "verbatim",
		Model:    "m",
		Pages: []wiki.Page{{Title: "T",
			Evidence: []wiki.Evidence{{
				Quote: "the validator drops unverified quotes", LineStart: 2, LineEnd: 2,
				SourceFilePath: filepath.Base(env.srcPath),
			}},
		}},
		At: time.Date(2026, 5, 4, 15, 2, 8, 0, time.UTC),
	}
	answerPath := writeAnswerFileForPromote(t, env, "evidence-invalid", in)
	// Mutate source after answer is saved.
	if err := os.WriteFile(env.srcPath, []byte("totally different\n"), 0644); err != nil {
		t.Fatalf("rewrite source: %v", err)
	}
	promoteCmd.Flags().Set("title", "Stale Page")
	err := runPromote(promoteCmd, []string{answerPath})
	if err == nil {
		t.Fatal("expected error for stale evidence")
	}
	var ue *cliutil.UserError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *cliutil.UserError; got %T: %v", err, err)
	}
	if !strings.Contains(ue.Cause, "evidence_invalid") {
		t.Errorf("cause should mention evidence_invalid; got %q", ue.Cause)
	}
}
