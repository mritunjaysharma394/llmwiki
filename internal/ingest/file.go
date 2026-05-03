package ingest

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

var textExtensions = map[string]bool{
	".md": true, ".txt": true, ".go": true, ".py": true, ".js": true,
	".ts": true, ".rs": true, ".c": true, ".cpp": true, ".h": true,
	".java": true, ".rb": true, ".sh": true, ".yaml": true, ".yml": true,
	".toml": true, ".json": true, ".html": true, ".css": true, ".rst": true,
}

func ReadLocal(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", path, err)
	}
	if info.IsDir() {
		return readDir(path)
	}
	return readFile(path)
}

func readDir(dir string) (string, error) {
	var sb strings.Builder
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && d.Name() != "." {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if !textExtensions[ext] {
			return nil
		}
		content, err := readFile(path)
		if err != nil {
			return nil
		}
		sb.WriteString(fmt.Sprintf("=== %s ===\n%s\n\n", path, content))
		return nil
	})
	if err != nil {
		return "", err
	}
	return sb.String(), nil
}

func readFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if !isText(data) {
		return "", nil
	}
	return string(data), nil
}

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
