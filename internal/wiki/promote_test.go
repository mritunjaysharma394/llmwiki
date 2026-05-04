package wiki

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mritunjaysharma394/llmwiki/internal/db"
	"github.com/mritunjaysharma394/llmwiki/internal/ingest"
	"github.com/mritunjaysharma394/llmwiki/internal/llm"
)

// promoteTestFixture wires together a tempdir wiki + raw + db, ingests a
// synthetic source file, hand-authors a saved-answer file referencing the
// ingested file, and returns the assembled inputs PromoteAnswer expects.
//
// All on-disk paths are absolute; the wiki/raw/answers dirs are siblings
// the same way `llmwiki init` lays them out. The DB is opened against a
// real sqlite file in tempdir so the queries the runner uses (GetSource,
// GetSourceFiles, UpsertSourceFile, etc.) all behave for real.
type promoteTestFixture struct {
	WikiDir    string
	RawDir     string
	AnswersDir string
	DB         *db.DB
	SourcePath string
	SourceURI  string
	SourceID   int64
	FileID     int64
	Cfg        IngestSourceConfig
	AnswerPath string
}

func setupPromoteFixture(t *testing.T, sourceContent string) *promoteTestFixture {
	t.Helper()
	root := t.TempDir()
	wikiDir := filepath.Join(root, "wiki")
	rawDir := filepath.Join(root, "raw")
	answersDir := filepath.Join(root, "answers")
	for _, d := range []string{wikiDir, rawDir, answersDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	dbPath := filepath.Join(root, "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	// Source file lives at sourceURI on disk; relative path under it is
	// the basename. mcp.write_page's readSourceFileContent treats the URI
	// as a file path when it exists, so this mirrors how MCP resolves
	// real-world local sources.
	srcPath := filepath.Join(root, "src.md")
	if err := os.WriteFile(srcPath, []byte(sourceContent), 0644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	sourceFile := ingest.NewSourceFile(filepath.Base(srcPath), []byte(sourceContent))
	sourceID, err := database.UpsertSource(srcPath, sourceFile.ContentHash)
	if err != nil {
		t.Fatalf("UpsertSource: %v", err)
	}
	fileID, err := database.UpsertSourceFile(db.SourceFile{
		SourceID:     sourceID,
		RelativePath: sourceFile.RelativePath,
		ContentHash:  sourceFile.ContentHash,
		ByteSize:     sourceFile.ByteSize,
		LineCount:    sourceFile.LineCount,
	})
	if err != nil {
		t.Fatalf("UpsertSourceFile: %v", err)
	}
	cfg := IngestSourceConfig{
		WikiDir:          wikiDir,
		RawDir:           rawDir,
		RespectGitignore: true,
	}
	return &promoteTestFixture{
		WikiDir:    wikiDir,
		RawDir:     rawDir,
		AnswersDir: answersDir,
		DB:         database,
		SourcePath: srcPath,
		SourceURI:  srcPath,
		SourceID:   sourceID,
		FileID:     fileID,
		Cfg:        cfg,
	}
}

// writeAnswerFile materializes a saved-answer file from a SavedAnswerInput.
// Filename uses the same `<ts>-<slug>.md` shape cmd/ask.go's saveAnswer
// produces.
func (fx *promoteTestFixture) writeAnswerFile(t *testing.T, in SavedAnswerInput, slug string) string {
	t.Helper()
	body := FormatSavedAnswer(in)
	ts := in.At.UTC().Format("2006-01-02-150405")
	name := ts + "-" + slug + ".md"
	path := filepath.Join(fx.AnswersDir, name)
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("write answer: %v", err)
	}
	fx.AnswerPath = path
	return path
}

// stubLLMClient is a no-op llm.Client that fails loudly if PromoteAnswer
// happens to call into it on the default (--rewrite=false) path. The
// rewrite test substitutes a more useful stub.
type stubLLMClient struct {
	completeFn func(ctx context.Context, system, user string) (string, error)
}

func (s *stubLLMClient) Complete(ctx context.Context, system, user string) (string, error) {
	if s.completeFn != nil {
		return s.completeFn(ctx, system, user)
	}
	return "", errors.New("stubLLMClient.Complete unexpectedly called")
}
func (s *stubLLMClient) CompleteStructured(ctx context.Context, system, user string, ts llm.ToolSchema) (map[string]any, error) {
	return nil, errors.New("stubLLMClient.CompleteStructured unexpectedly called")
}
func (s *stubLLMClient) CompleteStream(ctx context.Context, system, user string, w io.Writer) (string, error) {
	return s.Complete(ctx, system, user)
}

// TestPromoteAnswer_HappyPath ingests a synthetic source whose content
// includes the literal substring `the validator drops unverified quotes`,
// hand-authors a saved-answer file quoting that span, and asserts that
// PromoteAnswer lands a real wiki page on disk + in the DB and appends a
// **promote** line to log.md.
func TestPromoteAnswer_HappyPath(t *testing.T) {
	source := "Line one of source.\nthe validator drops unverified quotes\nLine three.\n"
	fx := setupPromoteFixture(t, source)
	at := time.Date(2026, 5, 4, 15, 2, 8, 0, time.UTC)
	in := SavedAnswerInput{
		Question: "how does the validator work?",
		Answer:   "The validator drops unverified quotes before write.",
		Model:    "test-model",
		Pages: []Page{{
			Title: "Validator Internals",
			Evidence: []Evidence{{
				Quote:          "the validator drops unverified quotes",
				LineStart:      2,
				LineEnd:        2,
				SourceFilePath: filepath.Base(fx.SourcePath),
			}},
		}},
		At: at,
	}
	answerPath := fx.writeAnswerFile(t, in, "how-does-the-validator-work")

	res, err := PromoteAnswer(context.Background(), fx.Cfg, fx.DB, &stubLLMClient{}, answerPath, PromoteOptions{Title: "Validator Internals"})
	if err != nil {
		t.Fatalf("PromoteAnswer: %v", err)
	}
	if res.Title != "Validator Internals" {
		t.Errorf("Title = %q, want %q", res.Title, "Validator Internals")
	}
	wantPath := filepath.Join(fx.WikiDir, "Validator Internals.md")
	if res.Path != wantPath {
		t.Errorf("Path = %q, want %q", res.Path, wantPath)
	}
	if res.EvidenceQuotes != 1 {
		t.Errorf("EvidenceQuotes = %d, want 1", res.EvidenceQuotes)
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("page file not written: %v", err)
	}
	stored, err := fx.DB.GetPage("Validator Internals")
	if err != nil || stored == nil {
		t.Fatalf("DB.GetPage: stored=%v err=%v", stored, err)
	}
	evRows, err := fx.DB.GetEvidenceForPage(stored.ID)
	if err != nil {
		t.Fatalf("GetEvidenceForPage: %v", err)
	}
	if len(evRows) != 1 {
		t.Fatalf("evidence rows = %d, want 1", len(evRows))
	}
	if evRows[0].SourceFileID == nil || *evRows[0].SourceFileID != fx.FileID {
		t.Errorf("evidence source_file_id = %v, want %d", evRows[0].SourceFileID, fx.FileID)
	}
	// Trust property: the persisted quote substring-matches the on-disk source.
	srcBytes, err := os.ReadFile(fx.SourcePath)
	if err != nil {
		t.Fatalf("re-read source: %v", err)
	}
	if !strings.Contains(string(srcBytes), evRows[0].Quote) {
		t.Errorf("quote not a substring of source: %q", evRows[0].Quote)
	}

	// log.md got a **promote** line.
	logPath := filepath.Join(fx.WikiDir, "log.md")
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log.md: %v", err)
	}
	if !strings.Contains(string(logBytes), "**promote**") {
		t.Errorf("log.md missing **promote** entry:\n%s", logBytes)
	}
}

// TestPromoteAnswer_StaleEvidenceReturnsErrEvidenceInvalid mutates the
// source file between answer-write and promote so the quoted substring no
// longer matches. The defensive re-validation should drop the only quote
// and PromoteAnswer should return ErrEvidenceInvalid; nothing should hit
// disk and log.md should be unchanged.
func TestPromoteAnswer_StaleEvidenceReturnsErrEvidenceInvalid(t *testing.T) {
	source := "Line one.\nthe validator drops unverified quotes\nLine three.\n"
	fx := setupPromoteFixture(t, source)
	in := SavedAnswerInput{
		Question: "how does the validator work?",
		Answer:   "Verbatim.",
		Model:    "m",
		Pages: []Page{{
			Title: "T",
			Evidence: []Evidence{{
				Quote:          "the validator drops unverified quotes",
				LineStart:      2,
				LineEnd:        2,
				SourceFilePath: filepath.Base(fx.SourcePath),
			}},
		}},
		At: time.Date(2026, 5, 4, 15, 2, 8, 0, time.UTC),
	}
	answerPath := fx.writeAnswerFile(t, in, "how-does-the-validator-work")

	// Mutate: rewrite source so the quote no longer substring-matches.
	if err := os.WriteFile(fx.SourcePath, []byte("totally different content\n"), 0644); err != nil {
		t.Fatalf("rewrite source: %v", err)
	}

	res, err := PromoteAnswer(context.Background(), fx.Cfg, fx.DB, &stubLLMClient{}, answerPath, PromoteOptions{Title: "Stale Page"})
	if !errors.Is(err, ErrEvidenceInvalid) {
		t.Fatalf("err = %v, want ErrEvidenceInvalid", err)
	}
	if len(res.DroppedQuotes) == 0 {
		t.Errorf("DroppedQuotes empty; expected at least one entry naming the failing quote")
	}
	// No disk write under wiki dir for this title.
	pagePath := filepath.Join(fx.WikiDir, "Stale Page.md")
	if _, err := os.Stat(pagePath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("page file should not exist after evidence_invalid: stat err = %v", err)
	}
	// No log.md (or no **promote** entry in it).
	logPath := filepath.Join(fx.WikiDir, "log.md")
	if data, err := os.ReadFile(logPath); err == nil {
		if strings.Contains(string(data), "**promote**") {
			t.Errorf("log.md should not contain **promote** after evidence_invalid:\n%s", data)
		}
	}
}

// TestPromoteAnswer_TitleCollisionReturnsErrTitleExists pre-seeds a page
// titled "Validator Internals" and then promotes with the same title; the
// runner should reject before any disk write.
func TestPromoteAnswer_TitleCollisionReturnsErrTitleExists(t *testing.T) {
	source := "alpha\nthe validator drops unverified quotes\ngamma\n"
	fx := setupPromoteFixture(t, source)
	// Pre-seed.
	preExistingPath := filepath.Join(fx.WikiDir, "Validator Internals.md")
	if err := os.WriteFile(preExistingPath, []byte("---\ntitle: Validator Internals\n---\nseed body\n"), 0644); err != nil {
		t.Fatalf("seed page: %v", err)
	}
	if err := fx.DB.UpsertPage(db.PageRecord{
		Title:       "Validator Internals",
		Path:        preExistingPath,
		Body:        "seed body",
		ContentHash: HashContent("seed body"),
		SourceIDs:   []int64{fx.SourceID},
	}); err != nil {
		t.Fatalf("seed UpsertPage: %v", err)
	}
	in := SavedAnswerInput{
		Question: "anything",
		Answer:   "verbatim",
		Model:    "m",
		Pages: []Page{{
			Title: "Validator Internals",
			Evidence: []Evidence{{
				Quote:          "the validator drops unverified quotes",
				LineStart:      2,
				LineEnd:        2,
				SourceFilePath: filepath.Base(fx.SourcePath),
			}},
		}},
		At: time.Date(2026, 5, 4, 15, 2, 8, 0, time.UTC),
	}
	answerPath := fx.writeAnswerFile(t, in, "anything")
	res, err := PromoteAnswer(context.Background(), fx.Cfg, fx.DB, &stubLLMClient{}, answerPath, PromoteOptions{Title: "Validator Internals"})
	if !errors.Is(err, ErrTitleExists) {
		t.Fatalf("err = %v, want ErrTitleExists", err)
	}
	if res.Path != preExistingPath {
		t.Errorf("res.Path = %q, want %q (existing page path)", res.Path, preExistingPath)
	}
}

// TestPromoteAnswer_DefaultTitleFromQuestion confirms that a blank
// Title in PromoteOptions falls through to the slugify-then-Title-Case
// of the answer's question frontmatter.
func TestPromoteAnswer_DefaultTitleFromQuestion(t *testing.T) {
	source := "alpha\nthe validator drops unverified quotes\ngamma\n"
	fx := setupPromoteFixture(t, source)
	in := SavedAnswerInput{
		Question: "how does the validator work?",
		Answer:   "Yes.",
		Model:    "m",
		Pages: []Page{{
			Title: "T",
			Evidence: []Evidence{{
				Quote:          "the validator drops unverified quotes",
				LineStart:      2,
				LineEnd:        2,
				SourceFilePath: filepath.Base(fx.SourcePath),
			}},
		}},
		At: time.Date(2026, 5, 4, 15, 2, 8, 0, time.UTC),
	}
	answerPath := fx.writeAnswerFile(t, in, "how-does-the-validator-work")
	res, err := PromoteAnswer(context.Background(), fx.Cfg, fx.DB, &stubLLMClient{}, answerPath, PromoteOptions{})
	if err != nil {
		t.Fatalf("PromoteAnswer: %v", err)
	}
	want := "How Does The Validator Work"
	if res.Title != want {
		t.Errorf("Title = %q, want %q", res.Title, want)
	}
}

// TestPromoteAnswer_RewriteFlagFallsBackOnValidationFailure asserts that
// a --rewrite call whose rewritten body fails validation falls back to
// the verbatim answer body and emits a WARN line on stderr.
func TestPromoteAnswer_RewriteFlagFallsBackOnValidationFailure(t *testing.T) {
	source := "alpha\nthe validator drops unverified quotes\ngamma\n"
	fx := setupPromoteFixture(t, source)
	answerBody := "The validator drops unverified quotes before write."
	in := SavedAnswerInput{
		Question: "rewrite test",
		Answer:   answerBody,
		Model:    "m",
		Pages: []Page{{
			Title: "T",
			Evidence: []Evidence{{
				Quote:          "the validator drops unverified quotes",
				LineStart:      2,
				LineEnd:        2,
				SourceFilePath: filepath.Base(fx.SourcePath),
			}},
		}},
		At: time.Date(2026, 5, 4, 15, 2, 8, 0, time.UTC),
	}
	answerPath := fx.writeAnswerFile(t, in, "rewrite-test")

	// Stub LLM returns a body whose quotes won't validate.
	client := &stubLLMClient{
		completeFn: func(ctx context.Context, system, user string) (string, error) {
			return "Hallucinated rewrite that has nothing in common with source.", nil
		},
	}

	// Capture stderr so we can assert the WARN line.
	origStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = origStderr })

	res, err := PromoteAnswer(context.Background(), fx.Cfg, fx.DB, client, answerPath, PromoteOptions{
		Title:   "Rewrite Page",
		Rewrite: true,
	})
	w.Close()
	stderrBytes, _ := io.ReadAll(r)
	os.Stderr = origStderr
	if err != nil {
		t.Fatalf("PromoteAnswer: %v", err)
	}
	if res.RewriteApplied {
		t.Errorf("RewriteApplied = true; expected false (fallback path)")
	}
	pagePath := filepath.Join(fx.WikiDir, "Rewrite Page.md")
	pageBody, err := os.ReadFile(pagePath)
	if err != nil {
		t.Fatalf("read written page: %v", err)
	}
	if !strings.Contains(string(pageBody), answerBody) {
		t.Errorf("written page body should fall back to verbatim answer body. got:\n%s", pageBody)
	}
	if !strings.Contains(string(stderrBytes), "WARN rewrite produced unverifiable body") {
		t.Errorf("expected WARN on stderr; got:\n%s", stderrBytes)
	}
}

