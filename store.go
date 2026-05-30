package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Sentinel errors returned by FileStore.
var (
	ErrNotFound = errors.New("file not found")
	ErrExpired  = errors.New("file has expired")
)

// FileMeta holds metadata about an uploaded file.
type FileMeta struct {
	ID          string    `json:"id"`
	OrigName    string    `json:"orig_name"`
	ContentType string    `json:"content_type"`
	Size        int64     `json:"size"`
	UploadedAt  time.Time `json:"uploaded_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// FileStore manages storage and retrieval of uploaded files on disk.
type FileStore struct {
	dir string
	ttl time.Duration
	mu  sync.RWMutex
}

// NewFileStore creates and initialises a FileStore that persists files to dir.
// A background goroutine runs periodically to delete expired files.
func NewFileStore(dir string, ttl time.Duration) (*FileStore, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	fs := &FileStore{dir: dir, ttl: ttl}
	go fs.cleanupLoop()
	return fs, nil
}

// Save writes the file content and its metadata to disk.
func (fs *FileStore) Save(meta *FileMeta, r io.Reader) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.safeItemDir(meta.ID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}

	// Write file data.
	dataPath := filepath.Join(dir, "data")
	f, err := os.OpenFile(dataPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, r)
	f.Close()
	if err != nil {
		os.RemoveAll(dir)
		return err
	}
	// Update size from actual bytes written if header lied.
	if meta.Size == 0 {
		meta.Size = n
	}

	// Write metadata.
	metaPath := filepath.Join(dir, "meta.json")
	mf, err := os.OpenFile(metaPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o640)
	if err != nil {
		os.RemoveAll(dir)
		return err
	}
	err = json.NewEncoder(mf).Encode(meta)
	mf.Close()
	if err != nil {
		os.RemoveAll(dir)
		return err
	}

	return nil
}

// Meta loads and returns the metadata for the given file ID.
func (fs *FileStore) Meta(id string) (*FileMeta, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	return fs.loadMeta(id)
}

// Get returns the metadata and a ReadCloser for the file content.
// The caller must close the returned ReadCloser.
func (fs *FileStore) Get(id string) (*FileMeta, io.ReadCloser, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	meta, err := fs.loadMeta(id)
	if err != nil {
		return nil, nil, err
	}

	dir, err := fs.safeItemDir(id)
	if err != nil {
		return nil, nil, err
	}
	dataPath := filepath.Join(dir, "data")
	f, err := os.Open(dataPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, err
	}
	return meta, f, nil
}

// cleanupLoop periodically removes expired files.
func (fs *FileStore) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		fs.deleteExpired()
	}
}

func (fs *FileStore) deleteExpired() {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	entries, err := os.ReadDir(fs.dir)
	if err != nil {
		return
	}
	now := time.Now().UTC()
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		id := entry.Name()
		meta, err := fs.loadMeta(id)
		if err != nil {
			continue
		}
		if now.After(meta.ExpiresAt) {
			dir, err := fs.safeItemDir(id)
			if err == nil {
				os.RemoveAll(dir)
			}
		}
	}
}

func (fs *FileStore) loadMeta(id string) (*FileMeta, error) {
	dir, err := fs.safeItemDir(id)
	if err != nil {
		return nil, ErrNotFound
	}
	metaPath := filepath.Join(dir, "meta.json")
	f, err := os.Open(metaPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	defer f.Close()

	var meta FileMeta
	if err := json.NewDecoder(f).Decode(&meta); err != nil {
		return nil, err
	}

	if time.Now().UTC().After(meta.ExpiresAt) {
		return nil, ErrExpired
	}

	return &meta, nil
}

// safeItemDir returns the per-file directory only when the resolved path is
// confirmed to be inside fs.dir, preventing any path-traversal attack.
func (fs *FileStore) safeItemDir(id string) (string, error) {
	if !isValidID(id) {
		return "", ErrNotFound
	}
	base, err := filepath.Abs(fs.dir)
	if err != nil {
		return "", err
	}
	target := filepath.Join(base, id)
	// Ensure the resolved path is still inside the base directory.
	if !strings.HasPrefix(target+string(filepath.Separator), base+string(filepath.Separator)) {
		return "", ErrNotFound
	}
	return target, nil
}

// isValidID checks that the id is a 32-character hex string to prevent path traversal.
func isValidID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
