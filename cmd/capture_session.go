// Package cmd — capture_session.go
//
// `llmwiki capture-session` is the sub-project 8 Phase E command that
// reads a Claude Code Stop-hook session payload from stdin and turns
// any wiki-relevant assistant turns into a saved-answer file at
// `.llmwiki/answers/<ts>-session-<slug>.md`. The auto-promote gate
// (plan §"Six design calls #2") then decides whether the file is
// promoted to a permanent wiki page or left for manual review.
//
// Stop-hook payload shape (best-effort): Claude Code documents an
// object with a `transcript` array of `{role, content}` messages, OR
// (more commonly in newer CLI versions) a `{"transcript_path": "..."}`
// pointer to an on-disk transcript JSON file. We accept either; we
// also tolerate empty stdin and malformed JSON by exiting 0 with a
// stderr WARN — the hook MUST NOT fail the user's session-end flow.
//
// Wiki-relevance heuristic (plan §6): keep assistant turns whose text
// references `LLMWIKI_DIR` or contains `llmwiki ` (the binary name with
// trailing space — i.e. invocations of the CLI). Everything else is
// dropped from the synthesized answer body. The most recent user
// message becomes the synthetic question.
package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mritunjaysharma394/llmwiki/internal/db"
	"github.com/mritunjaysharma394/llmwiki/internal/wiki"
	"github.com/spf13/cobra"
)

var captureSessionCmd = &cobra.Command{
	Use:   "capture-session",
	Short: "Read a Claude Code Stop-hook payload from stdin and file/promote a saved answer",
	Long: `Designed to be wired into Claude Code's Stop hook. Reads the session
JSON from stdin, extracts assistant turns that reference llmwiki,
files them to .llmwiki/answers/<ts>-session-<slug>.md, and runs the
auto-promote heuristic gate. On gate-pass + validator-pass the answer
is filed as a permanent wiki page; on gate-fail the file is left for
manual promote.

This command must NEVER fail the Stop hook — empty stdin, malformed
JSON, no wiki-relevant turns: all exit 0 with a stderr WARN.

Recipe (paste into ~/.claude/settings.json):

  { "hooks": { "Stop": [{ "command": "llmwiki capture-session" }] } }
`,
	RunE: runCaptureSession,
}

// captureSessionStdin lets tests inject a transcript without piping
// real stdin. Production reads os.Stdin directly.
var captureSessionStdin io.Reader = os.Stdin

// captureSessionPayload is the permissive shape we decode the
// Stop-hook JSON into. Unknown keys are ignored (encoding/json's
// default). Only the fields we actually use are listed; future hook
// versions can add fields without breaking us.
type captureSessionPayload struct {
	Transcript     []captureSessionMessage `json:"transcript"`
	TranscriptPath string                  `json:"transcript_path"`
}

// captureSessionMessage is one transcript turn. Content can arrive as
// a plain string OR as a structured array of content blocks (Claude
// Code's newer transcript format); we json.RawMessage and flatten in
// stringContent.
type captureSessionMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// stringContent returns the message's content as a plain string,
// flattening the structured-content variant by concatenating any
// `text` fields. Errors degrade silently to empty string — capture
// must never fail.
func (m captureSessionMessage) stringContent() string {
	if len(m.Content) == 0 {
		return ""
	}
	// Plain-string variant: "content": "hello"
	var s string
	if err := json.Unmarshal(m.Content, &s); err == nil {
		return s
	}
	// Structured variant: "content": [{"type":"text","text":"hello"}, ...]
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(m.Content, &blocks); err == nil {
		var sb strings.Builder
		for _, b := range blocks {
			if b.Text == "" {
				continue
			}
			if sb.Len() > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(b.Text)
		}
		return sb.String()
	}
	return ""
}

// runCaptureSession is the cobra entry point. The body is wrapped so
// any error path falls back to exit-0; cobra would otherwise render
// the error and return non-zero, which would cascade into the user's
// Stop hook chain and potentially block their next prompt.
func runCaptureSession(cmd *cobra.Command, _ []string) error {
	if err := captureSession(cmd, captureSessionStdin); err != nil {
		fmt.Fprintf(os.Stderr, "  WARN capture-session: %v\n", err)
	}
	return nil
}

