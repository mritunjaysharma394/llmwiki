package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"

	"github.com/mritunjaysharma394/llmwiki/internal/db"
	"github.com/mritunjaysharma394/llmwiki/internal/ingest"
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

// ----- write_page ---------------------------------------------------------
//
// write_page is the load-bearing MCP tool. Every page-write driven over
// MCP funnels through this handler, which in turn funnels through
// wiki.ValidateAndAttachEvidence — the same trust-property validator
// cmd/ingest uses. Quotes that don't byte-exactly substring-match the
// named source_file are rejected with a structured error and never
// reach disk or DB. Title collisions, missing evidence, and ingest-not-
// done sources all return their own structured-error codes so the
// agent can react programmatically.
//
// log.md only records validated, written pages. Failed write_page
// calls go to stderr (and the structured error payload returned to the
// client) — never to log.md. Spec §risks calls this out as the
// denial-of-evidence vector mitigation.
//
// Return JSON shape on success (sub-project 6a / v1.2.0):
//
//	{
//	  "title":              string,    // resolved page title
//	  "path":               string,    // absolute on-disk path
//	  "evidence_quotes":    int,       // surviving validated quotes
//	  "sources":            []string,  // distinct source_file paths
//	  "retro_linked_pages": int,       // sub-project 6a (Phase D): count of
//	                                   //   existing pages whose body was
//	                                   //   rewritten to include [[NewTitle]]
//	                                   //   for this new title (body-only,
//	                                   //   idempotent; evidence rows
//	                                   //   untouched).
//	}
//
// Structured errors: title_exists (existing_path), evidence_required,
// evidence_invalid (dropped, hint), source_not_ingested (source_file),
// source_not_readable (source_file, source_uri), write_failed.

