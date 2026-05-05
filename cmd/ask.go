package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/mattn/go-isatty"
	"github.com/mritunjaysharma394/llmwiki/internal/cliutil"
	"github.com/mritunjaysharma394/llmwiki/internal/db"
	"github.com/mritunjaysharma394/llmwiki/internal/wiki"
	"github.com/spf13/cobra"
)

// pageBundle pairs a page with the FTS-matched evidence rows that pulled it
// into the answer context. Held in a small map keyed by page ID to dedup
// pages that match both the page-level and evidence-level FTS searches.
type pageBundle struct {
	page     db.PageRecord
	evidence []db.Evidence
}

var askCmd = &cobra.Command{
	Use:   "ask <question>",
	Short: "Ask a question and get an answer from your wiki",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runAsk,
}

func init() {
	askCmd.Flags().Bool("no-stream", false, "force buffered output (no streaming)")
	askCmd.Flags().Bool("no-save", false, "skip auto-archiving the answer")
	askCmd.Flags().String("out", "", "also write the answer to this path")
}

func runAsk(cmd *cobra.Command, args []string) error {
	question := strings.Join(args, " ")
	ctx := cmd.Context()

	pageHits, err := database.SearchPages(question, 5)
	if err != nil {
		fmt.Fprintln(os.Stderr, "(page FTS unavailable; scanning all pages)")
		pageHits, _ = database.AllPages()
		if len(pageHits) > 5 {
			pageHits = pageHits[:5]
		}
	}
	evHits, _ := database.SearchEvidence(question, 10)

	bundles := map[int64]*pageBundle{}
	var order []int64
	for _, p := range pageHits {
		bundles[p.ID] = &pageBundle{page: p}
		order = append(order, p.ID)
	}
	for _, h := range evHits {
		if _, ok := bundles[h.PageID]; !ok {
			page, _ := database.GetPageByID(h.PageID)
			if page == nil {
				continue
			}
			bundles[h.PageID] = &pageBundle{page: *page}
			order = append(order, h.PageID)
		}
		bundles[h.PageID].evidence = append(bundles[h.PageID].evidence, h.Evidence)
	}

	if len(bundles) == 0 {
		all, err := database.AllPages()
		if err != nil {
			return fmt.Errorf("loading pages: %w", err)
		}
		if len(all) == 0 {
			return cliutil.Wrap(
				"no pages in wiki",
				nil,
				"run 'llmwiki ingest <source>' to add content first",
			)
		}
		if len(all) > 5 {
			all = all[:5]
		}
		for _, p := range all {
			bundles[p.ID] = &pageBundle{page: p}
			order = append(order, p.ID)
		}
	}

	// Resolve source_file_id -> relative_path once per ask, by gathering every
	// source backing any candidate page. Built lazily so legacy pages without
	// a source_file_id incur no DB hit beyond what they need.
	sourceFilePaths := buildSourceFilePathLookup(order, bundles)

	var pages []wiki.Page
	for _, id := range order {
		b := bundles[id]
		var ev []wiki.Evidence
		for _, e := range b.evidence {
			ev = append(ev, wiki.Evidence{
				Quote:          e.Quote,
				LineStart:      e.LineStart,
				LineEnd:        e.LineEnd,
				SourceFilePath: pathForEvidence(e, sourceFilePaths),
			})
		}
		if len(ev) == 0 {
			dbEv, _ := database.GetEvidenceForPage(b.page.ID)
			for _, e := range dbEv {
				ev = append(ev, wiki.Evidence{
					Quote:          e.Quote,
					LineStart:      e.LineStart,
					LineEnd:        e.LineEnd,
					SourceFilePath: pathForEvidence(e, sourceFilePaths),
				})
				if len(ev) >= 3 {
					break
				}
			}
		}
		pages = append(pages, wiki.Page{
			Title:    b.page.Title,
			Body:     b.page.Body,
			Evidence: ev,
		})
	}

	isTTY := isatty.IsTerminal(os.Stdout.Fd())
	noStream, _ := cmd.Flags().GetBool("no-stream")

	var answer string
	if isTTY && !noStream {
		var buf strings.Builder
		mw := io.MultiWriter(os.Stdout, &buf)
		fmt.Println()
		// Phase C Task 6: activeSchema is loaded by cmd/root.go's
		// loadConfig from AGENTS.md / CLAUDE.md, falling back to
		// schema.Bundled() when neither file exists.
		ans, err := wiki.StreamAnswer(ctx, llmClient, question, pages, mw, activeSchema)
		fmt.Println()
		if err != nil {
			return fmt.Errorf("streaming answer: %w", err)
		}
		answer = ans
		rendered, rerr := glamour.Render(answer, glamourStyle())
		if rerr == nil {
			fmt.Println("\n──────")
			fmt.Print(rendered)
		}
	} else {
		spin := startSpinner("Thinking...")
		// Phase C Task 6: activeSchema is loaded by cmd/root.go's
		// loadConfig from AGENTS.md / CLAUDE.md, falling back to
		// schema.Bundled() when neither file exists.
		ans, err := wiki.AnswerQuestion(ctx, llmClient, question, pages, activeSchema)
		spin.Stop()
		if err != nil {
			return fmt.Errorf("llm answer: %w", err)
		}
		answer = ans
		if isTTY {
			rendered, _ := glamour.Render(answer, glamourStyle())
			fmt.Print(rendered)
		} else {
			fmt.Println(answer)
		}
	}

	printSources(pages, isTTY)

	// Phase F: chronicle this ask in log.md. Best-effort — a write
	// failure here does not invalidate the answer the user already saw.
	// Logged BEFORE auto-promote so the chronicle ordering is "ask then
	// promote" when both fire.
	_ = wiki.AppendLog(cfg.Wiki.WikiDir, wiki.LogEntry{
		At:   time.Now().UTC(),
		Kind: "ask",
		Payload: fmt.Sprintf("%q → %d chars, %d sources",
			question, len(answer), len(pages)),
	})

	if shouldSkipSave(cmd, cfg) {
		return nil
	}
	filePath, err := saveAnswer(cmd, cfg, question, answer, pages)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  WARN failed to save answer: %v\n", err)
		return nil
	}
	if filePath == "" {
		return nil
	}

	// Sub-project 8 Phase B: auto-promote pipeline.
	//
	// Trust property: the saved-answer file is ALREADY on disk at this
	// point and stays on disk regardless of gate / validator outcome.
	// This preserves plan §2's "two locks" — gate-fail OR validator-fail
	// → answer stays in `.llmwiki/answers/` for manual `promote` review.
	// We never silently drop.
	maybeAutoPromote(cmd, cfg, filePath, question, answer, pages)
	return nil
}

