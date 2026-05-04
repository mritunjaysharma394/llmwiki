package cmd

import (
	"crypto/sha256"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/mritunjaysharma394/llmwiki/internal/cliutil"
	"github.com/mritunjaysharma394/llmwiki/internal/db"
	"github.com/mritunjaysharma394/llmwiki/internal/ingest"
	"github.com/mritunjaysharma394/llmwiki/internal/wiki"
	"github.com/spf13/cobra"
)

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
	ingestCmd.Flags().Bool("update-existing", false,
		"after writing new pages, propose updates to existing pages whose claims this source touches; off by default. Pages whose proposed body fails byte-exact substring-match validation against the (new + existing) source union stay at their previous version.")
	ingestCmd.Flags().Bool("debug-updates", false,
		"print per-candidate verdicts from --update-existing to stderr (LLM proposed body, validator kept N quotes, content_hash drift); useful when an update_failed line appears in the summary.")
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

// runIngest is the cobra adapter: it translates --flags + cmd.Config into
// wiki.IngestSourceConfig + wiki.IngestOptions and delegates the heavy
// lifting to wiki.IngestSource. Phase G2 lifted the body into
// internal/wiki so the MCP ingest handler can drive the same pipeline
// without importing cmd/. Error cliutil-wrapping for HTTP / scanned-PDF
// stays here because cliutil is a CLI-surface concern.
func runIngest(cmd *cobra.Command, args []string) error {
	source := args[0]
	ctx := cmd.Context()

	wcfg := toWikiIngestConfig(cfg)
	opts := buildWikiIngestOptions(cmd, cfg)
	opts.Logger = os.Stdout

	_, err := wiki.IngestSource(ctx, wcfg, database, llmClient, source, opts)
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
		return err
	}
	return nil
}

// toWikiIngestConfig translates the cobra-side cmd.Config into the slim
// IngestSourceConfig the wiki runner expects. internal/wiki must not
// import cmd, so this lives on the cmd side.
func toWikiIngestConfig(c *Config) wiki.IngestSourceConfig {
	if c == nil {
		return wiki.IngestSourceConfig{
			RespectGitignore: true,
		}
	}
	return wiki.IngestSourceConfig{
		WikiDir:               c.Wiki.WikiDir,
		RawDir:                c.Wiki.RawDir,
		MaxFileBytes:          c.Ingest.MaxFileBytes,
		ChunkSizeBytes:        c.Ingest.ChunkSizeBytes,
		HTTPTimeoutSeconds:    c.Ingest.HTTPTimeoutSeconds,
		HTTPMaxBytes:          c.Ingest.HTTPMaxBytes,
		ExtraTextExtensions:   c.Ingest.ExtraTextExtensions,
		ExtraSkipGlobs:        c.Ingest.ExtraSkipGlobs,
		RespectGitignore:      c.Ingest.RespectGitignoreOrDefault(),
		FeedRequestsPerSecond: c.Ingest.FeedRequestsPerSecond,
		FeedMaxEntries:        c.Ingest.FeedMaxEntries,
		SitemapMaxPages:       c.Ingest.SitemapMaxPages,
	}
}

// buildWikiIngestOptions reads cobra flags into the runner's IngestOptions.
// Defaults are zero-values; the runner falls back to walker / fetcher
// defaults internally when fields are zero. The Config argument is used
// for sub-project 6b's [ingest] update_existing block — flag wins when
// explicitly set, otherwise the config falls through.
func buildWikiIngestOptions(cmd *cobra.Command, c *Config) wiki.IngestOptions {
	opts := wiki.IngestOptions{
		Force:     forceFlag(cmd),
		NoRechunk: false,
	}
	if v, _ := cmd.Flags().GetBool("no-rechunk"); v {
		opts.NoRechunk = true
	}
	if v, _ := cmd.Flags().GetBool("feed"); v {
		opts.Feed = true
	}
	if v, _ := cmd.Flags().GetBool("sitemap"); v {
		opts.Sitemap = true
	}
	if v, _ := cmd.Flags().GetInt("max-pages"); v > 0 {
		opts.MaxPages = v
	}
	if v, _ := cmd.Flags().GetInt64("max-file-bytes"); v > 0 {
		opts.MaxFileBytes = v
	}
	if v, _ := cmd.Flags().GetString("include"); v != "" {
		opts.Include = splitCSV(v)
	}
	if v, _ := cmd.Flags().GetString("exclude"); v != "" {
		opts.Exclude = splitCSV(v)
	}
	if v, _ := cmd.Flags().GetBool("no-gitignore"); v {
		opts.NoGitignore = true
	}
	opts.UpdateExisting = resolveUpdateExisting(cmd, c)
	if v, _ := cmd.Flags().GetBool("debug-updates"); v {
		opts.DebugUpdates = true
	}
	if c != nil {
		opts.UpdateExistingMaxCandidatesPerSource = c.Ingest.UpdateExistingMaxCandidatesPerSource
		opts.UpdateExistingMaxCandidatesTotal = c.Ingest.UpdateExistingMaxCandidatesTotal
		opts.UpdateExistingQuoteFloor = c.Ingest.UpdateExistingQuoteFloor
	}
	// Phase C Task 6: activeSchema is loaded by cmd/root.go's
	// loadConfig from AGENTS.md / CLAUDE.md at the wiki root, falling
	// back to schema.Bundled() when neither file exists.
	opts.Schema = activeSchema
	return opts
}

// resolveUpdateExisting layers package default → [ingest] config → CLI
// flag, CLI wins when explicitly set. Mirrors the RespectGitignore *bool
// "absent vs explicit" disambiguation pattern.
func resolveUpdateExisting(cmd *cobra.Command, c *Config) bool {
	if cmd.Flags().Changed("update-existing") {
		v, _ := cmd.Flags().GetBool("update-existing")
		return v
	}
	if c != nil {
		return c.Ingest.UpdateExistingOrDefault()
	}
	return false
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
