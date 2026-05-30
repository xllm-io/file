package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func newTestServer(t *testing.T, ttl time.Duration) (*Server, func()) {
	t.Helper()
	dir := t.TempDir()
	store, err := NewFileStore(dir, ttl)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	srv := NewServer(store, 10<<20) // 10 MB max
	return srv, func() { os.RemoveAll(dir) }
}

func uploadFile(t *testing.T, srv *Server, filename, content string) map[string]interface{} {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	fmt.Fprint(fw, content)
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload: expected 201, got %d – body: %s", resp.StatusCode, w.Body.String())
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	return result
}

func TestUploadAndDownload(t *testing.T) {
	srv, cleanup := newTestServer(t, 24*time.Hour)
	defer cleanup()

	content := "hello, world!"
	result := uploadFile(t, srv, "test.txt", content)

	id, _ := result["id"].(string)
	if id == "" {
		t.Fatal("upload response missing id")
	}
	downloadURL, _ := result["download_url"].(string)
	if downloadURL == "" {
		t.Fatal("upload response missing download_url")
	}

	// Download the file.
	req := httptest.NewRequest(http.MethodGet, downloadURL, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("download: expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if string(body) != content {
		t.Errorf("download content mismatch: got %q, want %q", string(body), content)
	}

	cd := resp.Header.Get("Content-Disposition")
	if !strings.Contains(cd, "test.txt") {
		t.Errorf("Content-Disposition missing filename: %s", cd)
	}
}

func TestInfoEndpoint(t *testing.T) {
	srv, cleanup := newTestServer(t, 24*time.Hour)
	defer cleanup()

	result := uploadFile(t, srv, "info.txt", "data")
	id := result["id"].(string)

	req := httptest.NewRequest(http.MethodGet, "/info/"+id, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("info: expected 200, got %d", resp.StatusCode)
	}

	var meta map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&meta)
	if meta["id"] != id {
		t.Errorf("info id mismatch: got %v, want %v", meta["id"], id)
	}
	if meta["name"] != "info.txt" {
		t.Errorf("info name mismatch: got %v", meta["name"])
	}
}

func TestDownloadNotFound(t *testing.T) {
	srv, cleanup := newTestServer(t, 24*time.Hour)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/download/"+strings.Repeat("a", 32), nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestDownloadExpired(t *testing.T) {
	srv, cleanup := newTestServer(t, 1*time.Millisecond)
	defer cleanup()

	result := uploadFile(t, srv, "expire.txt", "temporary")
	id := result["id"].(string)
	downloadURL := result["download_url"].(string)

	// Wait for the file to expire.
	time.Sleep(10 * time.Millisecond)

	req := httptest.NewRequest(http.MethodGet, downloadURL, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusGone {
		t.Errorf("expected 410, got %d (id=%s)", w.Code, id)
	}
}

func TestUploadMethodNotAllowed(t *testing.T) {
	srv, cleanup := newTestServer(t, 24*time.Hour)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/upload", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestUploadMissingFile(t *testing.T) {
	srv, cleanup := newTestServer(t, 24*time.Hour)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader(""))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=----boundary")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestIndexPage(t *testing.T) {
	srv, cleanup := newTestServer(t, 24*time.Hour)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("index: expected 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("index: expected text/html, got %s", ct)
	}
}

func TestExtractSegment(t *testing.T) {
	cases := []struct {
		path, prefix, want string
	}{
		{"/download/abc123/file.txt", "/download/", "abc123"},
		{"/download/abc123", "/download/", "abc123"},
		{"/info/xyz", "/info/", "xyz"},
	}
	for _, c := range cases {
		got := extractSegment(c.path, c.prefix)
		if got != c.want {
			t.Errorf("extractSegment(%q, %q) = %q, want %q", c.path, c.prefix, got, c.want)
		}
	}
}

func TestIsValidID(t *testing.T) {
	valid := []string{
		strings.Repeat("a", 32),
		strings.Repeat("f", 32),
		strings.Repeat("0", 32),
		"0123456789abcdef0123456789abcdef",
	}
	invalid := []string{
		"",
		"short",
		strings.Repeat("g", 32), // 'g' is not hex
		strings.Repeat("A", 32), // uppercase not allowed
		"../etc/passwd" + strings.Repeat("a", 18),
	}
	for _, id := range valid {
		if !isValidID(id) {
			t.Errorf("isValidID(%q) = false, want true", id)
		}
	}
	for _, id := range invalid {
		if isValidID(id) {
			t.Errorf("isValidID(%q) = true, want false", id)
		}
	}
}

// TestIndexRebuildOnRestart verifies that files uploaded before a restart are
// still accessible after the store is re-created (index rebuilt from disk).
func TestIndexRebuildOnRestart(t *testing.T) {
	dir := t.TempDir()

	// First store: upload a file.
	store1, err := NewFileStore(dir, 24*time.Hour)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	srv1 := NewServer(store1, 10<<20)
	result := uploadFile(t, srv1, "persist.txt", "persistent content")
	id := result["id"].(string)
	downloadURL := result["download_url"].(string)

	// Second store using the same dir: simulates a server restart.
	store2, err := NewFileStore(dir, 24*time.Hour)
	if err != nil {
		t.Fatalf("NewFileStore (restart): %v", err)
	}
	srv2 := NewServer(store2, 10<<20)

	req := httptest.NewRequest(http.MethodGet, downloadURL, nil)
	w := httptest.NewRecorder()
	srv2.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("download after restart: expected 200, got %d (id=%s)", w.Code, id)
	}
	body, _ := io.ReadAll(w.Result().Body)
	if string(body) != "persistent content" {
		t.Errorf("content mismatch after restart: got %q", string(body))
	}
}

