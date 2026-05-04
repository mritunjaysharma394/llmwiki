// Package wiki — obsidian.go
//
// Three pure helpers that turn the directory of llmwiki-written Markdown
// pages into a first-class Obsidian vault without anyone building a UI.
//
//   - RewriteBareReferencesAsWikilinks: post-validation pass that wraps
//     bare prose mentions of known page titles in [[wikilink]] syntax,
//     skipping fenced code blocks, inline-backticked spans, and YAML
//     frontmatter. Idempotent (running twice is a no-op) and conservative
//     (case-sensitive, whole-word) — false negatives are fine, false
//     positives are not.
//
//   - RegenerateIndex: overwrites <wikiDir>/index.md with a deterministic,
//     sorted listing of every page plus a "By source" grouping. Output is
//     byte-identical for identical inputs and a fixed clock — the caller
//     passes time.Now().UTC() in production; tests pin a fixed time so
//     they can compare runs.
//
//   - AppendLog: append-only, RFC3339-UTC line per significant event.
//     Never rotated, never truncated by llmwiki. Phase F wires this into
//     ingest and ask success paths only — failures go to stderr.
package wiki

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/mritunjaysharma394/llmwiki/internal/db"
)

// LogEntry is one line in <wikiDir>/log.md. Timestamp is rendered as
// RFC3339 UTC; Kind is a short tag ("ingest" | "ask" | "mcp.write_page");
// Payload is a free-form one-liner the caller composes.
type LogEntry struct {
	At      time.Time
	Kind    string
	Payload string
}