func writePageHandler(d Deps) mcpsrv.ToolHandlerFunc {
	return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		title, err := req.RequireString("title")
		if err != nil {
			return errorResult("bad_request", err.Error(), nil), nil
		}
		body, err := req.RequireString("body")
		if err != nil {
			return errorResult("bad_request", err.Error(), nil), nil
		}

		args := req.GetArguments()

		// 1. Title collision: refuse early so we never silently overwrite an
		//    existing page. The agent must call read_page + supersede via
		//    links if it wants to replace prior content (resolves spec open
		//    question 3).
		if existing, err := d.DB.GetPage(title); err != nil {
			return errorResult("db_error", "checking existing page: "+err.Error(), nil), nil
		} else if existing != nil {
			return errorResult("title_exists",
				fmt.Sprintf("a page titled %q already exists", title),
				map[string]any{"existing_path": existing.Path}), nil
		}

		// 2. Parse evidence. Schema requires at least one entry; we still
		//    re-check defensively in case a client bypasses the schema or
		//    the schema isn't enforced server-side.
		evRaw, _ := args["evidence"].([]any)
		if len(evRaw) == 0 {
			return errorResult("evidence_required",
				"write_page requires at least one evidence quote", nil), nil
		}

		type evidenceInput struct {
			Quote      string
			SourceFile string
		}
		var inputs []evidenceInput
		for _, it := range evRaw {
			m, ok := it.(map[string]any)
			if !ok {
				continue
			}
			q, _ := m["quote"].(string)
			sf, _ := m["source_file"].(string)
			inputs = append(inputs, evidenceInput{Quote: q, SourceFile: sf})
		}
		if len(inputs) == 0 {
			return errorResult("evidence_required",
				"write_page requires at least one well-formed evidence entry (each with quote + source_file)", nil), nil
		}

		// 3. Resolve every named source_file against the DB. Build a
		//    synthetic []ingest.SourceFile from the resolved rows so
		//    ValidateAndAttachEvidence sees the same shape it sees during
		//    ingest. Sources can live under cfg.RawDir/<sourceURI>/<rel>
		//    or be re-readable from the original URI; v1.1 reads the
		//    canonical content from db (we don't currently snapshot
		//    source_file content into the DB, so we rely on the URI being
		//    a path that's still readable from disk).
		sources, err := d.DB.GetAllSources()
		if err != nil {
			return errorResult("db_error", "loading sources: "+err.Error(), nil), nil
		}
		// Build a path → (source URI, source_file row) lookup from
		// every ingested source_file, indexed by RelativePath.
		type sfRef struct {
			sourceURI string
			file      db.SourceFile
		}
		byPath := map[string]sfRef{}
		for _, s := range sources {
			files, err := d.DB.GetSourceFiles(s.ID)
			if err != nil {
				continue
			}
			for _, f := range files {
				if _, taken := byPath[f.RelativePath]; taken {
					continue
				}
				byPath[f.RelativePath] = sfRef{sourceURI: s.URI, file: f}
			}
		}

		// Resolve named source_files and build the ingest.SourceFile[]
		// the validator wants. Read content fresh from disk under the
		// source URI; if the source URI isn't a local path we currently
		// cannot reconstruct content, so the agent must re-ingest first
		// (this matches the spec's "MCP needs an already-ingested
		// source" assumption).
		fileSet := map[string]ingest.SourceFile{}
		for _, in := range inputs {
			ref, ok := byPath[in.SourceFile]
			if !ok {
				return errorResult("source_not_ingested",
					fmt.Sprintf("source_file %q has no source_files row; run llmwiki ingest first", in.SourceFile),
					map[string]any{"source_file": in.SourceFile}), nil
			}
			if _, dup := fileSet[in.SourceFile]; dup {
				continue
			}
			content, err := wiki.ReadSourceFileContent(ref.sourceURI, ref.file.RelativePath)
			if err != nil {
				return errorResult("source_not_readable",
					fmt.Sprintf("cannot read source_file %q from source %q: %v", in.SourceFile, ref.sourceURI, err),
					map[string]any{"source_file": in.SourceFile, "source_uri": ref.sourceURI}), nil
			}
			fileSet[in.SourceFile] = ingest.NewSourceFile(in.SourceFile, content)
		}
		ingestFiles := make([]ingest.SourceFile, 0, len(fileSet))
		for _, f := range fileSet {
			ingestFiles = append(ingestFiles, f)
		}

		// 4. Run the validator. Build a synthetic Page with the agent-
		//    supplied evidence; ValidateAndAttachEvidence drops anything
		//    that fails the byte-exact substring match and returns the
		//    surviving quotes plus computed line ranges.
		linkInputs, _ := args["links"].([]any)
		var linkObjs []wiki.Link
		for _, l := range linkInputs {
			lm, ok := l.(map[string]any)
			if !ok {
				continue
			}
			to, _ := lm["to"].(string)
			typ, _ := lm["type"].(string)
			if to == "" {
				continue
			}
			linkObjs = append(linkObjs, wiki.Link{To: to, Type: typ})
		}

		candidate := wiki.Page{
			Title: title,
			Body:  body,
			Links: linkObjs,
		}
		for _, in := range inputs {
			candidate.Evidence = append(candidate.Evidence, wiki.Evidence{
				Quote:          in.Quote,
				SourceFilePath: in.SourceFile,
			})
		}

		validated, _ := wiki.ValidateAndAttachEvidence([]wiki.Page{candidate}, ingestFiles)
		if len(validated) == 0 {
			// Reconstruct the dropped report: every input quote that didn't
			// byte-match its named source_file. We compute this directly
			// from the inputs (the validator already logged warnings to
			// stderr) so the structured error payload tells the agent
			// which quotes failed and why.
			dropped := make([]map[string]any, 0, len(inputs))
			for _, in := range inputs {
				f, ok := fileSet[in.SourceFile]
				switch {
				case !ok:
					dropped = append(dropped, map[string]any{
						"quote":       in.Quote,
						"source_file": in.SourceFile,
						"reason":      "source_file not in resolved set",
					})
				case !strings.Contains(f.Content, in.Quote):
					dropped = append(dropped, map[string]any{
						"quote":       in.Quote,
						"source_file": in.SourceFile,
						"reason":      "quote not a byte-exact substring of the named source_file",
					})
				}
			}
			return errorResult("evidence_invalid",
				"every evidence quote failed validation; nothing was written",
				map[string]any{
					"dropped": dropped,
					"hint":    "quotes must be byte-exact substrings of an already-ingested source_file's content",
				}), nil
		}
		page := validated[0]

		// 5. Stamp tags / sources / created and run the wikilink rewrite.
		//    SourceIDs is the union of every source backing the surviving
		//    evidence — for write_page that's typically one entry per
		//    distinct source URI, deduped via the sfRef lookup above.
		srcIDSet := map[int64]struct{}{}
		var srcIDs []int64
		for _, e := range page.Evidence {
			ref, ok := byPath[e.SourceFilePath]
			if !ok {
				continue
			}
			if _, dup := srcIDSet[ref.file.SourceID]; dup {
				continue
			}
			srcIDSet[ref.file.SourceID] = struct{}{}
			srcIDs = append(srcIDs, ref.file.SourceID)
		}
		now := time.Now().UTC()
		page.SourceIDs = srcIDs
		page.UpdatedAt = now
		if page.Created.IsZero() {
			page.Created = now
		}
		page.ContentHash = wiki.HashContent(page.Body)
		page.Tags = []string{"llmwiki", "mcp.write_page"}
		// Sources: distinct source_file paths from the surviving evidence.
		seen := map[string]struct{}{}
		var sourcesList []string
		for _, e := range page.Evidence {
			if e.SourceFilePath == "" {
				continue
			}
			if _, dup := seen[e.SourceFilePath]; dup {
				continue
			}
			seen[e.SourceFilePath] = struct{}{}
			sourcesList = append(sourcesList, e.SourceFilePath)
		}
		page.Sources = sourcesList

		// Wikilink rewrite: union of existing wiki titles + this new page's
		// title. Matches what cmd/ingest does for ingested pages.
		titles, err := d.DB.AllPageTitles()
		if err != nil {
			return errorResult("db_error", "loading page titles: "+err.Error(), nil), nil
		}
		titles = append(titles, page.Title)
		page.Body = wiki.RewriteBareReferencesAsWikilinks(page.Body, titles)

		// 6. Disk + DB writes. Errors here surface as code: "write_failed"
		//    so the agent can distinguish trust-validator rejection from
		//    operational issues (full disk, db locked, etc).
		if err := os.MkdirAll(d.Cfg.WikiDir, 0755); err != nil {
			return errorResult("write_failed", "mkdir wiki dir: "+err.Error(), nil), nil
		}
		path := wiki.PagePath(d.Cfg.WikiDir, page.Title)
		if err := wiki.WritePage(page, d.Cfg.WikiDir); err != nil {
			return errorResult("write_failed", "writing page: "+err.Error(), nil), nil
		}
		rec := db.PageRecord{
			Title:       page.Title,
			Path:        path,
			Body:        page.Body,
			ContentHash: page.ContentHash,
			SourceIDs:   page.SourceIDs,
		}
		if err := d.DB.UpsertPage(rec); err != nil {
			return errorResult("write_failed", "db upsert: "+err.Error(), nil), nil
		}
		stored, err := d.DB.GetPage(page.Title)
		if err != nil || stored == nil {
			return errorResult("write_failed", "re-fetching written page", nil), nil
		}

		// Map evidence rows back to source_file IDs / source IDs so
		// InsertEvidence FKs resolve. Group by source so InsertEvidence
		// (which takes one sourceID per call) is invoked once per
		// distinct source.
		evBySource := map[int64][]db.Evidence{}
		for _, e := range page.Evidence {
			ref, ok := byPath[e.SourceFilePath]
			if !ok {
				continue
			}
			sfid := ref.file.ID
			evBySource[ref.file.SourceID] = append(evBySource[ref.file.SourceID], db.Evidence{
				Quote:        e.Quote,
				LineStart:    e.LineStart,
				LineEnd:      e.LineEnd,
				SourceFileID: &sfid,
			})
		}
		evidenceCount := 0
		for sid, items := range evBySource {
			if err := d.DB.InsertEvidence(stored.ID, sid, items); err != nil {
				return errorResult("write_failed", "insert evidence: "+err.Error(), nil), nil
			}
			evidenceCount += len(items)
		}

		// Links table.
		if len(page.Links) > 0 {
			var links []db.Link
			for _, l := range page.Links {
				links = append(links, db.Link{FromPage: page.Title, ToPage: l.To, LinkType: l.Type})
			}
			_ = d.DB.UpsertLinks(page.Title, links)
		}

		// Phase D (sub-project 6a): retro-link existing pages to the new
		// title. Body-only, idempotent; evidence rows untouched. Runs
		// BEFORE RegenerateIndex so index.md reflects the bumped
		// updated_at on any rewritten existing pages. Failures go to
		// stderr and don't undo the disk write.
		retroRes, rlErr := wiki.RetroLinkPages(d.DB, d.Cfg.WikiDir, []string{page.Title})
		if rlErr != nil {
			fmt.Fprintf(os.Stderr, "  WARN retro-linking existing pages after mcp.write_page: %v\n", rlErr)
		}

		// 7. Side files: regenerate index, append log. Best-effort —
		//    failures go to stderr and don't undo the disk write.
		allPageRecs, err := d.DB.AllPages()
		if err == nil {
			allSources, _ := d.DB.GetAllSources()
			if rerr := wiki.RegenerateIndex(d.Cfg.WikiDir, allPageRecs, allSources, time.Now().UTC()); rerr != nil {
				fmt.Fprintf(os.Stderr, "  WARN regenerating index.md after mcp.write_page: %v\n", rerr)
			}
		} else {
			fmt.Fprintf(os.Stderr, "  WARN reading pages for index after mcp.write_page: %v\n", err)
		}
		_ = wiki.AppendLog(d.Cfg.WikiDir, wiki.LogEntry{
			At:      time.Now().UTC(),
			Kind:    "mcp.write_page",
			Payload: fmt.Sprintf("%s → %d evidence quotes", page.Title, evidenceCount),
		})

		return jsonResult(map[string]any{
			"title":              page.Title,
			"path":               path,
			"evidence_quotes":    evidenceCount,
			"sources":            sourcesList,
			"retro_linked_pages": len(retroRes.UpdatedTitles),
		})
	}
}

