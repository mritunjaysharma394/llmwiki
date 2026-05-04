package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/mritunjaysharma394/llmwiki/internal/db"
	"github.com/mritunjaysharma394/llmwiki/internal/wiki"
)

// errorResult builds a CallToolResult with IsError=true whose text content is
// a JSON-encoded {code, message, ...extra} payload. MCP clients that surface
// IsError can render the structured fields machine-readably; raw text-only
// clients still see a stable JSON string.
func errorResult(code, msg string, extra map[string]any) *mcpgo.CallToolResult {
	payload := map[string]any{"code": code, "message": msg}
	for k, v := range extra {
		payload[k] = v
	}
	b, _ := json.Marshal(payload)
	return mcpgo.NewToolResultError(string(b))
}

// jsonResult wraps NewToolResultText with a marshaled body. Centralised so
// every read-only handler emits the same content shape (text content with a
// JSON object) and we can swap to NewToolResultStructured later without
// chasing call sites.
func jsonResult(payload any) (*mcpgo.CallToolResult, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return errorResult("internal", "marshal response: "+err.Error(), nil), nil
	}
	return mcpgo.NewToolResultText(string(b)), nil
}

// ----- list_pages ---------------------------------------------------------

func listPagesHandler(d Deps) mcpsrv.ToolHandlerFunc {
	return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		prefix := strings.TrimSpace(req.GetString("prefix", ""))
		limit := req.GetInt("limit", 50)
		if limit <= 0 {
			limit = 50
		}

		pages, err := d.DB.AllPages()
		if err != nil {
			return errorResult("db_error", "loading pages: "+err.Error(), nil), nil
		}
		out := make([]map[string]any, 0, len(pages))
		for _, p := range pages {
			if prefix != "" && !strings.HasPrefix(p.Title, prefix) {
				continue
			}
			files := sourceFilesForPage(d.DB, p)
			out = append(out, map[string]any{
				"title":        p.Title,
				"path":         p.Path,
				"updated_at":   p.UpdatedAt,
				"source_files": files,
			})
			if len(out) >= limit {
				break
			}
		}
		return jsonResult(map[string]any{"pages": out})
	}
}

// ----- read_page ----------------------------------------------------------

func readPageHandler(d Deps) mcpsrv.ToolHandlerFunc {
	return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		title, err := req.RequireString("title")
		if err != nil {
			return errorResult("bad_request", err.Error(), nil), nil
		}
		page, err := d.DB.GetPage(title)
		if err != nil {
			return errorResult("db_error", "loading page: "+err.Error(), nil), nil
		}
		if page == nil {
			return errorResult("not_found", fmt.Sprintf("no page titled %q", title), map[string]any{"title": title}), nil
		}
		evRows, err := d.DB.GetEvidenceForPage(page.ID)
		if err != nil {
			return errorResult("db_error", "loading evidence: "+err.Error(), nil), nil
		}
		sfPaths := sourceFilePathLookup(d.DB, page.SourceIDs)

		evidence := make([]map[string]any, 0, len(evRows))
		for _, e := range evRows {
			path := ""
			if e.SourceFileID != nil {
				path = sfPaths[*e.SourceFileID]
			}
			evidence = append(evidence, map[string]any{
				"quote":       e.Quote,
				"line_start":  e.LineStart,
				"line_end":    e.LineEnd,
				"source_file": path,
			})
		}

		// Links: db.DB exposes UpsertLinks but no GetLinks for v1; the
		// authoritative source on disk is the page's frontmatter. Reading
		// page.Path and re-parsing the YAML is cheap (<1ms for typical
		// pages) and the result mirrors what `read_page` would render in
		// any other surface. Empty array on miss keeps the JSON shape
		// stable for clients that always iterate links.
		linkOut := make([]map[string]any, 0)
		if page.Path != "" {
			if data, err := os.ReadFile(page.Path); err == nil {
				if parsed, err := wiki.ParsePage(string(data)); err == nil {
					for _, l := range parsed.Links {
						linkOut = append(linkOut, map[string]any{"to": l.To, "type": l.Type})
					}
				}
			}
		}

		sourceFiles := distinctSourceFilePaths(evRows, sfPaths)

		return jsonResult(map[string]any{
			"title":        page.Title,
			"path":         page.Path,
			"body":         page.Body,
			"updated_at":   page.UpdatedAt,
			"content_hash": page.ContentHash,
			"evidence":     evidence,
			"links":        linkOut,
			"source_files": sourceFiles,
		})
	}
}

// ----- lint ---------------------------------------------------------------

// LintResult mirrors the human-readable report cmd/lint emits. The handler
// renders it back into a single text content; cmd/lint.go keeps its own copy
// of the print loop for Phase F-style backwards compat. Extracting the Stale
// + Contradictions data here (rather than capturing stdout) keeps the MCP
// surface free of stray spinner / color output.
type LintResult struct {
	Stale          []string
	Contradictions []string
}