// RewriteBareReferencesAsWikilinks does case-sensitive, whole-word
// substitution of every knownTitle into [[Title]] form, skipping fenced
// code blocks (```...```), inline-backticked spans (`...`) and YAML
// frontmatter (--- ... ---). Idempotent: a body that already contains
// [[Title]] is a no-op for that title. Longer titles are tried before
// shorter ones, so "Trust Property Validator" wins over "Trust Property"
// when both are known.
//
// In Phase F production callers pass the union of (existing wiki titles)
// and (new-in-this-batch titles). The validator is unchanged — wikilinks
// are body-quality, not a trust property — so a page whose body still
// mentions a stranger title in prose is not a validation error.
func RewriteBareReferencesAsWikilinks(body string, knownTitles []string) string {
	if len(knownTitles) == 0 || body == "" {
		return body
	}
	// Sort by descending length so "Trust Property Validator" gets a shot
	// before "Trust Property". Stable sort by length then lexicographic
	// keeps the ordering deterministic.
	titles := make([]string, len(knownTitles))
	copy(titles, knownTitles)
	sort.SliceStable(titles, func(i, j int) bool {
		if len(titles[i]) != len(titles[j]) {
			return len(titles[i]) > len(titles[j])
		}
		return titles[i] < titles[j]
	})

	// Pre-compile per-title \b<escaped>\b regexes once.
	patterns := make([]*regexp.Regexp, 0, len(titles))
	keep := make([]string, 0, len(titles))
	for _, t := range titles {
		if t == "" {
			continue
		}
		// Whole-word boundaries via \b. Title may contain spaces; \b at the
		// title's first/last char is what we want — the boundary is between
		// the outer letter/digit and the surrounding context.
		re, err := regexp.Compile(`\b` + regexp.QuoteMeta(t) + `\b`)
		if err != nil {
			continue
		}
		patterns = append(patterns, re)
		keep = append(keep, t)
	}
	if len(patterns) == 0 {
		return body
	}

	// Walk line by line, tracking fence state. Within an out-of-fence
	// line, mask inline-backtick runs before applying substitutions.
	lines := strings.Split(body, "\n")
	inFence := false
	inFrontmatter := false
	// Frontmatter is leading "---" followed by a closing "---" line. We
	// only honor it when it begins on the very first line.
	if len(lines) > 0 && lines[0] == "---" {
		inFrontmatter = true
	}
	for i, ln := range lines {
		if inFrontmatter {
			// Closing delimiter ends frontmatter; the closing line itself
			// is part of frontmatter and is left untouched.
			if i > 0 && ln == "---" {
				inFrontmatter = false
			}
			continue
		}
		// Fence open/close: a line starting with ``` (any language tag).
		trim := strings.TrimSpace(ln)
		if strings.HasPrefix(trim, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		lines[i] = rewriteLine(ln, patterns, keep)
	}
	return strings.Join(lines, "\n")
}

// rewriteLine applies the per-title regex substitutions to a single
// out-of-fence, out-of-frontmatter line, masking inline-backtick spans so
// matches inside `...` are skipped. We mask, substitute, then unmask —
// simpler and more robust than threading a "skip-range" set through the
// regex engine.
func rewriteLine(line string, patterns []*regexp.Regexp, titles []string) string {
	if !strings.ContainsAny(line, "`") {
		return rewriteOutsideBackticks(line, patterns, titles)
	}
	// Split on backticks; even-indexed segments are outside-backticks,
	// odd-indexed are inside-backticks. This is a single-line approximation
	// of inline `...` runs — adequate for the conservative "false negatives
	// fine, false positives not" stance.
	parts := strings.Split(line, "`")
	for i := range parts {
		if i%2 == 0 {
			parts[i] = rewriteOutsideBackticks(parts[i], patterns, titles)
		}
	}
	return strings.Join(parts, "`")
}

// rewriteOutsideBackticks runs the per-title substitutions over a span
// known to be outside any code fence and any inline-backtick run. Skips
// matches whose surrounding [[ ]] already wraps the title or wraps a
// longer wikilink that contains the title as a substring. Walking the
// matches with FindAllIndex (rather than ReplaceAllStringFunc) lets us
// peek at the bytes around each hit and decide per-match whether to
// rewrite — needed for both idempotency ([[Title]] already present) and
// longer-first correctness (a match inside an already-rewritten longer
// link must not be re-wrapped).
func rewriteOutsideBackticks(s string, patterns []*regexp.Regexp, titles []string) string {
	for i, re := range patterns {
		title := titles[i]
		bracketed := "[[" + title + "]]"
		var out strings.Builder
		out.Grow(len(s))
		last := 0
		idxs := re.FindAllStringIndex(s, -1)
		for _, idx := range idxs {
			start, end := idx[0], idx[1]
			// Skip if the match is already inside an existing wikilink:
			// either the immediate surroundings are [[...]] (idempotency)
			// or any earlier "[[" opens a span that hasn't been closed by
			// a "]]" before `start` (a longer-first wikilink that swallows
			// this title).
			if insideWikilink(s, start, end) {
				continue
			}
			out.WriteString(s[last:start])
			out.WriteString(bracketed)
			last = end
		}
		out.WriteString(s[last:])
		s = out.String()
	}
	return s
}

// insideWikilink reports whether the byte span [start,end) of s lies
// inside an existing [[...]] wikilink. True when the most recent "[["
// before `start` is not already closed by a "]]" before `start`, AND a
// "]]" appears at or after `end`.
func insideWikilink(s string, start, end int) bool {
	openIdx := strings.LastIndex(s[:start], "[[")
	if openIdx < 0 {
		return false
	}
	// Any "]]" between openIdx+2 and start means that "[[" was closed
	// before the match, so the match isn't inside it.
	if strings.Contains(s[openIdx+2:start], "]]") {
		return false
	}
	// And a "]]" must appear at or after end for this to be a real
	// enclosure (otherwise the "[[" was a stray, not a real wikilink).
	return strings.Contains(s[end:], "]]")
}

// RegenerateIndex overwrites <wikiDir>/index.md with a deterministic
// listing of every page (sorted by title) plus a "By source" grouping
// keyed by Source.URI. Frontmatter carries title=index, generated_at=
// <now in RFC3339 UTC>, generator=llmwiki, plus a comment line warning
// against manual edits.
//
// Idempotent for a fixed `now`: two consecutive calls with the same
// inputs produce byte-identical output. Production callers pass
// time.Now().UTC(); tests pin a known time so they can compare runs.
//
// Performance note (per spec): O(N log N) over `pages` from the sort,
// O(N) for the body. At 50k pages this is "noticeable but not prohibitive"
// in v1.1; we sort once and group once and call it done.
func RegenerateIndex(wikiDir string, pages []db.PageRecord, sources []db.Source, now time.Time) error {
	if err := os.MkdirAll(wikiDir, 0755); err != nil {
		return err
	}
	// Build ID → URI lookup once.
	uriByID := make(map[int64]string, len(sources))
	for _, s := range sources {
		uriByID[s.ID] = s.URI
	}

	// Sorted copy by title (case-sensitive, lexicographic).
	sorted := make([]db.PageRecord, len(pages))
	copy(sorted, pages)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Title < sorted[j].Title
	})

	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString("title: index\n")
	sb.WriteString(fmt.Sprintf("generated_at: %s\n", now.UTC().Format(time.RFC3339)))
	sb.WriteString("generator: llmwiki\n")
	sb.WriteString("# generated file — do not edit manually; regenerated by llmwiki on every ingest\n")
	sb.WriteString("---\n\n")

	sb.WriteString("# Wiki index\n\n")
	sb.WriteString(fmt.Sprintf("## Pages (%d)\n\n", len(sorted)))
	for _, p := range sorted {
		dateStr := p.UpdatedAt.UTC().Format("2006-01-02")
		sb.WriteString(fmt.Sprintf("- [[%s]] — updated %s\n", p.Title, dateStr))
	}
	sb.WriteString("\n## By source\n\n")

	// Group titles by source URI. A page can list multiple SourceIDs;
	// it appears under each. Pages with no SourceIDs land under a
	// synthetic "(no source)" header so they aren't silently dropped.
	groups := make(map[string][]string)
	for _, p := range sorted {
		if len(p.SourceIDs) == 0 {
			groups["(no source)"] = append(groups["(no source)"], p.Title)
			continue
		}
		for _, sid := range p.SourceIDs {
			uri := uriByID[sid]
			if uri == "" {
				uri = fmt.Sprintf("(source %d)", sid)
			}
			groups[uri] = append(groups[uri], p.Title)
		}
	}
	groupKeys := make([]string, 0, len(groups))
	for k := range groups {
		groupKeys = append(groupKeys, k)
	}
	sort.Strings(groupKeys)
	for _, k := range groupKeys {
		titles := groups[k]
		sort.Strings(titles)
		sb.WriteString(fmt.Sprintf("### %s\n", k))
		for _, t := range titles {
			sb.WriteString(fmt.Sprintf("- [[%s]]\n", t))
		}
		sb.WriteString("\n")
	}

	path := filepath.Join(wikiDir, "index.md")
	return os.WriteFile(path, []byte(sb.String()), 0644)
}

// AppendLog appends one timestamped line to <wikiDir>/log.md, creating
// the file (and the directory) on the first call. Format:
//
//	- 2026-05-04T14:30:12Z **ingest** ./README.md → 7 pages, 23 evidence quotes
//
// RFC3339 UTC with no fractional seconds is the only timestamp form so
// downstream tooling can parse the file unambiguously. Append-only —
// llmwiki never rotates or truncates this file.
//
// Goroutine-safety: AppendLog opens the file with O_APPEND and writes the
// full line in one Write call. POSIX guarantees O_APPEND atomicity for
// writes ≤ PIPE_BUF (typically 4 KiB on Darwin/Linux); a single log line
// is well under that, so concurrent callers (e.g. multiple MCP tool
// invocations) won't interleave bytes. We do not take a process-level
// lock here — Phase G's MCP handler can call AppendLog directly.
func AppendLog(wikiDir string, entry LogEntry) error {
	if err := os.MkdirAll(wikiDir, 0755); err != nil {
		return err
	}
	path := filepath.Join(wikiDir, "log.md")
	line := fmt.Sprintf("- %s **%s** %s\n",
		entry.At.UTC().Format(time.RFC3339), entry.Kind, entry.Payload)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(line)
	return err
}
