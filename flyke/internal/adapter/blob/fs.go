// Package blob stores content-addressed blobs (code bundles) on a filesystem.
package blob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

var digestRe = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

type FS struct{ dir string }

func NewFS(dir string) (*FS, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &FS{dir: dir}, nil
}

// MaxBundleSize bounds uploads.
const MaxBundleSize = 64 << 20

func (f *FS) Put(_ context.Context, r io.Reader) (string, int64, error) {
	tmp, err := os.CreateTemp(f.dir, "upload-*")
	if err != nil {
		return "", 0, err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(r, MaxBundleSize+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", 0, err
	}
	if n > MaxBundleSize {
		return "", 0, fmt.Errorf("bundle exceeds %d bytes", MaxBundleSize)
	}
	digest := "sha256:" + hex.EncodeToString(h.Sum(nil))
	if err := os.Rename(tmp.Name(), f.path(digest)); err != nil {
		return "", 0, err
	}
	return digest, n, nil
}

func (f *FS) Get(_ context.Context, digest string) (io.ReadCloser, error) {
	if !digestRe.MatchString(digest) {
		return nil, fmt.Errorf("invalid digest %q", digest)
	}
	return os.Open(f.path(digest))
}

// Size returns a stored bundle's size in bytes.
func (f *FS) Size(_ context.Context, digest string) (int64, error) {
	if !digestRe.MatchString(digest) {
		return 0, fmt.Errorf("invalid digest %q", digest)
	}
	fi, err := os.Stat(f.path(digest))
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

func (f *FS) path(digest string) string {
	return filepath.Join(f.dir, digest[len("sha256:"):]+".tar.gz")
}