func lintHandler(d Deps) mcpsrv.ToolHandlerFunc {
	return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		res, err := runLintInternal(ctx, d)
		if err != nil {
			return errorResult("lint_failed", err.Error(), nil), nil
		}
		var sb strings.Builder
		sb.WriteString("=== Staleness Check ===\n")
		if len(res.Stale) == 0 {
			sb.WriteString("  All sources up to date.\n")
		} else {
			for _, s := range res.Stale {
				sb.WriteString("  STALE: " + s + "\n")
			}
		}
		sb.WriteString("\n=== Contradiction Check ===\n")
		if len(res.Contradictions) == 0 {
			sb.WriteString("  Not enough pages to check for contradictions.\n")
		} else {
			for _, c := range res.Contradictions {
				sb.WriteString(c)
				if !strings.HasSuffix(c, "\n") {
					sb.WriteString("\n")
				}
			}
		}
		return mcpgo.NewToolResultText(sb.String()), nil
	}
}

// runLintInternal is the package-internal twin of cmd.runLint that returns a
// structured LintResult instead of printing. Kept here (rather than in
// cmd/lint.go) because internal/mcp must not import cmd — that direction
// would create an import cycle through cmd/root.go's globals. cmd/lint.go
// continues to print via its own loop; the duplication is small and the two
// paths diverge anyway (TTY spinners vs MCP serialization).
func runLintInternal(ctx context.Context, d Deps) (LintResult, error) {
	var out LintResult
	sources, err := d.DB.GetAllSources()
	if err != nil {
		return out, fmt.Errorf("loading sources: %w", err)
	}
	for _, s := range sources {
		current, err := currentHash(s.URI)
		if err != nil {
			// Unreachable URIs are treated as unknown rather than stale; the
			// CLI surface logs a WARN line there. MCP clients receive the
			// staleness list only; transient fetch errors are silently
			// skipped to keep responses deterministic.
			continue
		}
		if current != s.ContentHash {
			out.Stale = append(out.Stale, s.URI)
		}
	}

	records, err := d.DB.AllPages()
	if err != nil {
		return out, fmt.Errorf("loading pages: %w", err)
	}
	if len(records) < 2 {
		return out, nil
	}
	pages := make([]wiki.Page, 0, len(records))
	for _, r := range records {
		pages = append(pages, wiki.Page{Title: r.Title, Body: r.Body})
	}
	const batchSize = 10
	for i := 0; i < len(pages); i += batchSize {
		end := i + batchSize
		if end > len(pages) {
			end = len(pages)
		}
		batch := pages[i:end]
		result, err := wiki.DetectContradictions(ctx, d.Client, batch)
		if err != nil {
			out.Contradictions = append(out.Contradictions, fmt.Sprintf("  WARN: contradiction check failed: %v", err))
			continue
		}
		out.Contradictions = append(out.Contradictions, result)
	}
	return out, nil
}

