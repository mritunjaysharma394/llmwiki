package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/mattn/go-isatty"
	"github.com/mritunjaysharma394/llmwiki/internal/db"
	"github.com/mritunjaysharma394/llmwiki/internal/wiki"
	"github.com/spf13/cobra"
)

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

	type pageBundle struct {
		page     db.PageRecord
		evidence []db.Evidence
	}
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
			fmt.Println("No pages in wiki yet. Run `llmwiki ingest <source>` first.")
			return nil
		}
		if len(all) > 5 {
			all = all[:5]
		}
		for _, p := range all {
			bundles[p.ID] = &pageBundle{page: p}
			order = append(order, p.ID)
		}
	}

	var pages []wiki.Page
	for _, id := range order {
		b := bundles[id]
		var ev []wiki.Evidence
		for _, e := range b.evidence {
			ev = append(ev, wiki.Evidence{Quote: e.Quote, LineStart: e.LineStart, LineEnd: e.LineEnd})
		}
		if len(ev) == 0 {
			dbEv, _ := database.GetEvidenceForPage(b.page.ID)
			for _, e := range dbEv {
				ev = append(ev, wiki.Evidence{Quote: e.Quote, LineStart: e.LineStart, LineEnd: e.LineEnd})
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
		ans, err := wiki.StreamAnswer(ctx, llmClient, question, pages, mw)
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
		ans, err := wiki.AnswerQuestion(ctx, llmClient, question, pages)
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

	if !shouldSkipSave(cmd, cfg) {
		filePath, err := saveAnswer(cmd, cfg, question, answer, pages)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  WARN failed to save answer: %v\n", err)
		} else if filePath != "" {
			fmt.Printf("\nsaved: %s\n", filePath)
		}
	}
	return nil
}

func glamourStyle() string {
	if os.Getenv("NO_COLOR") != "" {
		return "notty"
	}
	return "auto"
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
			sb.WriteString(fmt.Sprintf("    > %q  (lines %d-%d)\n", e.Quote, e.LineStart, e.LineEnd))
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