// readSourceFileContent moved to internal/wiki/promote.go (v1.2 phase B,
// task 2). Both this handler and wiki.PromoteAnswer share the same
// implementation; the wiki package owns it because internal/mcp already
// imports internal/wiki and the reverse would form a cycle.

// ----- ingest -------------------------------------------------------------
//
// ingestHandler delegates to wiki.IngestSource — the same callable
// cmd/ingest's runIngest wraps. internal/mcp deliberately does not
// import cmd/, so the runner's lifted-out form (Task 11 step 3) is
// what makes this handler implementable. Logger is io.Discard so
// progress output doesn't pollute the JSON-RPC stdout channel; errors
// surface in the structured response.
//
// Return JSON shape on success (sub-project 6a / v1.2.0):
//
//	{
//	  "source":                 string,
//	  "pages_written":          int,
//	  "evidence_quotes":        int,
//	  "dropped_pages":          int,
//	  "skipped":                bool,
//	  "retro_linked_pages":     int,    // sub-project 6a Phase D
//	  "contradictions_flagged": int,    // sub-project 6a Phase E
//	}
//
// retro_linked_pages counts existing pages whose body was rewritten to
// include [[NewTitle]] for any of the new-this-batch titles (body-only,
// idempotent; evidence rows untouched). contradictions_flagged counts
// (newPage, existingPage) tuples where the contradiction-detection LLM
// call returned a direct factual contradiction backed by validated
// quotes on both sides; details append to <wikiDir>/contradictions.md.
// Both counters are informational — they never block the ingest write.
//
// Structured errors: bad_request, ingest_failed.