// currentHash mirrors cmd.currentHash. Duplicated here for the same reason
// as runLintInternal — internal/mcp must not import cmd.
func currentHash(uri string) (string, error) {
	var data []byte
	var err error
	switch {
	case strings.HasPrefix(uri, "http://") || strings.HasPrefix(uri, "https://"):
		resp, herr := http.Get(uri)
		if herr != nil {
			return "", herr
		}
		defer resp.Body.Close()
		data, err = io.ReadAll(resp.Body)
		if err != nil {
			return "", err
		}
	default:
		data, err = os.ReadFile(uri)
		if err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

// ----- ask ----------------------------------------------------------------

func askHandler(d Deps) mcpsrv.ToolHandlerFunc {
	return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		question, err := req.RequireString("question")
		if err != nil {
			return errorResult("bad_request", err.Error(), nil), nil
		}

		// Page selection follows cmd/ask.go: FTS over pages and evidence,
		// fall back to AllPages() when the FTS index is empty / errors out.
		pageHits, err := d.DB.SearchPages(question, 5)
		if err != nil {
			pageHits, _ = d.DB.AllPages()
			if len(pageHits) > 5 {
				pageHits = pageHits[:5]
			}
		}
		evHits, _ := d.DB.SearchEvidence(question, 10)

		type bundle struct {
			page     db.PageRecord
			evidence []db.Evidence
		}
		bundles := map[int64]*bundle{}
		var order []int64
		for _, p := range pageHits {
			bundles[p.ID] = &bundle{page: p}
			order = append(order, p.ID)
		}
		for _, h := range evHits {
			if _, ok := bundles[h.PageID]; !ok {
				p, _ := d.DB.GetPageByID(h.PageID)
				if p == nil {
					continue
				}
				bundles[h.PageID] = &bundle{page: *p}
				order = append(order, h.PageID)
			}
			bundles[h.PageID].evidence = append(bundles[h.PageID].evidence, h.Evidence)
		}
		if len(bundles) == 0 {
			all, err := d.DB.AllPages()
			if err != nil {
				return errorResult("db_error", "loading pages: "+err.Error(), nil), nil
			}
			if len(all) == 0 {
				return errorResult("empty_wiki", "no pages in wiki — run llmwiki ingest <source> first", nil), nil
			}
			if len(all) > 5 {
				all = all[:5]
			}
			for _, p := range all {
				bundles[p.ID] = &bundle{page: p}
				order = append(order, p.ID)
			}
		}

		// Resolve source_file_id -> relative path for every backing source.
		sourceFilePaths := map[int64]string{}
		seenSrc := map[int64]bool{}
		for _, id := range order {
			b := bundles[id]
			for _, sid := range b.page.SourceIDs {
				if seenSrc[sid] {
					continue
				}
				seenSrc[sid] = true
				files, err := d.DB.GetSourceFiles(sid)
				if err != nil {
					continue
				}
				for _, f := range files {
					sourceFilePaths[f.ID] = f.RelativePath
				}
			}
		}

		var pages []wiki.Page
		type sourceCit struct {
			PageTitle  string `json:"page_title"`
			Quote      string `json:"quote"`
			SourceFile string `json:"source_file"`
			LineStart  int    `json:"line_start"`
			LineEnd    int    `json:"line_end"`
		}
		var citations []sourceCit
		for _, id := range order {
			b := bundles[id]
			ev := b.evidence
			if len(ev) == 0 {
				dbEv, _ := d.DB.GetEvidenceForPage(b.page.ID)
				if len(dbEv) > 3 {
					dbEv = dbEv[:3]
				}
				ev = dbEv
			}
			pageEv := make([]wiki.Evidence, 0, len(ev))
			for _, e := range ev {
				path := ""
				if e.SourceFileID != nil {
					path = sourceFilePaths[*e.SourceFileID]
				}
				pageEv = append(pageEv, wiki.Evidence{
					Quote:          e.Quote,
					LineStart:      e.LineStart,
					LineEnd:        e.LineEnd,
					SourceFilePath: path,
				})
				citations = append(citations, sourceCit{
					PageTitle:  b.page.Title,
					Quote:      e.Quote,
					SourceFile: path,
					LineStart:  e.LineStart,
					LineEnd:    e.LineEnd,
				})
			}
			pages = append(pages, wiki.Page{
				Title:    b.page.Title,
				Body:     b.page.Body,
				Evidence: pageEv,
			})
		}

		answer, err := wiki.AnswerQuestion(ctx, d.Client, question, pages)
		if err != nil {
			return errorResult("llm_error", "answering question: "+err.Error(), nil), nil
		}
		return jsonResult(map[string]any{
			"answer":  answer,
			"sources": citations,
		})
	}
}

// ----- write_page (stub, replaced in Phase G2) ---------------------------

func writePageHandler(d Deps) mcpsrv.ToolHandlerFunc {
	return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		return mcpgo.NewToolResultError("write_page not yet implemented in this phase (Phase G2)"), nil
	}
}

// ----- ingest (stub, replaced in Phase G2) -------------------------------

func ingestHandler(d Deps) mcpsrv.ToolHandlerFunc {
	return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		return mcpgo.NewToolResultError("ingest not yet implemented in this phase (Phase G2)"), nil
	}
}

// ----- helpers -----------------------------------------------------------

// sourceFilesForPage returns the distinct relative paths of source_files that
// back any evidence row on this page. Fast path for legacy pages without
// source_files: returns an empty slice rather than nil so the JSON shape
// stays stable.
func sourceFilesForPage(d *db.DB, p db.PageRecord) []string {
	out := []string{}
	ev, err := d.GetEvidenceForPage(p.ID)
	if err != nil || len(ev) == 0 {
		return out
	}
	lookup := sourceFilePathLookup(d, p.SourceIDs)
	return distinctSourceFilePaths(ev, lookup)
}

func sourceFilePathLookup(d *db.DB, sourceIDs []int64) map[int64]string {
	out := map[int64]string{}
	for _, sid := range sourceIDs {
		files, err := d.GetSourceFiles(sid)
		if err != nil {
			continue
		}
		for _, f := range files {
			out[f.ID] = f.RelativePath
		}
	}
	return out
}

func distinctSourceFilePaths(ev []db.Evidence, lookup map[int64]string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, e := range ev {
		if e.SourceFileID == nil {
			continue
		}
		p, ok := lookup[*e.SourceFileID]
		if !ok || p == "" {
			continue
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}
