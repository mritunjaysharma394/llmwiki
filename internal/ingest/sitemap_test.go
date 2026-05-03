package ingest

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// serveSitemapAndPages serves the four sitemap fixtures and ten leaf HTML
// pages (/p-1../p-5 and /q-1../q-5) needed by the sitemap tests. Fixture URLs
// of the form "https://example.test" are rewritten to the test server's base
// URL at serve time so <loc> entries resolve back to this server.
func serveSitemapAndPages(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var srv *httptest.Server
	for _, name := range []string{"flat.xml", "index.xml", "nested.xml", "malformed.xml"} {
		path := name
		body, err := os.ReadFile("testdata/sitemaps/" + path)
		if err != nil {
			t.Fatal(err)
		}
		mux.HandleFunc("/"+path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/xml")
			rewritten := bytes.ReplaceAll(body, []byte("https://example.test"), []byte(srv.URL))
			w.Write(rewritten)
		})
	}
	for _, prefix := range []string{"/p-", "/q-"} {
		prefix := prefix
		for i := 1; i <= 5; i++ {
			i := i
			mux.HandleFunc(fmt.Sprintf("%s%d", prefix, i), func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				fmt.Fprintf(w, "<html><body><article><p>%s%d body</p></article></body></html>", prefix, i)
			})
		}
	}
	srv = httptest.NewServer(mux)
	return srv
}

func TestFetchSitemapFilesFlat(t *testing.T) {
	srv := serveSitemapAndPages(t)
	defer srv.Close()
	files, err := FetchSitemapFiles(srv.URL+"/flat.xml", DefaultURLOptions(), DefaultSitemapOptions())
	if err != nil {
		t.Fatalf("FetchSitemapFiles: %v", err)
	}
	if len(files) != 5 {
		t.Errorf("got %d, want 5", len(files))
	}
}

func TestFetchSitemapFilesNested(t *testing.T) {
	srv := serveSitemapAndPages(t)
	defer srv.Close()
	files, err := FetchSitemapFiles(srv.URL+"/index.xml", DefaultURLOptions(), DefaultSitemapOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Errorf("nested got %d, want 3", len(files))
	}
}

func TestFetchSitemapFilesMaxPagesCap(t *testing.T) {
	srv := serveSitemapAndPages(t)
	defer srv.Close()
	opts := DefaultSitemapOptions()
	opts.MaxPages = 2
	files, _ := FetchSitemapFiles(srv.URL+"/flat.xml", DefaultURLOptions(), opts)
	if len(files) > 2 {
		t.Errorf("cap not honored: got %d", len(files))
	}
}

func TestFetchSitemapFilesMalformed(t *testing.T) {
	srv := serveSitemapAndPages(t)
	defer srv.Close()
	if _, err := FetchSitemapFiles(srv.URL+"/malformed.xml", DefaultURLOptions(), DefaultSitemapOptions()); err == nil {
		t.Error("expected error for malformed sitemap")
	}
}
