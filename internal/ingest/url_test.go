package ingest

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchURLHTML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><head><title>Hello</title></head><body>
<nav>navvy navvy</nav>
<header>headery</header>
<main><article>
<h1>Real Title</h1>
<p>Body paragraph that should survive readability extraction across this whole sentence.</p>
<p>Another paragraph with enough words to satisfy the readability heuristic for content density.</p>
</article></main>
<footer>footery</footer>
<script>var noise = 1;</script>
</body></html>`))
	}))
	defer srv.Close()

	files, err := FetchURLFiles(srv.URL, DefaultURLOptions())
	if err != nil {
		t.Fatalf("FetchURLFiles: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("got %d files, want 1", len(files))
	}
	if files[0].RelativePath != "index.html" {
		t.Errorf("RelativePath = %q, want %q", files[0].RelativePath, "index.html")
	}
	body := files[0].Content
	if !strings.Contains(body, "Body paragraph") {
		t.Errorf("article body missing: %q", body)
	}
	for _, noise := range []string{"navvy navvy", "headery", "footery", "var noise"} {
		if strings.Contains(body, noise) {
			t.Errorf("noise %q leaked into article: %q", noise, body)
		}
	}
}

func TestFetchURLPDF(t *testing.T) {
	pdfBytes := buildSimplePDF(
		"alpha beta gamma delta epsilon zeta eta theta iota kappa",
		"lambda mu nu xi omicron pi rho sigma tau upsilon phi chi",
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(pdfBytes)
	}))
	defer srv.Close()

	files, err := FetchURLFiles(srv.URL, DefaultURLOptions())
	if err != nil {
		t.Fatalf("FetchURLFiles: %v", err)
	}
	if len(files) < 1 {
		t.Fatal("got 0 pages from PDF URL")
	}
	if !strings.HasPrefix(files[0].RelativePath, "page-") {
		t.Errorf("expected page-N relative path, got %q", files[0].RelativePath)
	}
	if files[0].RelativePath != "page-1" {
		t.Errorf("first page RelativePath = %q, want page-1", files[0].RelativePath)
	}
}

func TestFetchURLPlainText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("hello world\n"))
	}))
	defer srv.Close()

	files, err := FetchURLFiles(srv.URL, DefaultURLOptions())
	if err != nil {
		t.Fatalf("FetchURLFiles: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("got %d files, want 1", len(files))
	}
	if !strings.Contains(files[0].Content, "hello world") {
		t.Errorf("content = %q, want hello world", files[0].Content)
	}
}

func TestFetchURL5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
	}))
	defer srv.Close()

	if _, err := FetchURLFiles(srv.URL, DefaultURLOptions()); err == nil {
		t.Error("expected error for 503")
	}
}

func TestFetchURLBodyOverLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(make([]byte, 10*1024*1024)) // 10 MB
	}))
	defer srv.Close()

	opts := DefaultURLOptions()
	opts.MaxBodyBytes = 5 * 1024 * 1024
	files, err := FetchURLFiles(srv.URL, opts)
	if err != nil {
		// Implementations may return an error when the body exceeds the limit;
		// that satisfies the contract.
		return
	}
	if files[0].ByteSize > opts.MaxBodyBytes {
		t.Errorf("body not capped: got %d bytes, max %d", files[0].ByteSize, opts.MaxBodyBytes)
	}
}

func TestFetchURLUnsupportedContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte{0, 1, 2, 3})
	}))
	defer srv.Close()

	_, err := FetchURLFiles(srv.URL, DefaultURLOptions())
	if err == nil {
		t.Error("expected error for unsupported content-type")
	}
	if err != nil && !strings.Contains(err.Error(), "unsupported content-type") {
		t.Errorf("expected 'unsupported content-type' in error, got: %v", err)
	}
}
