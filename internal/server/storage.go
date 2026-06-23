package server

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// FileStore saves uploaded files under <root>/files/<id>/<filename>.
// In-progress chunked uploads are staged under <root>/uploads/<id>/.
type FileStore struct {
	root string
}

func NewFileStore(root string) (*FileStore, error) {
	for _, sub := range []string{"files", "uploads"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o755); err != nil {
			return nil, err
		}
	}
	return &FileStore{root: root}, nil
}

// UploadPath returns the staging path for a chunked upload (just the file —
// chunks are appended in order).
func (s *FileStore) UploadPath(uploadID, filename string) (string, error) {
	dir := filepath.Join(s.root, "uploads", uploadID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, filename), nil
}

// FinalizeUpload moves the staged upload into the permanent files dir under
// the given item id, computes the sha256 + size from the file on disk, and
// returns the final path.
func (s *FileStore) FinalizeUpload(uploadID, itemID, filename string) (string, int64, string, error) {
	src := filepath.Join(s.root, "uploads", uploadID, filename)
	f, err := os.Open(src)
	if err != nil {
		return "", 0, "", err
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	f.Close()
	if err != nil {
		return "", 0, "", err
	}
	sum := hex.EncodeToString(h.Sum(nil))

	dstDir := filepath.Join(s.root, "files", itemID)
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return "", 0, "", err
	}
	dst := filepath.Join(dstDir, filename)
	if err := os.Rename(src, dst); err != nil {
		return "", 0, "", fmt.Errorf("rename: %w", err)
	}
	// best-effort cleanup of the now-empty upload dir
	os.Remove(filepath.Join(s.root, "uploads", uploadID))
	return dst, n, sum, nil
}

// AbortUpload removes the staging dir for an upload that won't be finalized.
func (s *FileStore) AbortUpload(uploadID string) error {
	return os.RemoveAll(filepath.Join(s.root, "uploads", uploadID))
}

// Save streams r to a new file, returning the on-disk path, byte count, and sha256.
func (s *FileStore) Save(id, filename string, r io.Reader) (string, int64, string, error) {
	dir := filepath.Join(s.root, "files", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, "", err
	}
	path := filepath.Join(dir, filename)
	f, err := os.Create(path)
	if err != nil {
		return "", 0, "", err
	}
	defer f.Close()

	h := sha256.New()
	mw := io.MultiWriter(f, h)
	n, err := io.Copy(mw, r)
	if err != nil {
		os.Remove(path)
		return "", 0, "", fmt.Errorf("write: %w", err)
	}
	return path, n, hex.EncodeToString(h.Sum(nil)), nil
}

// Open returns a reader for download; caller closes.
func (s *FileStore) Open(path string) (*os.File, error) {
	return os.Open(path)
}

// Delete removes the per-item directory (<root>/files/<id>/) and everything
// in it. No-op if it doesn't exist.
func (s *FileStore) Delete(id string) error {
	dir := filepath.Join(s.root, "files", id)
	return os.RemoveAll(dir)
}