func ingestHandler(d Deps) mcpsrv.ToolHandlerFunc {
	return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		source, err := req.RequireString("source")
		if err != nil {
			return errorResult("bad_request", err.Error(), nil), nil
		}
		opts := wiki.IngestOptions{
			Force:   req.GetBool("force", false),
			Feed:    req.GetBool("feed", false),
			Sitemap: req.GetBool("sitemap", false),
			// Logger left nil → io.Discard inside IngestSource.
		}
		if mp := req.GetInt("max_pages", 0); mp > 0 {
			opts.MaxPages = mp
		}
		// MCP's slim cfg only carries WikiDir/RawDir/DBPath; fall back
		// to the runner's package defaults for everything else. The CLI
		// keeps its richer Config by going through cmd's runIngest.
		wcfg := wiki.IngestSourceConfig{
			WikiDir:          d.Cfg.WikiDir,
			RawDir:           d.Cfg.RawDir,
			RespectGitignore: true,
		}
		res, err := wiki.IngestSource(ctx, wcfg, d.DB, d.Client, source, opts)
		if err != nil {
			return errorResult("ingest_failed", err.Error(), nil), nil
		}
		return jsonResult(map[string]any{
			"source":                 res.Source,
			"pages_written":          res.PagesWritten,
			"evidence_quotes":        res.EvidenceQuotes,
			"dropped_pages":          res.DroppedPages,
			"skipped":                res.Skipped,
			"retro_linked_pages":     res.RetroLinkedPages,
			"contradictions_flagged": res.ContradictionsFlagged,
		})
	}
}

