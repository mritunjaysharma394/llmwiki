// Package wiki — ingest_runner.go
//
// IngestSource is the package-internal entry point that wraps the full
// "fetch a source, dedup against prior runs, chunk, ask the LLM for pages,
// validate evidence, write pages to disk, persist to DB, regenerate index,
// append log" pipeline. It exists so that both cmd/ingest.go (the cobra
// command) and internal/mcp's ingestHandler can drive the same code path
// without internal/mcp importing cmd (which would create a cycle through
// cmd/root.go's package-level globals).
//
// cmd/ingest.go's runIngest is a thin wrapper that maps cobra flags +
// cmd.Config onto IngestOptions / IngestSourceConfig and calls
// IngestSource. The MCP ingest handler does the same translation from
// mcp.Config + tool arguments. The progress-printing behaviour is gated
// by IngestOptions.Logger — the CLI passes os.Stdout, MCP passes
// io.Discard.
package wiki

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mritunjaysharma394/llmwiki/internal/db"
	"github.com/mritunjaysharma394/llmwiki/internal/ingest"
	"github.com/mritunjaysharma394/llmwiki/internal/llm"
)

const ingestMaxInflight = 5

// IngestSourceConfig is the slim subset of cmd.Config the runner needs.
// cmd/ingest.go translates from cmd.Config; internal/mcp builds it from
// mcp.Config + per-call defaults.
type IngestSourceConfig struct {
	WikiDir string
	RawDir  string

	// Walker / fetcher tunables.
	MaxFileBytes        int64
	ChunkSizeBytes      int
	HTTPTimeoutSeconds  int
	HTTPMaxBytes        int64
	ExtraTextExtensions []string
	ExtraSkipGlobs      []string
	RespectGitignore    bool

	// Feed/sitemap tunables.
	FeedRequestsPerSecond float64
	FeedMaxEntries        int
	SitemapMaxPages       int
}

// IngestOptions captures the per-call knobs that map onto cobra flags
// (`--force`, `--feed`, `--sitemap`, `--include`, `--exclude`,
// `--max-file-bytes`, `--no-gitignore`, `--no-rechunk`, `--max-pages`).
// MCP's ingest handler exposes a subset (force, feed, sitemap, max_pages,
// include, exclude); the rest stay at config defaults.
type IngestOptions struct {
	Force        bool
	NoRechunk    bool
	Feed         bool
	Sitemap      bool
	MaxPages     int
	Include      []string
	Exclude      []string
	NoGitignore  bool
	MaxFileBytes int64

	// Logger receives the human-readable progress lines runIngest used to
	// print to stdout. nil → io.Discard (the MCP handler passes nil).
	Logger io.Writer
}

// IngestRunResult is what cmd and MCP both surface to their callers.
// Skipped is true when the run was a no-op because the source's
// content_hash matches the prior run (and Force was false).
//
// Sub-project 6a additions:
//   - RetroLinkedPages: number of pre-existing pages whose bodies were
//     rewritten to add `[[wikilink]]` references to titles written by
//     this ingest run. Populated by the RetroLinkPages call between
//     the persist loop and RegenerateIndex.
//   - RetroLinkedTitles: the actual titles of those pages, in
//     candidate-walk order; the cmd/ingest summary lists them under
//     the "Retro-linked N existing page(s):" line.
//   - ContradictionsFlagged: populated by Phase E (DetectIngestContradictions)
//     once that wires in.
type IngestRunResult struct {
	Source                string
	PagesWritten          int
	EvidenceQuotes        int
	DroppedPages          int
	Skipped               bool
	RetroLinkedPages      int
	RetroLinkedTitles     []string
	ContradictionsFlagged int
}

