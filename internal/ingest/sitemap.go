package ingest

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// SitemapOptions controls sitemap crawl breadth and rate.
type SitemapOptions struct {
	MaxPages          int     // 0 → use default 200
	RequestsPerSecond float64 // 0 → use default 1.0
}

// DefaultSitemapOptions returns the standard sitemap crawler configuration:
// 1 req/s polite rate-limit and a 200-URL breadth cap.
func DefaultSitemapOptions() SitemapOptions {
	return SitemapOptions{MaxPages: 200, RequestsPerSecond: 1.0}
}

type sitemapURL struct {
	Loc string `xml:"loc"`
}

type urlSet struct {
	XMLName xml.Name     `xml:"urlset"`
	URLs    []sitemapURL `xml:"url"`
}

type sitemapIndex struct {
	XMLName  xml.Name     `xml:"sitemapindex"`
	Sitemaps []sitemapURL `xml:"sitemap"`
}

// FetchSitemapFiles crawls a sitemap.xml or sitemap-index. One level of
// sitemap-of-sitemaps recursion is supported; deeper is rejected. Each leaf
// URL becomes a SourceFile via FetchURLFiles, with the URL as RelativePath.
func FetchSitemapFiles(sitemapURL string, urlOpts URLOptions, opts SitemapOptions) ([]SourceFile, error) {
	if opts.MaxPages <= 0 {
		opts.MaxPages = 200
	}
	if opts.RequestsPerSecond <= 0 {
		opts.RequestsPerSecond = 1.0
	}
	urls, err := flattenSitemap(sitemapURL, urlOpts, 0)
	if err != nil {
		return nil, err
	}
	if len(urls) > opts.MaxPages {
		urls = urls[:opts.MaxPages]
	}
	gap := time.Duration(float64(time.Second) / opts.RequestsPerSecond)
	var out []SourceFile
	for i, u := range urls {
		if i > 0 {
			time.Sleep(gap)
		}
		entryFiles, err := FetchURLFiles(u, urlOpts)
		if err != nil {
			continue
		}
		for _, f := range entryFiles {
			f.RelativePath = u
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("sitemap %s yielded zero ingestable URLs", sitemapURL)
	}
	return out, nil
}

func flattenSitemap(u string, urlOpts URLOptions, depth int) ([]string, error) {
	if depth > 1 {
		return nil, fmt.Errorf("sitemap recursion depth > 1: %s", u)
	}
	body, err := fetchRaw(u, urlOpts)
	if err != nil {
		return nil, err
	}
	// Try urlset.
	var us urlSet
	if err := xml.Unmarshal(body, &us); err == nil && len(us.URLs) > 0 {
		out := make([]string, 0, len(us.URLs))
		for _, e := range us.URLs {
			loc := strings.TrimSpace(e.Loc)
			if loc != "" {
				out = append(out, loc)
			}
		}
		return out, nil
	}
	// Try sitemapindex.
	var si sitemapIndex
	if err := xml.Unmarshal(body, &si); err == nil && len(si.Sitemaps) > 0 {
		var out []string
		for _, e := range si.Sitemaps {
			child, err := flattenSitemap(strings.TrimSpace(e.Loc), urlOpts, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, child...)
		}
		return out, nil
	}
	return nil, fmt.Errorf("sitemap %s: not a valid <urlset> or <sitemapindex>", u)
}

func fetchRaw(u string, opts URLOptions) ([]byte, error) {
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: opts.Timeout}
	}
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", opts.UserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching sitemap %s: %w", u, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d fetching %s", resp.StatusCode, u)
	}
	return io.ReadAll(io.LimitReader(resp.Body, opts.MaxBodyBytes))
}
