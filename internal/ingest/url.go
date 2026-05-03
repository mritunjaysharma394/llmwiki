package ingest

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	htmltomd "github.com/JohannesKaufmann/html-to-markdown/v2"
	readability "github.com/go-shiori/go-readability"

	"github.com/mritunjaysharma394/llmwiki/internal/version"
)

// URLOptions tunes the URL fetcher used by FetchURLFiles.
//
// Defaults come from DefaultURLOptions(): 30s timeout, 5 MB body cap, the
// "llmwiki/<version>" user agent, and the standard library's net/http client
// (which by default follows up to 10 redirects). Tests can inject a custom
// HTTPClient to capture or stub network behavior.
type URLOptions struct {
	Timeout      time.Duration
	MaxBodyBytes int64
	UserAgent    string
	HTTPClient   *http.Client
}

// DefaultURLOptions returns the standard URL fetcher configuration.
func DefaultURLOptions() URLOptions {
	return URLOptions{
		Timeout:      30 * time.Second,
		MaxBodyBytes: 5 * 1024 * 1024,
		UserAgent:    "llmwiki/" + version.Version,
	}
}

// FetchURLFiles fetches a URL and dispatches by content-type, returning one or
// more SourceFile values for the ingest pipeline:
//
//	application/pdf  (or .pdf path) -> ReadPDF on a temp file, one SourceFile per page ("page-N")
//	text/html, application/xhtml+xml -> Readability article extraction + html-to-markdown,
//	                                    one SourceFile with RelativePath "index.html"
//	other text/*                    -> raw passthrough as one SourceFile ("body.txt")
//	anything else                   -> error
//
// Body size is capped at opts.MaxBodyBytes via io.LimitReader. Status codes
// >= 400 produce an error.
func FetchURLFiles(rawURL string, opts URLOptions) ([]SourceFile, error) {
	if opts.Timeout == 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.MaxBodyBytes == 0 {
		opts.MaxBodyBytes = 5 * 1024 * 1024
	}
	if opts.UserAgent == "" {
		opts.UserAgent = "llmwiki/" + version.Version
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: opts.Timeout}
	}

	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("User-Agent", opts.UserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d fetching %s", resp.StatusCode, rawURL)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, opts.MaxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("reading body: %w", err)
	}

	ct := strings.SplitN(resp.Header.Get("Content-Type"), ";", 2)[0]
	ct = strings.TrimSpace(strings.ToLower(ct))
	parsed, _ := url.Parse(rawURL)
	extLower := ""
	if parsed != nil {
		extLower = strings.ToLower(filepath.Ext(parsed.Path))
	}

	switch {
	case ct == "application/pdf" || extLower == ".pdf":
		return fetchPDFViaTempFile(body)
	case isFeedContentType(ct, body):
		// Re-dispatch through FetchFeedFiles, which re-fetches the feed body
		// (cheap; gofeed parses URL directly). Acceptable double-fetch for v1.
		return FetchFeedFiles(rawURL, opts, DefaultFeedOptions())
	case isSitemapContentType(ct, body, parsed):
		return FetchSitemapFiles(rawURL, opts, DefaultSitemapOptions())
	case ct == "text/html", ct == "application/xhtml+xml":
		return fetchHTMLAsMarkdown(body, rawURL)
	case strings.HasPrefix(ct, "text/"):
		return []SourceFile{NewSourceFile("body.txt", body)}, nil
	default:
		return nil, fmt.Errorf("unsupported content-type %q for URL ingestion", ct)
	}
}

// isFeedContentType returns true when the (content-type, body) pair looks like
// an RSS, Atom, or JSON Feed. The XML branch peeks at the first 512 bytes for
// a <rss> or <feed> root; the JSON branch sniffs for "version" + "jsonfeed.org"
// to avoid false-positives on arbitrary application/json.
func isFeedContentType(ct string, body []byte) bool {
	switch ct {
	case "application/rss+xml", "application/atom+xml":
		return true
	}
	if ct == "application/json" {
		// JSON Feed — sniff the "feed_url" or "version" key.
		return bytes.Contains(body, []byte(`"version"`)) && bytes.Contains(body, []byte(`jsonfeed.org`))
	}
	if ct == "application/xml" || ct == "text/xml" {
		head := body
		if len(head) > 512 {
			head = head[:512]
		}
		return bytes.Contains(head, []byte("<rss")) || bytes.Contains(head, []byte("<feed"))
	}
	return false
}

// isSitemapContentType returns true when the URL or body looks like a
// sitemap.xml. The path-suffix check handles the common case where servers
// return application/xml for /sitemap.xml; the body sniff catches index files
// served from non-standard paths.
func isSitemapContentType(ct string, body []byte, parsed *url.URL) bool {
	if parsed != nil && strings.HasSuffix(strings.ToLower(parsed.Path), "sitemap.xml") {
		return true
	}
	if ct == "application/xml" || ct == "text/xml" {
		head := body
		if len(head) > 512 {
			head = head[:512]
		}
		return bytes.Contains(head, []byte("<urlset")) || bytes.Contains(head, []byte("<sitemapindex"))
	}
	return false
}

func fetchPDFViaTempFile(body []byte) ([]SourceFile, error) {
	tmp, err := os.CreateTemp("", "llmwiki-url-*.pdf")
	if err != nil {
		return nil, fmt.Errorf("temp pdf: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return nil, fmt.Errorf("writing temp pdf: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("closing temp pdf: %w", err)
	}
	return ReadPDF(tmp.Name())
}

func fetchHTMLAsMarkdown(body []byte, srcURL string) ([]SourceFile, error) {
	parsed, _ := url.Parse(srcURL)
	article, err := readability.FromReader(bytes.NewReader(body), parsed)
	var html string
	if err == nil && strings.TrimSpace(article.Content) != "" {
		html = article.Content
	} else {
		// Fallback: pass full body through html-to-markdown which strips
		// <script>/<style>/<nav>/<footer>/<aside>/<header> by default rules.
		html = string(body)
	}
	md, err := htmltomd.ConvertString(html)
	if err != nil {
		return nil, fmt.Errorf("html→markdown: %w", err)
	}
	return []SourceFile{NewSourceFile("index.html", []byte(md))}, nil
}