// IngestSource runs the full ingest pipeline and returns a structured
// result. Mirrors what the v1.0 cmd/ingest.go runIngest body did; lifted
// here so internal/mcp's ingest handler can drive it without importing
// cmd/.
func IngestSource(ctx context.Context, cfg IngestSourceConfig, database *db.DB, client llm.Client, source string, opts IngestOptions) (IngestRunResult, error) {
	out := IngestRunResult{Source: source}
	logger := opts.Logger
	if logger == nil {
		logger = io.Discard
	}
	logf := func(format string, args ...any) { fmt.Fprintf(logger, format, args...) }

	walkOpts := buildWalkOptions(cfg, opts)
	urlOpts := buildURLOptions(cfg)

	var sourceFiles []ingest.SourceFile
	var err error
	switch {
	case opts.Feed:
		feedOpts := ingest.DefaultFeedOptions()
		if cfg.FeedRequestsPerSecond > 0 {
			feedOpts.RequestsPerSecond = cfg.FeedRequestsPerSecond
		}
		if cfg.FeedMaxEntries > 0 {
			feedOpts.MaxEntries = cfg.FeedMaxEntries
		}
		if opts.MaxPages > 0 {
			feedOpts.MaxEntries = opts.MaxPages
		}
		logf("Fetching feed %s...\n", source)
		sourceFiles, err = ingest.FetchFeedFiles(source, urlOpts, feedOpts)
	case opts.Sitemap:
		smOpts := ingest.DefaultSitemapOptions()
		if cfg.SitemapMaxPages > 0 {
			smOpts.MaxPages = cfg.SitemapMaxPages
		}
		if cfg.FeedRequestsPerSecond > 0 {
			smOpts.RequestsPerSecond = cfg.FeedRequestsPerSecond
		}
		if opts.MaxPages > 0 {
			smOpts.MaxPages = opts.MaxPages
		}
		logf("Crawling sitemap %s...\n", source)
		sourceFiles, err = ingest.FetchSitemapFiles(source, urlOpts, smOpts)
	case ingest.IsGitHubURL(source):
		logf("Cloning GitHub repo %s...\n", source)
		sourceFiles, err = ingest.FetchGitHubFiles(source, walkOpts)
	case isHTTPURL(source):
		logf("Fetching URL %s...\n", source)
		sourceFiles, err = ingest.FetchURLFiles(source, urlOpts)
	default:
		logf("Reading local path %s...\n", source)
		sourceFiles, err = ingest.ReadLocalFiles(source, walkOpts)
	}
	if err != nil {
		return out, fmt.Errorf("reading source: %w", err)
	}
	if len(sourceFiles) == 0 {
		return out, fmt.Errorf("no content found in source")
	}
	logf("Resolved to %d source file(s)\n", len(sourceFiles))

	wholeHash := computeWholeHash(sourceFiles)
	existingSrc, err := database.GetSource(source)
	if err != nil {
		return out, fmt.Errorf("checking source: %w", err)
	}
	if existingSrc != nil && existingSrc.ContentHash == wholeHash && !opts.Force {
		logf("Source unchanged at file level, skipping.\n")
		out.Skipped = true
		return out, nil
	}

	sourceID, err := database.UpsertSource(source, wholeHash)
	if err != nil {
		return out, fmt.Errorf("recording source: %w", err)
	}

	existingFiles := map[string]db.SourceFile{}
	if rows, err := database.GetSourceFiles(sourceID); err == nil {
		for _, f := range rows {
			existingFiles[f.RelativePath] = f
		}
	}
	parts := partitionByFileHash(sourceFiles, existingFiles)

	if opts.Force {
		parts.changed = append(parts.changed, parts.unchanged...)
		parts.unchanged = nil
	}

	if !opts.NoRechunk && len(parts.changed) > 0 {
		priorChunks := map[string][]string{}
		for _, ch := range parts.changed {
			chunks, err := database.GetChunksForFile(sourceID, ch.RelativePath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  WARN reading prior chunks for %s: %v\n", ch.RelativePath, err)
				continue
			}
			for _, c := range chunks {
				priorChunks[c.ChunkHash] = c.FilePaths
			}
		}
		changedPaths := make([]string, len(parts.changed))
		for i, f := range parts.changed {
			changedPaths[i] = f.RelativePath
		}
		dirtyPaths := ingest.MarkCoResidentDirty(changedPaths, priorChunks)
		dirtySet := map[string]bool{}
		for _, p := range dirtyPaths {
			dirtySet[p] = true
		}
		stillUnchanged := parts.unchanged[:0]
		for _, f := range parts.unchanged {
			if dirtySet[f.RelativePath] {
				parts.changed = append(parts.changed, f)
			} else {
				stillUnchanged = append(stillUnchanged, f)
			}
		}
		parts.unchanged = stillUnchanged
	}

	logf("Walking %s (%d files: %d new, %d changed, %d unchanged, %d gone)\n",
		source, len(sourceFiles), len(parts.newFiles), len(parts.changed), len(parts.unchanged), len(parts.gone))

	for _, gone := range parts.gone {
		logf("  - removing %s (gone)\n", gone.RelativePath)
		if err := database.DeleteSourceFile(gone.ID); err != nil {
			fmt.Fprintf(os.Stderr, "  WARN delete source_file %s: %v\n", gone.RelativePath, err)
		}
	}

	toIngest := append([]ingest.SourceFile{}, parts.newFiles...)
	toIngest = append(toIngest, parts.changed...)
	if len(toIngest) == 0 && len(parts.gone) == 0 {
		logf("Source unchanged at file level, skipping.\n")
		out.Skipped = true
		return out, nil
	}
	if len(toIngest) == 0 {
		logf("No new or changed files; reaped %d removed file(s).\n", len(parts.gone))
		return out, nil
	}

	chunkSize := cfg.ChunkSizeBytes
	if chunkSize <= 0 {
		chunkSize = 16 * 1024
	}
	chunks := ingest.ChunkSourceFiles(toIngest, chunkSize)
	if len(chunks) > 1 {
		logf("  Packing into %d chunks (max %d in flight)\n", len(chunks), ingestMaxInflight)
	}

	titles, err := database.AllPageTitles()
	if err != nil {
		return out, fmt.Errorf("fetching titles: %w", err)
	}

	type chunkResult struct {
		pages []Page
		err   error
		idx   int
	}
	results := make([]chunkResult, len(chunks))
	sem := make(chan struct{}, ingestMaxInflight)
	var wg sync.WaitGroup
	var done int64
	for i, ch := range chunks {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, ch ingest.Chunk) {
			defer wg.Done()
			defer func() { <-sem }()
			got, err := IngestSourceFilesToPages(ctx, client, ch.Files, titles)
			results[i] = chunkResult{pages: got, err: err, idx: i}
			n := atomic.AddInt64(&done, 1)
			fmt.Fprintf(logger, "\r  [%d/%d] processed", n, len(chunks))
		}(i, ch)
	}
	wg.Wait()
	if len(chunks) > 1 {
		logf("\n")
	}

	pathToFileID := map[string]int64{}
	for _, f := range toIngest {
		id, err := database.UpsertSourceFile(db.SourceFile{
			SourceID:     sourceID,
			RelativePath: f.RelativePath,
			ContentHash:  f.ContentHash,
			ByteSize:     f.ByteSize,
			LineCount:    f.LineCount,
		})
		if err != nil {
			return out, fmt.Errorf("upsert source_file %s: %w", f.RelativePath, err)
		}
		pathToFileID[f.RelativePath] = id
	}
	for _, f := range parts.changed {
		if id := pathToFileID[f.RelativePath]; id != 0 {
			if err := database.DeleteEvidenceForSourceFile(id); err != nil {
				fmt.Fprintf(os.Stderr, "  WARN clear evidence for %s: %v\n", f.RelativePath, err)
			}
		}
	}

	if err := database.DeleteChunksForSource(sourceID); err != nil {
		fmt.Fprintf(os.Stderr, "  WARN clearing chunks for source %d: %v\n", sourceID, err)
	}
	var chunkRows []db.Chunk
	for _, ch := range chunks {
		hash := sha256.Sum256([]byte(ch.Text))
		paths := make([]string, len(ch.Files))
		for i, f := range ch.Files {
			paths[i] = f.RelativePath
		}
		chunkRows = append(chunkRows, db.Chunk{
			SourceID:  sourceID,
			ChunkHash: fmt.Sprintf("%x", hash),
			FilePaths: paths,
		})
	}
	if err := database.InsertChunks(chunkRows); err != nil {
		fmt.Fprintf(os.Stderr, "  WARN persisting chunks for source %d: %v\n", sourceID, err)
	}

	var allPages []Page
	for _, r := range results {
		if r.err != nil {
			logf("  WARN chunk %d failed: %v\n", r.idx+1, r.err)
			continue
		}
		allPages = append(allPages, r.pages...)
	}
	if len(allPages) == 0 {
		logf("LLM produced no pages with verifiable evidence.\n")
		return out, nil
	}

	if err := os.MkdirAll(cfg.WikiDir, 0755); err != nil {
		return out, err
	}
	allTitles := make([]string, 0, len(titles)+len(allPages))
	allTitles = append(allTitles, titles...)
	for _, p := range allPages {
		allTitles = append(allTitles, p.Title)
	}
	now := time.Now().UTC()
	totalEvidence := 0
	for i := range allPages {
		allPages[i].SourceIDs = []int64{sourceID}
		allPages[i].Body = RewriteBareReferencesAsWikilinks(allPages[i].Body, allTitles)
		allPages[i].Tags = []string{"llmwiki", "ingest"}
		allPages[i].Sources = distinctEvidenceSourceFiles(allPages[i].Evidence)
		if allPages[i].Created.IsZero() {
			allPages[i].Created = now
		}
		totalEvidence += len(allPages[i].Evidence)
		path := PagePath(cfg.WikiDir, allPages[i].Title)
		if err := WritePage(allPages[i], cfg.WikiDir); err != nil {
			return out, fmt.Errorf("writing page %q: %w", allPages[i].Title, err)
		}
		rec := db.PageRecord{
			Title:       allPages[i].Title,
			Path:        path,
			Body:        allPages[i].Body,
			ContentHash: allPages[i].ContentHash,
			SourceIDs:   allPages[i].SourceIDs,
		}
		if err := database.UpsertPage(rec); err != nil {
			return out, fmt.Errorf("db upsert %q: %w", allPages[i].Title, err)
		}

		stored, err := database.GetPage(allPages[i].Title)
		if err != nil || stored == nil {
			return out, fmt.Errorf("re-fetch page %q: %v", allPages[i].Title, err)
		}
		var dbEv []db.Evidence
		for _, e := range allPages[i].Evidence {
			var sfPtr *int64
			if id, ok := pathToFileID[e.SourceFilePath]; ok && id != 0 {
				v := id
				sfPtr = &v
			}
			dbEv = append(dbEv, db.Evidence{
				Quote:        e.Quote,
				LineStart:    e.LineStart,
				LineEnd:      e.LineEnd,
				SourceFileID: sfPtr,
			})
		}
		if err := database.InsertEvidence(stored.ID, sourceID, dbEv); err != nil {
			return out, fmt.Errorf("insert evidence for %q: %w", allPages[i].Title, err)
		}

		var links []db.Link
		for _, l := range allPages[i].Links {
			links = append(links, db.Link{FromPage: allPages[i].Title, ToPage: l.To, LinkType: l.Type})
		}
		if len(links) > 0 {
			database.UpsertLinks(allPages[i].Title, links)
		}

		seen := map[string]bool{}
		var distinctFiles []string
		for _, e := range allPages[i].Evidence {
			if e.SourceFilePath == "" || seen[e.SourceFilePath] {
				continue
			}
			seen[e.SourceFilePath] = true
			distinctFiles = append(distinctFiles, e.SourceFilePath)
		}
		annotation := ""
		if len(distinctFiles) > 0 {
			annotation = fmt.Sprintf(", files: %s", joinComma(distinctFiles))
		}
		logf("  ✓ %s (%d evidence%s)\n", allPages[i].Title, len(allPages[i].Evidence), annotation)
	}
	logf("Ingested %d page(s) from %s\n", len(allPages), source)

	// Phase D (sub-project 6a): retro-link existing pages whose bodies
	// mention any of the just-written titles. Body-only, idempotent,
	// never touches evidence — the validator does not run here. Spec
	// risk #4: at N>=500 existing pages the candidate set is FTS-filtered.
	// Runs BEFORE RegenerateIndex so index.md picks up the bumped
	// updated_at on rewritten existing pages.
	newTitles := make([]string, 0, len(allPages))
	for _, p := range allPages {
		newTitles = append(newTitles, p.Title)
	}
	retroRes, rlErr := RetroLinkPages(database, cfg.WikiDir, newTitles)
	if rlErr != nil {
		fmt.Fprintf(os.Stderr, "  WARN retro-linking existing pages: %v\n", rlErr)
	}
	out.RetroLinkedPages = len(retroRes.UpdatedTitles)
	out.RetroLinkedTitles = retroRes.UpdatedTitles
	if len(retroRes.UpdatedTitles) > 0 {
		logf("Retro-linked %d existing page(s) that now reference [[%s]]:\n",
			len(retroRes.UpdatedTitles), joinComma(newTitles))
		for _, t := range retroRes.UpdatedTitles {
			logf("  - %s\n", t)
		}
	}

	allPageRecs, err := database.AllPages()
	if err != nil {
		fmt.Fprintf(os.Stderr, "  WARN reading pages for index: %v\n", err)
	} else {
		allSources, _ := database.GetAllSources()
		if err := RegenerateIndex(cfg.WikiDir, allPageRecs, allSources, time.Now().UTC()); err != nil {
			fmt.Fprintf(os.Stderr, "  WARN regenerating index.md: %v\n", err)
		}
	}
	_ = AppendLog(cfg.WikiDir, LogEntry{
		At:   time.Now().UTC(),
		Kind: "ingest",
		Payload: fmt.Sprintf("%s → %d pages, %d evidence quotes",
			source, len(allPages), totalEvidence),
	})

	out.PagesWritten = len(allPages)
	out.EvidenceQuotes = totalEvidence
	return out, nil
}

