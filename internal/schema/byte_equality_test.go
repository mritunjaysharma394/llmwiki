// Package schema_test — byte_equality_test.go
//
// Phase B Task 4: byte-equality regression tests pinning v0.7's bundled
// default schema to v0.6's hard-coded prompt strings. The load-bearing
// contract: a v0.6 wiki opening under v0.7 with NO AGENTS.md sees zero
// behaviour change, because schema.Bundled().Render(Prompts.X, vars)
// produces the v0.6 hard-coded string byte-for-byte for all six prompt
// sites (Ingest, Ask, UpdateExisting, Contradiction, PromoteRewrite,
// LintContradictions).
//
// Cassettes recorded under v0.6 will continue to replay because the
// prompt strings reaching the LLM are byte-identical (spec risk #12).
//
// The wiki.*PromptForTests exports are the test-only seam that lets
// internal/schema_test reach back into internal/wiki for the v0.6
// const-string source of truth without coupling production internal/schema
// to internal/wiki.
package schema_test

import (
	"strings"
	"testing"

	"github.com/mritunjaysharma394/llmwiki/internal/schema"
	"github.com/mritunjaysharma394/llmwiki/internal/wiki"
)

// diff returns a small leading-context string describing where two
// strings first diverge so test failures point at the byte position
// rather than dumping two multi-kilobyte blobs.
func diff(got, want string) string {
	if got == want {
		return "(equal)"
	}
	n := len(got)
	if len(want) < n {
		n = len(want)
	}
	for i := 0; i < n; i++ {
		if got[i] != want[i] {
			start := i - 30
			if start < 0 {
				start = 0
			}
			return "first divergence at byte " + itoa(i) + ":\n  got:  " + quote(got[start:min(i+30, len(got))]) + "\n  want: " + quote(want[start:min(i+30, len(want))])
		}
	}
	if len(got) != len(want) {
		return "len mismatch: got=" + itoa(len(got)) + " want=" + itoa(len(want))
	}
	return "(equal but reported as different)"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	negative := false
	if n < 0 {
		negative = true
		n = -n
	}
	var digits [20]byte
	i := len(digits)
	for n > 0 {
		i--
		digits[i] = byte('0' + n%10)
		n /= 10
	}
	if negative {
		i--
		digits[i] = '-'
	}
	return string(digits[i:])
}

func quote(s string) string { return "`" + s + "`" }

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// formatExistingTitles is the bundled rendering of the existing-titles
// bullet list — mirrors what wiki callers will pass into Render at the
// {{existing_titles}} slot. Empty list renders to the v0.6 sentinel
// "(none yet)" so a fresh wiki produces the v0.6 prompt byte-for-byte.
func formatExistingTitles(titles []string) string {
	if len(titles) == 0 {
		return "(none yet)"
	}
	var sb strings.Builder
	for i, t := range titles {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString("- " + t)
	}
	return sb.String()
}

// TestBundledPrompts_ByteEqualV06_Ingest pins the bundled Ingest prompt
// to the v0.6 system+preamble byte string. v0.6 built the user prompt
// inline as `"Existing wiki pages (titles only):\n(none yet)\n\n...SOURCE..."`;
// option (a) from the plan hoists the "Existing wiki pages..." preamble
// into the schema-rendered system prompt so the user can rewrite that
// surrounding line if they want.
func TestBundledPrompts_ByteEqualV06_Ingest(t *testing.T) {
	s := schema.Bundled()
	got := s.Render(s.Prompts.Ingest, map[string]string{
		"domain":          s.Domain,
		"existing_titles": formatExistingTitles(nil),
	})
	want := wiki.IngestSystemPromptForTests() +
		"\n\nExisting wiki pages (titles only):\n(none yet)"
	if got != want {
		t.Fatalf("Ingest prompt drifted:\n%s\n--- want ---\n%q\n--- got ---\n%q",
			diff(got, want), want, got)
	}
}