// captureSession does the actual work and returns an error on any
// recoverable problem. The caller swallows it; errors from this
// function become stderr WARN lines, never non-zero exits.
func captureSession(cmd *cobra.Command, stdin io.Reader) error {
	raw, err := io.ReadAll(stdin)
	if err != nil {
		return fmt.Errorf("reading stdin: %w", err)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		// Empty stdin — perfectly valid (Claude Code's hook may fire
		// with no payload on quick sessions). Exit silently.
		return nil
	}
	payload, err := decodeCaptureSessionPayload(raw)
	if err != nil {
		return fmt.Errorf("decoding session payload: %w", err)
	}

	question, answer := extractWikiRelevantTurns(payload)
	if answer == "" {
		// No wiki-relevant turns; nothing to file. Silent.
		return nil
	}

	now := time.Now().UTC()
	wikiDir := cfg.Wiki.WikiDir
	answersDir := filepath.Join(filepath.Dir(wikiDir), "answers")
	if err := os.MkdirAll(answersDir, 0755); err != nil {
		return fmt.Errorf("mkdir answers dir: %w", err)
	}

	slug := slugify(question)
	if slug == "" {
		slug = "capture"
	}
	filename := fmt.Sprintf("%s-session-%s.md", now.Format("2006-01-02-150405"), slug)
	path := filepath.Join(answersDir, filename)

	body := wiki.FormatSavedAnswer(wiki.SavedAnswerInput{
		Question: question,
		Answer:   answer,
		Model:    "claude-code-session",
		Pages:    nil, // capture-session has no per-page evidence
		At:       now,
	})
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}

	// Best-effort: persist a saved_answers row so `llmwiki status` /
	// future maintain --promote-pending see the file. Failure is fine.
	_, _ = database.InsertSavedAnswer(db.SavedAnswer{
		Question:  question,
		Answer:    answer,
		Model:     "claude-code-session",
		FilePath:  path,
		CreatedAt: now,
	})

	// Run the auto-promote gate. Same shape as cmd/ask.go's
	// maybeAutoPromote, but with Source="session".
	parsed := wiki.ParsedSavedAnswer{
		Question:  question,
		Answer:    answer,
		Model:     "claude-code-session",
		CreatedAt: now,
	}
	apc := wiki.AutoPromoteConfig{
		HedgingPhrases: cfg.Ask.AutoPromoteHedgingPhrases,
		SkipScore:      cfg.Ask.AutoPromoteSkipScore,
		ScoreFloor:     cfg.Ask.AutoPromoteScoreFloor,
	}
	verdict, reason := wiki.EvaluateAutoPromote(parsed, database, apc)
	if !verdict.AutoPromote {
		fmt.Printf("→ saved to %s (%s)\n", path, reason)
		return nil
	}
	res, perr := wiki.PromoteAnswer(cmd.Context(), toWikiIngestConfig(cfg), database, llmClient, path, wiki.PromoteOptions{
		Schema: activeSchema,
		Source: "session",
	})
	if perr != nil {
		switch {
		case errors.Is(perr, wiki.ErrEvidenceInvalid):
			fmt.Printf("→ saved to %s (validator dropped quotes; promote_failed)\n", path)
		case errors.Is(perr, wiki.ErrTitleExists):
			fmt.Printf("→ saved to %s (title exists: %q; run `llmwiki promote --title <new> %s` to promote)\n",
				path, res.Title, filepath.Base(path))
		default:
			fmt.Fprintf(os.Stderr, "  WARN session auto-promote failed: %v\n", perr)
			fmt.Printf("→ saved to %s (auto-promote error)\n", path)
		}
		return nil
	}
	fmt.Printf("→ filed as [[%s]]\n", res.Title)
	return nil
}

// decodeCaptureSessionPayload reads `raw` as JSON, then resolves the
// transcript_path pointer if the inline transcript is empty. Errors on
// genuinely malformed JSON; an object with neither key present returns
// a payload whose Transcript is empty (the caller treats that as "no
// turns to capture").
func decodeCaptureSessionPayload(raw []byte) (captureSessionPayload, error) {
	var p captureSessionPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return p, err
	}
	if len(p.Transcript) > 0 {
		return p, nil
	}
	if p.TranscriptPath != "" {
		// Defensive: don't follow paths that don't exist; treat as empty.
		data, err := os.ReadFile(p.TranscriptPath)
		if err != nil {
			return p, nil
		}
		// The transcript file may be either an object with `transcript`
		// or a bare array of messages (different Claude Code versions
		// have shipped both shapes). Try both.
		var inner captureSessionPayload
		if err := json.Unmarshal(data, &inner); err == nil && len(inner.Transcript) > 0 {
			return inner, nil
		}
		var msgs []captureSessionMessage
		if err := json.Unmarshal(data, &msgs); err == nil {
			p.Transcript = msgs
		}
	}
	return p, nil
}

// extractWikiRelevantTurns walks the transcript, returning the most
// recent user message as the synthetic question and the concatenated
// wiki-relevant assistant turns as the answer body.
//
// Wiki-relevant ↔ contains "LLMWIKI_DIR" OR contains "llmwiki "
// (binary invocations). Case-sensitive on purpose — the env var name
// and the binary name are both lowercase / stable, and a wider match
// would dilute the signal with off-topic chat that happens to mention
// "wiki" in passing.
func extractWikiRelevantTurns(p captureSessionPayload) (question, answer string) {
	var lastUser string
	var relevantAssistant []string
	for _, m := range p.Transcript {
		text := m.stringContent()
		if text == "" {
			continue
		}
		switch m.Role {
		case "user":
			lastUser = text
		case "assistant":
			if strings.Contains(text, "LLMWIKI_DIR") || strings.Contains(text, "llmwiki ") {
				relevantAssistant = append(relevantAssistant, text)
			}
		}
	}
	if len(relevantAssistant) == 0 {
		return "", ""
	}
	return lastUser, strings.Join(relevantAssistant, "\n\n")
}
