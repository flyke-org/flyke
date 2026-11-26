// Package bundle packs a workflow's code directory into a reproducible tar.gz.
package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var skipDirs = map[string]bool{
	".git": true, ".venv": true, "venv": true, "__pycache__": true, "node_modules": true,
	".mypy_cache": true, ".pytest_cache": true, ".ruff_cache": true, ".idea": true, ".vscode": true,
}

// MaxSize mirrors the server's bundle limit.
const MaxSize = 64 << 20

// Pack archives root deterministically (sorted, zeroed mtimes/owners) so the
// same code always yields the same digest.
func Pack(root string) ([]byte, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && (skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() && !strings.HasSuffix(d.Name(), ".pyc") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	var buf bytes.Buffer
	gz, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	tw := tar.NewWriter(gz)
	for _, path := range files {
		rel, _ := filepath.Rel(root, path)
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		hdr := &tar.Header{Name: filepath.ToSlash(rel), Mode: 0o644, Size: info.Size(), Typeflag: tar.TypeReg, Format: tar.FormatPAX}
		if info.Mode()&0o111 != 0 {
			hdr.Mode = 0o755
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		_, err = io.Copy(tw, f)
		f.Close()
		if err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
