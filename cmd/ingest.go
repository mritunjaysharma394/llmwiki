package cmd

import (
	"crypto/sha256"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mritunjaysharma394/llmwiki/internal/cliutil"
	"github.com/mritunjaysharma394/llmwiki/internal/db"
	"github.com/mritunjaysharma394/llmwiki/internal/ingest"
	"github.com/mritunjaysharma394/llmwiki/internal/wiki"
	"github.com/spf13/cobra"
)

const ingestMaxInflight = 5

var ingestCmd = &cobra.Command{
	Use:   "ingest <source>",
	Short: "Ingest a source (file/directory, URL, or GitHub repo) into the wiki",
	Args:  cobra.ExactArgs(1),
	RunE:  runIngest,
}

func init() {
	ingestCmd.Flags().Int64("max-file-bytes", 0, "per-file size limit (0 uses ingest defaults)")
	ingestCmd.Flags().String("include", "", "comma-separated allowlist of extensions (e.g. .md,.go)")
	ingestCmd.Flags().String("exclude", "", "comma-separated extra skip globs (e.g. *.foo,vendor/*)")
	ingestCmd.Flags().Bool("no-gitignore", false, "ignore .gitignore for this run")
	ingestCmd.Flags().Bool("force", false, "ignore per-file unchanged check; re-ingest everything")
	ingestCmd.Flags().Bool("no-rechunk", false, "skip co-resident re-chunking; only re-process files whose own content changed")
	ingestCmd.Flags().Bool("feed", false, "force feed-parser dispatch")
	ingestCmd.Flags().Bool("sitemap", false, "force sitemap dispatch")
	ingestCmd.Flags().Int("max-pages", 0, "cap on feed entries / sitemap pages (0 uses [ingest] defaults)")
}

// DefaultFeedOptionsFromConfig resolves feed crawl tunables from the [ingest]
// config block, falling back to package defaults when c is nil.
func DefaultFeedOptionsFromConfig(c *Config) ingest.FeedOptions {
	if c == nil {
		return ingest.DefaultFeedOptions()
	}
	return ingest.FeedOptions{
		RequestsPerSecond: c.Ingest.FeedRequestsPerSecond,
		MaxEntries:        c.Ingest.FeedMaxEntries,
	}
}

// DefaultSitemapOptionsFromConfig resolves sitemap crawl tunables from the
// [ingest] config block, falling back to package defaults when c is nil. The
// rate limit is shared with feeds — both sources speak the same polite-crawl
// budget.
func DefaultSitemapOptionsFromConfig(c *Config) ingest.SitemapOptions {
	if c == nil {
		return ingest.DefaultSitemapOptions()
	}
	return ingest.SitemapOptions{
		MaxPages:          c.Ingest.SitemapMaxPages,
		RequestsPerSecond: c.Ingest.FeedRequestsPerSecond,
	}
}

// buildIngestOptions resolves the runtime walker / URL fetcher options for
// `ingest`. Layering goes: package defaults -> [ingest] config block ->
// explicit CLI flags. CLI flags always win when set; the [ingest] block lets
// users persist project-wide preferences without touching code.
func buildIngestOptions(cmd *cobra.Command, c *Config) (ingest.WalkOptions, ingest.URLOptions) {
	walk := ingest.DefaultWalkOptions()
	urlOpts := ingest.DefaultURLOptions()

	if c != nil {
		if c.Ingest.MaxFileBytes > 0 {
			walk.MaxFileBytes = c.Ingest.MaxFileBytes
		}
		if len(c.Ingest.ExtraTextExtensions) > 0 {
			walk.ExtraTextExtensions = append(walk.ExtraTextExtensions, c.Ingest.ExtraTextExtensions...)
		}
		if len(c.Ingest.ExtraSkipGlobs) > 0 {
			walk.ExtraSkipGlobs = append(walk.ExtraSkipGlobs, c.Ingest.ExtraSkipGlobs...)
		}
		walk.RespectGitignore = c.Ingest.RespectGitignoreOrDefault()
		if c.Ingest.HTTPTimeoutSeconds > 0 {
			urlOpts.Timeout = time.Duration(c.Ingest.HTTPTimeoutSeconds) * time.Second
		}
		if c.Ingest.HTTPMaxBytes > 0 {
			urlOpts.MaxBodyBytes = c.Ingest.HTTPMaxBytes
		}
	}

	if v, _ := cmd.Flags().GetInt64("max-file-bytes"); v > 0 {
		walk.MaxFileBytes = v
	}
	if v, _ := cmd.Flags().GetString("include"); v != "" {
		walk.IncludeOnly = splitCSV(v)
	}
	if v, _ := cmd.Flags().GetString("exclude"); v != "" {
		walk.ExtraSkipGlobs = append(walk.ExtraSkipGlobs, splitCSV(v)...)
	}
	if v, _ := cmd.Flags().GetBool("no-gitignore"); v {
		walk.RespectGitignore = false
	}
	return walk, urlOpts
}