// TestPromoteAnswer_NoSaveSkipsLogEntry asserts that PromoteOptions.NoSave
// = true lands the page but leaves log.md untouched.
func TestPromoteAnswer_NoSaveSkipsLogEntry(t *testing.T) {
	source := "alpha\nthe validator drops unverified quotes\ngamma\n"
	fx := setupPromoteFixture(t, source)
	in := SavedAnswerInput{
		Question: "no save test",
		Answer:   "verbatim",
		Model:    "m",
		Pages: []Page{{
			Title: "T",
			Evidence: []Evidence{{
				Quote:          "the validator drops unverified quotes",
				LineStart:      2,
				LineEnd:        2,
				SourceFilePath: filepath.Base(fx.SourcePath),
			}},
		}},
		At: time.Date(2026, 5, 4, 15, 2, 8, 0, time.UTC),
	}
	answerPath := fx.writeAnswerFile(t, in, "no-save")
	if _, err := PromoteAnswer(context.Background(), fx.Cfg, fx.DB, &stubLLMClient{}, answerPath, PromoteOptions{
		Title:  "No Save Page",
		NoSave: true,
	}); err != nil {
		t.Fatalf("PromoteAnswer: %v", err)
	}
	pagePath := filepath.Join(fx.WikiDir, "No Save Page.md")
	if _, err := os.Stat(pagePath); err != nil {
		t.Fatalf("page should exist: %v", err)
	}
	logPath := filepath.Join(fx.WikiDir, "log.md")
	if data, err := os.ReadFile(logPath); err == nil {
		if strings.Contains(string(data), "**promote**") {
			t.Errorf("log.md should NOT have **promote** when NoSave=true:\n%s", data)
		}
	}
}

// TestPromoteAnswer_MissingAnswerFileReturnsError asserts that a non-
// existent answerPath wraps os.ErrNotExist.
func TestPromoteAnswer_MissingAnswerFileReturnsError(t *testing.T) {
	fx := setupPromoteFixture(t, "ignored\n")
	_, err := PromoteAnswer(context.Background(), fx.Cfg, fx.DB, &stubLLMClient{}, "/nonexistent/answer.md", PromoteOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, want wrapping os.ErrNotExist", err)
	}
}