// maybeAutoPromote runs the four-signal heuristic gate and, on pass,
// invokes wiki.PromoteAnswer with Source="auto". One-line user-facing
// output for each branch:
//
//	pass:                "→ filed as [[Title]]"
//	gate fail / skipped: "→ saved to <path> (<reason>)"
//	validator fail:      "→ saved to <path> (validator dropped quotes; see promote_failed)"
//	title-collision:     "→ saved to <path> (title exists: \"<existing>\"; pass --title to promote)"
//	auto-promote OFF:    "saved: <path>"  (back-compat shape)
//
// All branches keep filePath on disk per plan §2's two-lock contract;
// the user can hand-promote later via `llmwiki promote <slug>`.
func maybeAutoPromote(cmd *cobra.Command, c *Config, filePath, question, answer string, pages []wiki.Page) {
	// Opt-out: explicit `[ask] auto_promote = false` reverts to the
	// pre-v0.8 "saved: <path>" surface (kept byte-stable).
	if !c.Ask.AutoPromoteOrDefault() {
		fmt.Printf("\nsaved: %s\n", filePath)
		return
	}

	// Build a ParsedSavedAnswer in-memory directly from the same fields
	// saveAnswer just rendered. We could round-trip via FormatSavedAnswer
	// → ParseSavedAnswer (and the disk file IS that exact rendering), but
	// we already have the typed values here — the in-memory build skips
	// one parse and keeps the gate's input source-of-truth identical to
	// what we just wrote to disk.
	parsed := wiki.ParsedSavedAnswer{
		Question:  question,
		Answer:    answer,
		Model:     c.LLM.Model,
		CreatedAt: time.Now().UTC(),
		Pages:     pages,
	}
	apc := wiki.AutoPromoteConfig{
		HedgingPhrases: c.Ask.AutoPromoteHedgingPhrases,
		SkipScore:      c.Ask.AutoPromoteSkipScore,
		ScoreFloor:     c.Ask.AutoPromoteScoreFloor,
	}
	verdict, reason := wiki.EvaluateAutoPromote(parsed, database, apc)
	if !verdict.AutoPromote {
		fmt.Printf("\n→ saved to %s (%s)\n", filePath, reason)
		return
	}

	// Heuristic gate cleared → run the trust validator via PromoteAnswer.
	// On any failure (validator, title-collision, generic error) the
	// saved-answer file is already on disk; we surface the reason and
	// leave it for manual review.
	res, perr := wiki.PromoteAnswer(cmd.Context(), toWikiIngestConfig(c), database, llmClient, filePath, wiki.PromoteOptions{
		Schema: activeSchema,
		Source: "auto",
	})
	if perr != nil {
		switch {
		case errors.Is(perr, wiki.ErrEvidenceInvalid):
			// Validator dropped quotes (source likely changed since the
			// ask). The trust property held; the answer stays for the
			// user to inspect / re-ask.
			fmt.Printf("\n→ saved to %s (validator dropped quotes; promote_failed)\n", filePath)
		case errors.Is(perr, wiki.ErrTitleExists):
			// Title collision — auto-promote MUST NOT silently overwrite.
			// Plan §2: skip; user can hand-promote with --title.
			fmt.Printf("\n→ saved to %s (title exists: %q; run `llmwiki promote --title <new> %s` to promote)\n",
				filePath, res.Title, filepath.Base(filePath))
		default:
			fmt.Fprintf(os.Stderr, "  WARN auto-promote failed: %v\n", perr)
			fmt.Printf("\n→ saved to %s (auto-promote error)\n", filePath)
		}
		return
	}
	fmt.Printf("\n→ filed as [[%s]]\n", res.Title)
}