// TestBundledPrompts_ByteEqualV06_Ask pins the bundled Ask prompt to
// the v0.6 answerSystemPrompt const. v0.6 had no domain context line;
// the bundled-default Domain is empty so {{domain}} substitutes to ""
// and the rendered prompt equals the v0.6 const verbatim.
func TestBundledPrompts_ByteEqualV06_Ask(t *testing.T) {
	s := schema.Bundled()
	got := s.Render(s.Prompts.Ask, map[string]string{
		"domain": s.Domain,
	})
	want := wiki.AnswerSystemPromptForTests()
	if got != want {
		t.Fatalf("Ask prompt drifted:\n%s\n--- want ---\n%q\n--- got ---\n%q",
			diff(got, want), want, got)
	}
}

// TestBundledPrompts_ByteEqualV06_UpdateExisting pins the bundled
// UpdateExisting prompt to v0.6's updateExistingSystemPrompt. v0.6's
// const did NOT include the "EXISTING PAGE BODY" / "EXISTING EVIDENCE"
// blocks — those were built inline in buildUpdatePromptUser as part of
// the user prompt — so under option (a) the schema-rendered system
// prompt only contains v0.6's const verbatim with {{domain}},
// {{existing_page_body}}, {{existing_evidence}} positioned to render to
// empty.
func TestBundledPrompts_ByteEqualV06_UpdateExisting(t *testing.T) {
	s := schema.Bundled()
	got := s.Render(s.Prompts.UpdateExisting, map[string]string{
		"domain":             s.Domain,
		"existing_page_body": "",
		"existing_evidence":  "",
	})
	want := wiki.UpdateExistingSystemPromptForTests()
	if got != want {
		t.Fatalf("UpdateExisting prompt drifted:\n%s\n--- want ---\n%q\n--- got ---\n%q",
			diff(got, want), want, got)
	}
}

// TestBundledPrompts_ByteEqualV06_Contradiction pins the bundled
// Contradiction prompt to v0.6's contradictionSystemPrompt const. The
// per-pair prompt has no required placeholders.
func TestBundledPrompts_ByteEqualV06_Contradiction(t *testing.T) {
	s := schema.Bundled()
	got := s.Render(s.Prompts.Contradiction, nil)
	want := wiki.ContradictionSystemPromptForTests()
	if got != want {
		t.Fatalf("Contradiction prompt drifted:\n%s\n--- want ---\n%q\n--- got ---\n%q",
			diff(got, want), want, got)
	}
}

// TestBundledPrompts_ByteEqualV06_PromoteRewrite pins the bundled
// PromoteRewrite prompt to v0.6's inline-then-hoisted
// promoteRewriteSystemPrompt const. v0.6 built the {{question}},
// {{answer_body}}, {{evidence_quotes}} body inline as the USER prompt;
// option (a) hoists the prompt body into the schema-rendered system
// prompt, with the placeholders rendering to empty under bundled
// defaults.
func TestBundledPrompts_ByteEqualV06_PromoteRewrite(t *testing.T) {
	s := schema.Bundled()
	got := s.Render(s.Prompts.PromoteRewrite, map[string]string{
		"question":        "",
		"answer_body":     "",
		"evidence_quotes": "",
	})
	want := wiki.PromoteRewriteSystemPromptForTests()
	if got != want {
		t.Fatalf("PromoteRewrite prompt drifted:\n%s\n--- want ---\n%q\n--- got ---\n%q",
			diff(got, want), want, got)
	}
}

// TestBundledPrompts_ByteEqualV06_LintContradictions pins the bundled
// LintContradictions prompt to the v0.6 inline lint prompt that was
// hoisted to a const in Phase B Task 4. No required placeholders.
func TestBundledPrompts_ByteEqualV06_LintContradictions(t *testing.T) {
	s := schema.Bundled()
	got := s.Render(s.Prompts.LintContradictions, nil)
	want := wiki.LintContradictionsSystemPromptForTests()
	if got != want {
		t.Fatalf("LintContradictions prompt drifted:\n%s\n--- want ---\n%q\n--- got ---\n%q",
			diff(got, want), want, got)
	}
}
