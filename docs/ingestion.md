# Ingestion

`llmwiki ingest <source>` accepts every shape below. Content-type sniffing
auto-routes URLs; the `--feed` and `--sitemap` flags override the dispatch
when sniffing isn't enough.

## Single file

```bash
llmwiki ingest ./notes.md
llmwiki ingest ./paper.pdf
```

## Directory

```bash
llmwiki ingest ./my-project/
```

The walker honors `.gitignore` by default and skips `.git`, `node_modules`,
`vendor`, lockfiles, and binary blobs. Override with flags:

```bash
llmwiki ingest ./my-project/ --no-gitignore --include=.md,.go --exclude=vendor/*
```

## URL (HTML page or PDF)

```bash
llmwiki ingest https://example.com/article
```

HTML pages are passed through Readability + html-to-markdown; PDF URLs go
through the PDF text-extraction path.

## GitHub repository

```bash
llmwiki ingest https://github.com/golang/example
```

Shallow-cloned to a temp dir, walked with the same per-file size cap and
`.gitignore` rules as a local directory.

## PDF (file or URL)

```bash
llmwiki ingest ./paper.pdf
llmwiki ingest https://example.com/whitepaper.pdf
```

Text PDFs are extracted page-by-page. Scanned/OCR-only pages are detected
and skipped with a warning — OCR is not supported.

## RSS / Atom / JSON Feed

```bash
llmwiki ingest --feed https://example.com/feed.atom
llmwiki ingest --feed https://example.com/rss.xml --max-pages 20
```

Each feed entry becomes its own `SourceFile` under one `sources` row.
Polite defaults: 1 request/second, cap of 50 entries (override with
`--max-pages` or via `[ingest]` config).

## Sitemap

```bash
llmwiki ingest --sitemap https://example.com/sitemap.xml --max-pages 100
```

Each URL in the sitemap becomes a `SourceFile` via the URL pipeline.
Sitemap-of-sitemaps recursion is supported one level deep. Default cap:
200 pages.

## Re-ingest behavior

Re-running `ingest` against the same source is incremental: per-file
content hashing skips unchanged files. When a file's content changes,
neighbours that were packed in the same prior chunk are re-processed too,
so cross-file synthesis stays stable.

- `--no-rechunk` skips the co-resident pass and only re-processes files
  whose own content changed.
- `--force` re-ingests everything.