// filePartition mirrors cmd/ingest.go's partitionByFileHash output. Lifted
// here so the runner can dedup against existing source_files without
// referring back to cmd.
type filePartition struct {
	unchanged []ingest.SourceFile
	changed   []ingest.SourceFile
	newFiles  []ingest.SourceFile
	gone      []db.SourceFile
}

func partitionByFileHash(incoming []ingest.SourceFile, existing map[string]db.SourceFile) filePartition {
	var p filePartition
	seen := map[string]bool{}
	for _, f := range incoming {
		seen[f.RelativePath] = true
		ex, ok := existing[f.RelativePath]
		switch {
		case !ok:
			p.newFiles = append(p.newFiles, f)
		case ex.ContentHash == f.ContentHash:
			p.unchanged = append(p.unchanged, f)
		default:
			p.changed = append(p.changed, f)
		}
	}
	for path, ex := range existing {
		if !seen[path] {
			p.gone = append(p.gone, ex)
		}
	}
	return p
}

func computeWholeHash(files []ingest.SourceFile) string {
	h := sha256.New()
	paths := make([]string, len(files))
	byPath := make(map[string]ingest.SourceFile, len(files))
	for i, f := range files {
		paths[i] = f.RelativePath
		byPath[f.RelativePath] = f
	}
	sort.Strings(paths)
	for _, p := range paths {
		h.Write([]byte(p))
		h.Write([]byte{0})
		h.Write([]byte(byPath[p].ContentHash))
		h.Write([]byte{0})
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func buildWalkOptions(cfg IngestSourceConfig, opts IngestOptions) ingest.WalkOptions {
	walk := ingest.DefaultWalkOptions()
	if cfg.MaxFileBytes > 0 {
		walk.MaxFileBytes = cfg.MaxFileBytes
	}
	if len(cfg.ExtraTextExtensions) > 0 {
		walk.ExtraTextExtensions = append(walk.ExtraTextExtensions, cfg.ExtraTextExtensions...)
	}
	if len(cfg.ExtraSkipGlobs) > 0 {
		walk.ExtraSkipGlobs = append(walk.ExtraSkipGlobs, cfg.ExtraSkipGlobs...)
	}
	walk.RespectGitignore = cfg.RespectGitignore

	if opts.MaxFileBytes > 0 {
		walk.MaxFileBytes = opts.MaxFileBytes
	}
	if len(opts.Include) > 0 {
		walk.IncludeOnly = opts.Include
	}
	if len(opts.Exclude) > 0 {
		walk.ExtraSkipGlobs = append(walk.ExtraSkipGlobs, opts.Exclude...)
	}
	if opts.NoGitignore {
		walk.RespectGitignore = false
	}
	return walk
}

func buildURLOptions(cfg IngestSourceConfig) ingest.URLOptions {
	urlOpts := ingest.DefaultURLOptions()
	if cfg.HTTPTimeoutSeconds > 0 {
		urlOpts.Timeout = time.Duration(cfg.HTTPTimeoutSeconds) * time.Second
	}
	if cfg.HTTPMaxBytes > 0 {
		urlOpts.MaxBodyBytes = cfg.HTTPMaxBytes
	}
	return urlOpts
}

func isHTTPURL(s string) bool {
	return len(s) >= 7 && (s[:7] == "http://" || (len(s) >= 8 && s[:8] == "https://"))
}

func distinctEvidenceSourceFiles(ev []Evidence) []string {
	if len(ev) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(ev))
	var out []string
	for _, e := range ev {
		if e.SourceFilePath == "" {
			continue
		}
		if _, ok := seen[e.SourceFilePath]; ok {
			continue
		}
		seen[e.SourceFilePath] = struct{}{}
		out = append(out, e.SourceFilePath)
	}
	return out
}

func joinComma(s []string) string {
	switch len(s) {
	case 0:
		return ""
	case 1:
		return s[0]
	}
	out := s[0]
	for _, p := range s[1:] {
		out += ", " + p
	}
	return out
}