func glamourStyle() string {
	if os.Getenv("NO_COLOR") != "" {
		return "notty"
	}
	return "auto"
}

// buildSourceFilePathLookup walks every candidate page in `order`, collects
// the union of their backing source IDs, and asks the DB for the source_files
// rows under each. The returned map keys source_file row IDs to their
// relative paths so evidence rendering can annotate quotes as "(path:a-b)".
//
// Done in cmd/ask.go (rather than via a JOIN in db.GetEvidenceForPage /
// db.SearchEvidence) so the Phase A schema stays untouched.
func buildSourceFilePathLookup(order []int64, bundles map[int64]*pageBundle) map[int64]string {
	out := map[int64]string{}
	seenSource := map[int64]bool{}
	for _, id := range order {
		b, ok := bundles[id]
		if !ok {
			continue
		}
		for _, sid := range b.page.SourceIDs {
			if seenSource[sid] {
				continue
			}
			seenSource[sid] = true
			files, err := database.GetSourceFiles(sid)
			if err != nil {
				continue
			}
			for _, f := range files {
				out[f.ID] = f.RelativePath
			}
		}
	}
	return out
}

// pathForEvidence resolves a db.Evidence row's SourceFileID to its relative
// path via the prebuilt lookup. Returns "" when SourceFileID is nil (legacy
// rows pre-dating the source_files table) so callers can fall back to the
// "lines a-b" annotation.
func pathForEvidence(e db.Evidence, lookup map[int64]string) string {
	if e.SourceFileID == nil {
		return ""
	}
	return lookup[*e.SourceFileID]
}

func printSources(pages []wiki.Page, isTTY bool) {
	if len(pages) == 0 {
		return
	}
	var sb strings.Builder
	sb.WriteString("\n── Sources ──\n")
	for i, p := range pages {
		sb.WriteString(fmt.Sprintf("[%d] %s\n", i+1, p.Title))
		for _, e := range p.Evidence {
			annotation := fmt.Sprintf("lines %d-%d", e.LineStart, e.LineEnd)
			if e.SourceFilePath != "" {
				annotation = fmt.Sprintf("%s:%d-%d", e.SourceFilePath, e.LineStart, e.LineEnd)
			}
			sb.WriteString(fmt.Sprintf("    > %q  (%s)\n", e.Quote, annotation))
		}
	}
	out := sb.String()
	if isTTY {
		rendered, err := glamour.Render(out, glamourStyle())
		if err == nil {
			fmt.Print(rendered)
			return
		}
	}
	fmt.Print(out)
}

func shouldSkipSave(cmd *cobra.Command, c *Config) bool {
	noSave, _ := cmd.Flags().GetBool("no-save")
	if noSave {
		return true
	}
	if c.Ask.AutoSave != nil && !*c.Ask.AutoSave {
		return true
	}
	return false
}

func saveAnswer(cmd *cobra.Command, c *Config, question, answer string, pages []wiki.Page) (string, error) {
	now := time.Now().UTC()
	dir := filepath.Join(filepath.Dir(c.Wiki.WikiDir), "answers")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	slug := slugify(question)
	if slug == "" {
		slug = "question"
	}
	filename := fmt.Sprintf("%s-%s.md", now.Format("2006-01-02-150405"), slug)
	path := filepath.Join(dir, filename)
	body := wiki.FormatSavedAnswer(wiki.SavedAnswerInput{
		Question: question,
		Answer:   answer,
		Model:    c.LLM.Model,
		Pages:    pages,
		At:       now,
	})
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		return "", err
	}

	var pageIDs []int64
	for _, p := range pages {
		stored, _ := database.GetPage(p.Title)
		if stored != nil {
			pageIDs = append(pageIDs, stored.ID)
		}
	}
	_, _ = database.InsertSavedAnswer(db.SavedAnswer{
		Question:     question,
		Answer:       answer,
		Model:        c.LLM.Model,
		CitedPageIDs: pageIDs,
		FilePath:     path,
		CreatedAt:    now,
	})

	if outPath, _ := cmd.Flags().GetString("out"); outPath != "" {
		if err := os.WriteFile(outPath, []byte(body), 0644); err != nil {
			return "", fmt.Errorf("--out %s: %w", outPath, err)
		}
	}
	return path, nil
}