// ----- promote_answer ----------------------------------------------------
//
// promoteAnswerHandler delegates to wiki.PromoteAnswer — the same
// callable cmd/promote's runPromote wraps. The MCP surface accepts an
// absolute answer_path (the agent doesn't share the CLI's answers-dir
// convention) plus the same PromoteOptions knobs cmd/promote exposes:
// title (override), rewrite (bool), no_save (bool). The trust property
// holds at the MCP boundary: a stale answer whose source files have
// changed since the ask is rejected before any disk write.
//
// Return JSON shape on success:
//
//	{
//	  "title":              string,         // resolved page title
//	  "path":               string,         // absolute on-disk path
//	  "evidence_quotes":    int,            // surviving validated quotes
//	  "rewrite_applied":    bool,           // false unless --rewrite + valid
//	  "retro_linked_pages": []string,       // existing pages now [[wikilinking]] this title
//	}
//
// Structured errors mirror write_page's vocabulary:
//   - evidence_invalid: every quote failed defensive re-validation;
//     payload includes a `dropped` array (quote / source_file / reason)
//     and a hint string. Same shape mcp.write_page returns.
//   - title_exists: a page with the resolved title already exists;
//     payload includes existing_path.
//   - bad_request: missing/invalid arguments.
//   - promote_failed: any other wiki.PromoteAnswer error.

func promoteAnswerHandler(d Deps) mcpsrv.ToolHandlerFunc {
	return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		answerPath, err := req.RequireString("answer_path")
		if err != nil {
			return errorResult("bad_request", err.Error(), nil), nil
		}
		opts := wiki.PromoteOptions{
			Title:   req.GetString("title", ""),
			Rewrite: req.GetBool("rewrite", false),
			NoSave:  req.GetBool("no_save", false),
		}

		wcfg := wiki.IngestSourceConfig{
			WikiDir:          d.Cfg.WikiDir,
			RawDir:           d.Cfg.RawDir,
			RespectGitignore: true,
		}
		res, err := wiki.PromoteAnswer(ctx, wcfg, d.DB, d.Client, answerPath, opts)
		if err != nil {
			switch {
			case errors.Is(err, wiki.ErrEvidenceInvalid):
				dropped := make([]map[string]any, 0, len(res.DroppedQuotes))
				for _, dq := range res.DroppedQuotes {
					dropped = append(dropped, map[string]any{
						"quote":       dq.Quote,
						"source_file": dq.SourceFile,
						"reason":      dq.Reason,
					})
				}
				return errorResult("evidence_invalid",
					"every evidence quote failed defensive re-validation; nothing was written",
					map[string]any{
						"dropped": dropped,
						"hint":    "the source files referenced by this answer have changed since the ask; re-run ask + promote against the current wiki",
					}), nil
			case errors.Is(err, wiki.ErrTitleExists):
				return errorResult("title_exists",
					fmt.Sprintf("a page titled %q already exists", res.Title),
					map[string]any{"existing_path": res.Path}), nil
			default:
				return errorResult("promote_failed", err.Error(), nil), nil
			}
		}

		retro := res.RetroLinkedTitles
		if retro == nil {
			retro = []string{}
		}
		return jsonResult(map[string]any{
			"title":              res.Title,
			"path":               res.Path,
			"evidence_quotes":    res.EvidenceQuotes,
			"rewrite_applied":    res.RewriteApplied,
			"retro_linked_pages": retro,
		})
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
