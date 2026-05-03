package ingest

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	gitignore "github.com/sabhiram/go-gitignore"
)

// isText returns false when the first 512 bytes contain a NUL byte,
// matching the heuristic used by `file(1)` and most text-vs-binary checks.
func isText(data []byte) bool {
	if len(data) == 0 {
		return true
	}
	check := data
	if len(check) > 512 {
		check = check[:512]
	}
	for _, b := range check {
		if b == 0 {
			return false
		}
	}
	return true
}

// Built-in directory denylist — never recurse into these.
var denyDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "target": true,
	"dist": true, "build": true, ".venv": true, "venv": true,
	"__pycache__": true, ".cache": true, ".next": true,
	"coverage": true, ".pytest_cache": true,
}

// Built-in extension denylist — skip these even if "looks like text".
var denyExt = map[string]bool{
	".lock": true, ".min.js": true, ".min.css": true, ".map": true,
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true,
	".ico": true, ".zip": true, ".tar": true, ".gz": true,
	".exe": true, ".dll": true, ".so": true, ".dylib": true,
	".wasm": true, ".class": true, ".jar": true,
}

// Built-in file-name denylist (exact match on basename).
var denyBasenames = map[string]bool{
	"package-lock.json": true,
	"yarn.lock":         true,
	"Cargo.lock":        true,
	"go.sum":            true, // opt-in via ExtraTextExtensions if desired
	".gitignore":        true, // metadata, not content; honored separately when RespectGitignore.
	".gitattributes":    true,
	".dockerignore":     true,
}

// WalkOptions controls how ReadLocalFiles selects files.
//
// Defaults come from DefaultWalkOptions(): 256 KB per-file cap, gitignore
// honored. ExtraSkipGlobs and ExtraTextExtensions / IncludeOnly let callers
// tighten or loosen the rules without touching the package internals.
type WalkOptions struct {
	MaxFileBytes        int64
	RespectGitignore    bool
	ExtraSkipGlobs      []string
	ExtraTextExtensions []string
	IncludeOnly         []string // if non-empty, only files matching these extensions
}

// DefaultWalkOptions returns the standard walker configuration.
func DefaultWalkOptions() WalkOptions {
	return WalkOptions{
		MaxFileBytes:     256 * 1024,
		RespectGitignore: true,
	}
}

// ReadLocalFiles reads a single file or recursively walks a directory,
// applying the skip rules described in WalkOptions. PDFs encountered (by
// extension or %PDF magic) are dispatched to ReadPDF and contribute one
// SourceFile per page; the page's RelativePath is prefixed with the file's
// path inside the walked tree (e.g. "docs/paper.pdf#page-3").
func ReadLocalFiles(path string, opts WalkOptions) ([]SourceFile, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	if !info.IsDir() {
		return readOne(path, filepath.Base(path))
	}
	return walkDirectory(path, opts)
}

func readOne(path, relPath string) ([]SourceFile, error) {
	// PDF dispatch — extension or %PDF magic. Phase D supplies the real ReadPDF.
	if strings.EqualFold(filepath.Ext(path), ".pdf") || hasPDFMagic(path) {
		pages, err := ReadPDF(path)
		if err != nil {
			return nil, err
		}
		for i := range pages {
			pages[i].RelativePath = relPath + "#" + pages[i].RelativePath
		}
		return pages, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if !isText(data) {
		return nil, nil
	}
	return []SourceFile{NewSourceFile(relPath, data)}, nil
}

func hasPDFMagic(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var head [4]byte
	n, _ := f.Read(head[:])
	return n >= 4 && string(head[:4]) == "%PDF"
}

func walkDirectory(root string, opts WalkOptions) ([]SourceFile, error) {
	var ig *gitignore.GitIgnore
	if opts.RespectGitignore {
		if data, err := os.ReadFile(filepath.Join(root, ".gitignore")); err == nil {
			ig = gitignore.CompileIgnoreLines(strings.Split(string(data), "\n")...)
		}
	}

	var out []SourceFile
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // tolerate stat errors mid-walk
		}
		rel, _ := filepath.Rel(root, path)
		if rel == "." {
			return nil
		}
		// Normalize to forward slashes for matching consistency.
		relSlash := filepath.ToSlash(rel)

		if d.IsDir() {
			base := d.Name()
			if denyDirs[base] {
				return filepath.SkipDir
			}
			if ig != nil && ig.MatchesPath(relSlash+"/") {
				return filepath.SkipDir
			}
			return nil
		}

		base := d.Name()
		ext := strings.ToLower(filepath.Ext(base))
		if denyBasenames[base] || denyExt[ext] {
			return nil
		}
		if ig != nil && ig.MatchesPath(relSlash) {
			return nil
		}
		for _, glob := range opts.ExtraSkipGlobs {
			if matched, _ := filepath.Match(glob, base); matched {
				return nil
			}
		}
		if len(opts.IncludeOnly) > 0 {
			ok := false
			for _, want := range opts.IncludeOnly {
				if strings.EqualFold(want, ext) {
					ok = true
					break
				}
			}
			if !ok {
				return nil
			}
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}
		if opts.MaxFileBytes > 0 && info.Size() > opts.MaxFileBytes {
			fmt.Fprintf(os.Stderr, "  WARN skipping %s: %d > max_file_bytes %d\n", relSlash, info.Size(), opts.MaxFileBytes)
			return nil
		}

		// PDF dispatch by extension or magic — pages become individual SourceFiles.
		if ext == ".pdf" || hasPDFMagic(path) {
			pdfFiles, err := ReadPDF(path)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  WARN PDF read failed for %s: %v\n", relSlash, err)
				return nil
			}
			// Prefix page paths with the file's relative path so quotes attribute
			// to "docs/paper.pdf#page-3" not just "page-3".
			for i := range pdfFiles {
				pdfFiles[i].RelativePath = relSlash + "#" + pdfFiles[i].RelativePath
			}
			out = append(out, pdfFiles...)
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if !isText(data) {
			return nil
		}
		out = append(out, NewSourceFile(relSlash, data))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