// splitCSV trims and drops empty entries from a comma-separated string.
func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// filePartition splits an incoming []ingest.SourceFile against the existing
// db.SourceFile rows for a source, classifying each by what the dedup pass
// should do with it.
type filePartition struct {
	unchanged []ingest.SourceFile // path matches and content_hash matches → skip
	changed   []ingest.SourceFile // path matches but hash differs → re-ingest, drop old evidence
	newFiles  []ingest.SourceFile // path absent from existing rows → ingest
	gone      []db.SourceFile     // present in existing rows, absent from incoming → delete row + cascade evidence
}

// partitionByFileHash classifies incoming SourceFiles against the rows already
// stored under this source. Pure function — no db access — so it's straight
// forward to unit-test.
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

// computeWholeHash returns a deterministic hash over the per-file
// (RelativePath, ContentHash) pairs sorted by path. Reordering the slice
// produces the same hash; changing any single file's content does not.
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

// forceFlag returns the value of --force, defaulting to false when the flag
// hasn't been registered yet (Task 12 wires it up).
func forceFlag(cmd *cobra.Command) bool {
	f := cmd.Flags().Lookup("force")
	if f == nil {
		return false
	}
	v, _ := cmd.Flags().GetBool("force")
	return v
}

func runIngest(cmd *cobra.Command, args []string) error {
	source := args[0]
	ctx := cmd.Context()

	walkOpts, urlOpts := buildIngestOptions(cmd, cfg)

	var sourceFiles []ingest.SourceFile
	var err error
	forceFeed, _ := cmd.Flags().GetBool("feed")
	forceSitemap, _ := cmd.Flags().GetBool("sitemap")
	switch {
	case forceFeed:
		feedOpts := DefaultFeedOptionsFromConfig(cfg)
		if mp, _ := cmd.Flags().GetInt("max-pages"); mp > 0 {
			feedOpts.MaxEntries = mp
		}
		fmt.Printf("Fetching feed %s...\n", source)
		sourceFiles, err = ingest.FetchFeedFiles(source, urlOpts, feedOpts)
	case forceSitemap:
		smOpts := DefaultSitemapOptionsFromConfig(cfg)
		if mp, _ := cmd.Flags().GetInt("max-pages"); mp > 0 {
			smOpts.MaxPages = mp
		}
		fmt.Printf("Crawling sitemap %s...\n", source)
		sourceFiles, err = ingest.FetchSitemapFiles(source, urlOpts, smOpts)
	case ingest.IsGitHubURL(source):
		fmt.Printf("Cloning GitHub repo %s...\n", source)
		sourceFiles, err = ingest.FetchGitHubFiles(source, walkOpts)
	case strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://"):
		fmt.Printf("Fetching URL %s...\n", source)
		sourceFiles, err = ingest.FetchURLFiles(source, urlOpts)
	default:
		fmt.Printf("Reading local path %s...\n", source)
		sourceFiles, err = ingest.ReadLocalFiles(source, walkOpts)
	}
	if err != nil {
		if strings.Contains(err.Error(), "HTTP ") {
			return cliutil.Wrap("ingest failed",
				err,
				"check the URL is reachable in a browser; for transient 5xx errors retry the command")
		}
		if strings.Contains(err.Error(), "no extractable text") {
			return cliutil.Wrap("PDF appears to be scanned",
				err,
				"this PDF has no text layer; OCR is not supported in v1.0")
		}
		return fmt.Errorf("reading source: %w", err)
	}
	if len(sourceFiles) == 0 {
		return fmt.Errorf("no content found in source")
	}
	fmt.Printf("Resolved to %d source file(s)\n", len(sourceFiles))

	wholeHash := computeWholeHash(sourceFiles)
	existingSrc, err := database.GetSource(source)
	if err != nil {
		return fmt.Errorf("checking source: %w", err)
	}
	if existingSrc != nil && existingSrc.ContentHash == wholeHash && !forceFlag(cmd) {
		fmt.Println("Source unchanged at file level, skipping.")
		return nil
	}

	// Record the source row early so source_files can FK against it.
	sourceID, err := database.UpsertSource(source, wholeHash)
	if err != nil {
		return fmt.Errorf("recording source: %w", err)
	}

	existingFiles := map[string]db.SourceFile{}
	if rows, err := database.GetSourceFiles(sourceID); err == nil {
		for _, f := range rows {
			existingFiles[f.RelativePath] = f
		}
	}
	parts := partitionByFileHash(sourceFiles, existingFiles)

	if forceFlag(cmd) {
		// Treat unchanged-by-hash as changed; the user explicitly asked for re-ingest.
		parts.changed = append(parts.changed, parts.unchanged...)
		parts.unchanged = nil
	}

	if v, _ := cmd.Flags().GetBool("no-rechunk"); !v && len(parts.changed) > 0 {
		// Build prior-chunks map for this source.
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

		// Promote each dirty co-resident from `unchanged` into `changed`.
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

	fmt.Printf("Walking %s (%d files: %d new, %d changed, %d unchanged, %d gone)\n",
		source, len(sourceFiles), len(parts.newFiles), len(parts.changed), len(parts.unchanged), len(parts.gone))

	// Reap files that disappeared from the source.
	for _, gone := range parts.gone {
		fmt.Printf("  - removing %s (gone)\n", gone.RelativePath)
		if err := database.DeleteSourceFile(gone.ID); err != nil {
			fmt.Fprintf(os.Stderr, "  WARN delete source_file %s: %v\n", gone.RelativePath, err)
		}
	}

	toIngest := append([]ingest.SourceFile{}, parts.newFiles...)
	toIngest = append(toIngest, parts.changed...)
	if len(toIngest) == 0 && len(parts.gone) == 0 {
		fmt.Println("Source unchanged at file level, skipping.")
		return nil
	}
	if len(toIngest) == 0 {
		fmt.Printf("No new or changed files; reaped %d removed file(s).\n", len(parts.gone))
		return nil
	}

	chunkSize := 16 * 1024
	if cfg != nil && cfg.Ingest.ChunkSizeBytes > 0 {
		chunkSize = cfg.Ingest.ChunkSizeBytes
	}
	chunks := ingest.ChunkSourceFiles(toIngest, chunkSize)
	if len(chunks) > 1 {
		fmt.Printf("  Packing into %d chunks (max %d in flight)\n", len(chunks), ingestMaxInflight)
	}

	titles, err := database.AllPageTitles()
	if err != nil {
		return fmt.Errorf("fetching titles: %w", err)
	}

	type chunkResult struct {
		pages []wiki.Page
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
			got, err := wiki.IngestSourceFilesToPages(ctx, llmClient, ch.Files, titles)
			results[i] = chunkResult{pages: got, err: err, idx: i}
			n := atomic.AddInt64(&done, 1)
			fmt.Printf("\r  [%d/%d] processed", n, len(chunks))
		}(i, ch)
	}
	wg.Wait()
	if len(chunks) > 1 {
		fmt.Println()
	}

	// Upsert source_files rows for every file we attempted to ingest. The
	// ON CONFLICT path keeps the same id when re-ingesting a changed file,
	// so DeleteEvidenceForSourceFile below targets the right rows.
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
			return fmt.Errorf("upsert source_file %s: %w", f.RelativePath, err)
		}
		pathToFileID[f.RelativePath] = id
	}
	// Clear stale evidence for changed files before re-inserting fresh rows.
	for _, f := range parts.changed {
		if id := pathToFileID[f.RelativePath]; id != 0 {
			if err := database.DeleteEvidenceForSourceFile(id); err != nil {
				fmt.Fprintf(os.Stderr, "  WARN clear evidence for %s: %v\n", f.RelativePath, err)
			}
		}
	}

	// Replace prior chunk bookkeeping for this source with the fresh pack.
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

	var allPages []wiki.Page
	for _, r := range results {
		if r.err != nil {
			fmt.Printf("  WARN chunk %d failed: %v\n", r.idx+1, r.err)
			continue
		}
		allPages = append(allPages, r.pages...)
	}
	if len(allPages) == 0 {
		fmt.Println("LLM produced no pages with verifiable evidence.")
		return nil
	}

	if err := os.MkdirAll(cfg.Wiki.WikiDir, 0755); err != nil {
		return err
	}
	// Build the title set once: existing-on-disk titles plus the new
	// titles in this batch. Used to rewrite bare prose references into
	// [[wikilinks]] before each page hits disk. The set is not enforced
	// by the validator — a body that mentions a stranger title in prose
	// is still valid; this is a body-quality polish.
	allTitles := make([]string, 0, len(titles)+len(allPages))
	allTitles = append(allTitles, titles...)
	for _, p := range allPages {
		allTitles = append(allTitles, p.Title)
	}
	now := time.Now().UTC()
	totalEvidence := 0
	for i := range allPages {
		allPages[i].SourceIDs = []int64{sourceID}
		// Phase F: wikilink rewrite + Dataview frontmatter stamps.
		allPages[i].Body = wiki.RewriteBareReferencesAsWikilinks(allPages[i].Body, allTitles)
		allPages[i].Tags = []string{"llmwiki", "ingest"}
		allPages[i].Sources = distinctSourceFiles(allPages[i].Evidence)
		if allPages[i].Created.IsZero() {
			allPages[i].Created = now
		}
		totalEvidence += len(allPages[i].Evidence)
		path := wiki.PagePath(cfg.Wiki.WikiDir, allPages[i].Title)
		if err := wiki.WritePage(allPages[i], cfg.Wiki.WikiDir); err != nil {
			return fmt.Errorf("writing page %q: %w", allPages[i].Title, err)
		}
		rec := db.PageRecord{
			Title:       allPages[i].Title,
			Path:        path,
			Body:        allPages[i].Body,
			ContentHash: allPages[i].ContentHash,
			SourceIDs:   allPages[i].SourceIDs,
		}
		if err := database.UpsertPage(rec); err != nil {
			return fmt.Errorf("db upsert %q: %w", allPages[i].Title, err)
		}

		stored, err := database.GetPage(allPages[i].Title)
		if err != nil || stored == nil {
			return fmt.Errorf("re-fetch page %q: %v", allPages[i].Title, err)
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
			return fmt.Errorf("insert evidence for %q: %w", allPages[i].Title, err)
		}

		var links []db.Link
		for _, l := range allPages[i].Links {
			links = append(links, db.Link{FromPage: allPages[i].Title, ToPage: l.To, LinkType: l.Type})
		}
		if len(links) > 0 {
			database.UpsertLinks(allPages[i].Title, links)
		}

		// Distinct list of source files backing this page's evidence.
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
			annotation = fmt.Sprintf(", files: %s", strings.Join(distinctFiles, ", "))
		}
		fmt.Printf("  ✓ %s (%d evidence%s)\n", allPages[i].Title, len(allPages[i].Evidence), annotation)
	}
	fmt.Printf("Ingested %d page(s) from %s\n", len(allPages), source)

	// Phase F: regenerate the Obsidian index against the current DB state,
	// then append a single chronicle line for this ingest run. Both are
	// best-effort — a failure does not undo the per-page disk writes
	// already done above. Failures go to stderr so the user still sees
	// the wiki was updated even if the side files needed attention.
	allPageRecs, err := database.AllPages()
	if err != nil {
		fmt.Fprintf(os.Stderr, "  WARN reading pages for index: %v\n", err)
	} else {
		allSources, _ := database.GetAllSources()
		if err := wiki.RegenerateIndex(cfg.Wiki.WikiDir, allPageRecs, allSources, time.Now().UTC()); err != nil {
			fmt.Fprintf(os.Stderr, "  WARN regenerating index.md: %v\n", err)
		}
	}
	_ = wiki.AppendLog(cfg.Wiki.WikiDir, wiki.LogEntry{
		At:   time.Now().UTC(),
		Kind: "ingest",
		Payload: fmt.Sprintf("%s → %d pages, %d evidence quotes",
			source, len(allPages), totalEvidence),
	})
	return nil
}

// distinctSourceFiles returns the distinct, first-occurrence-ordered list of
// non-empty SourceFilePath values across the given evidence rows. Used by the
// Phase F ingest wiring to populate Page.Sources before WritePage.
func distinctSourceFiles(ev []wiki.Evidence) []string {
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

func slugify(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + 32)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if len(out) > 60 {
		out = out[:60]
	}
	return out
}
