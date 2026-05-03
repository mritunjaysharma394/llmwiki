package ingest

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// serveFeedAndPosts returns an httptest.Server that serves a single feed
// fixture at "/" with the given content-type, plus three "post-N" routes
// returning HTML bodies. Fixture URLs of the form "https://example.test" are
// rewritten to the test server's base URL at serve time so feed entries
// resolve back to this server.
func serveFeedAndPosts(t *testing.T, fixturePath, contentType string) *httptest.Server {
	t.Helper()
	body, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		rewritten := bytes.ReplaceAll(body, []byte("https://example.test"), []byte(srv.URL))
		w.Write(rewritten)
	})
	for i := 1; i <= 3; i++ {
		i := i
		mux.HandleFunc(fmt.Sprintf("/post-%d", i), func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, "<html><body><article><h1>Post %d</h1><p>Body of post %d.</p></article></body></html>", i, i)
		})
	}
	srv = httptest.NewServer(mux)
	return srv
}

func TestFetchFeedFilesRSS(t *testing.T) {
	srv := serveFeedAndPosts(t, "testdata/feeds/sample.rss.xml", "application/rss+xml")
	defer srv.Close()
	files, err := FetchFeedFiles(srv.URL+"/", DefaultURLOptions(), DefaultFeedOptions())
	if err != nil {
		t.Fatalf("FetchFeedFiles: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("got %d entries, want 3", len(files))
	}
	for _, f := range files {
		if !strings.HasPrefix(f.RelativePath, srv.URL+"/post-") {
			t.Errorf("entry path %q not anchored to entry URL", f.RelativePath)
		}
	}
}

func TestFetchFeedFilesAtom(t *testing.T) {
	srv := serveFeedAndPosts(t, "testdata/feeds/sample.atom.xml", "application/atom+xml")
	defer srv.Close()
	files, err := FetchFeedFiles(srv.URL+"/", DefaultURLOptions(), DefaultFeedOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Errorf("atom: got %d, want 3", len(files))
	}
}

func TestFetchFeedFilesJSON(t *testing.T) {
	srv := serveFeedAndPosts(t, "testdata/feeds/sample.json", "application/json")
	defer srv.Close()
	files, err := FetchFeedFiles(srv.URL+"/", DefaultURLOptions(), DefaultFeedOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Errorf("jsonfeed: got %d, want 3", len(files))
	}
}

func TestFetchFeedFilesCapAtMaxEntries(t *testing.T) {
	srv := serveFeedAndPosts(t, "testdata/feeds/sample.rss.xml", "application/rss+xml")
	defer srv.Close()
	opts := DefaultFeedOptions()
	opts.MaxEntries = 2
	files, err := FetchFeedFiles(srv.URL+"/", DefaultURLOptions(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Errorf("MaxEntries=2 honored? got %d files", len(files))
	}
}

func TestFetchFeedFilesMalformed(t *testing.T) {
	srv := serveFeedAndPosts(t, "testdata/feeds/malformed.xml", "application/rss+xml")
	defer srv.Close()
	if _, err := FetchFeedFiles(srv.URL+"/", DefaultURLOptions(), DefaultFeedOptions()); err == nil {
		t.Error("expected error for malformed feed")
	}
}
