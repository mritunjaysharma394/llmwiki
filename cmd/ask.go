package cmd

import (
	"fmt"
	"strings"

	"github.com/mritunjaysharma394/llmwiki/internal/wiki"
	"github.com/spf13/cobra"
)

var askCmd = &cobra.Command{
	Use:   "ask <question>",
	Short: "Ask a question and get an answer from your wiki",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runAsk,
}

func runAsk(cmd *cobra.Command, args []string) error {
	question := strings.Join(args, " ")
	ctx := cmd.Context()

	// FTS5 search for relevant pages
	records, err := database.SearchPages(question, 5)
	if err != nil || len(records) == 0 {
		if err != nil {
			fmt.Println("(FTS search unavailable, scanning all pages)")
		} else {
			fmt.Println("(no FTS matches, scanning all pages)")
		}
		records, err = database.AllPages()
		if err != nil {
			return fmt.Errorf("searching pages: %w", err)
		}
		if len(records) > 5 {
			records = records[:5]
		}
	}

	if len(records) == 0 {
		fmt.Println("No pages in wiki yet. Run `llmwiki ingest <source>` first.")
		return nil
	}

	// Convert to wiki.Page for ops
	var pages []wiki.Page
	for _, r := range records {
		pages = append(pages, wiki.Page{
			Title: r.Title,
			Body:  r.Body,
		})
	}

	spin := startSpinner("Thinking...")
	answer, err := wiki.AnswerQuestion(ctx, llmClient, question, pages)
	spin.Stop()
	if err != nil {
		return fmt.Errorf("llm answer: %w", err)
	}

	fmt.Printf("\n%s\n\n", answer)
	fmt.Print("Sources: ")
	titles := make([]string, len(records))
	for i, r := range records {
		titles[i] = r.Title
	}
	fmt.Println(strings.Join(titles, ", "))
	return nil
}
