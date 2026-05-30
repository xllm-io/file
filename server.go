package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Server is the HTTP handler for the file service.
type Server struct {
	store   *FileStore
	maxSize int64
	mux     *http.ServeMux
}

// NewServer creates a new Server.
func NewServer(store *FileStore, maxSize int64) *Server {
	s := &Server{
		store:   store,
		maxSize: maxSize,
		mux:     http.NewServeMux(),
	}
	s.mux.HandleFunc("/upload", s.handleUpload)
	s.mux.HandleFunc("/download/", s.handleDownload)
	s.mux.HandleFunc("/info/", s.handleInfo)
	s.mux.HandleFunc("/", s.handleIndex)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// handleIndex serves a simple HTML upload form.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(indexHTML))
}

// handleUpload handles POST /upload – stores the uploaded file and returns JSON with the download URL.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, s.maxSize)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		if strings.Contains(err.Error(), "too large") {
			jsonError(w, "file too large", http.StatusRequestEntityTooLarge)
			return
		}
		jsonError(w, "invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		jsonError(w, "missing 'file' field: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()

	id, err := generateID()
	if err != nil {
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	meta := &FileMeta{
		ID:          id,
		OrigName:    filepath.Base(header.Filename),
		ContentType: header.Header.Get("Content-Type"),
		Size:        header.Size,
		UploadedAt:  time.Now().UTC(),
		ExpiresAt:   time.Now().UTC().Add(s.store.ttl),
	}

	if meta.ContentType == "" {
		meta.ContentType = "application/octet-stream"
	}

	if err := s.store.Save(meta, file); err != nil {
		jsonError(w, "failed to save file: "+err.Error(), http.StatusInternalServerError)
		return
	}

	downloadURL := path.Join("/download", id, meta.OrigName)
	infoURL := path.Join("/info", id)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":           id,
		"name":         meta.OrigName,
		"size":         meta.Size,
		"content_type": meta.ContentType,
		"uploaded_at":  meta.UploadedAt.Format(time.RFC3339),
		"expires_at":   meta.ExpiresAt.Format(time.RFC3339),
		"download_url": downloadURL,
		"info_url":     infoURL,
	})
}

// handleDownload handles GET /download/{id} or GET /download/{id}/{filename}.
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id := extractSegment(r.URL.Path, "/download/")
	if id == "" {
		http.Error(w, "missing file id", http.StatusBadRequest)
		return
	}

	meta, rc, err := s.store.Get(id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if errors.Is(err, ErrExpired) {
			http.Error(w, "file has expired", http.StatusGone)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer rc.Close()

	ct := meta.ContentType
	if ct == "" {
		ct = "application/octet-stream"
	}
	// Try to refine content-type from extension if it was stored as generic.
	if ct == "application/octet-stream" && meta.OrigName != "" {
		if guessed := mime.TypeByExtension(filepath.Ext(meta.OrigName)); guessed != "" {
			ct = guessed
		}
	}

	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", `attachment; filename="`+escapeFilename(meta.OrigName)+`"`)
	if meta.Size > 0 {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", meta.Size))
	}
	w.Header().Set("X-Expires-At", meta.ExpiresAt.Format(time.RFC3339))

	if r.Method == http.MethodHead {
		return
	}
	if _, err := io.Copy(w, rc); err != nil {
		log.Printf("download %s: copy error: %v", r.URL.Path, err)
	}
}

// handleInfo handles GET /info/{id} – returns JSON metadata for the file.
func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id := extractSegment(r.URL.Path, "/info/")
	if id == "" {
		http.Error(w, "missing file id", http.StatusBadRequest)
		return
	}

	meta, err := s.store.Meta(id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if errors.Is(err, ErrExpired) {
			http.Error(w, "file has expired", http.StatusGone)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":           meta.ID,
		"name":         meta.OrigName,
		"size":         meta.Size,
		"content_type": meta.ContentType,
		"uploaded_at":  meta.UploadedAt.Format(time.RFC3339),
		"expires_at":   meta.ExpiresAt.Format(time.RFC3339),
		"download_url": path.Join("/download", meta.ID, meta.OrigName),
	})
}

// helpers

func generateID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// extractSegment strips the prefix and returns the first path segment.
func extractSegment(p, prefix string) string {
	s := strings.TrimPrefix(p, prefix)
	// take only first segment (ignore trailing /filename)
	if idx := strings.Index(s, "/"); idx >= 0 {
		s = s[:idx]
	}
	return s
}

func escapeFilename(name string) string {
	return strings.ReplaceAll(name, `"`, `\"`)
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

const indexHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>临时文件上传</title>
<style>
  body { font-family: sans-serif; max-width: 600px; margin: 60px auto; padding: 0 20px; }
  h1 { color: #333; }
  .form-group { margin: 16px 0; }
  input[type=file] { display: block; margin-top: 8px; }
  button { background: #0078d4; color: #fff; border: none; padding: 10px 24px; border-radius: 4px; cursor: pointer; font-size: 14px; }
  button:hover { background: #005a9e; }
  #result { margin-top: 24px; padding: 16px; background: #f4f4f4; border-radius: 4px; display: none; word-break: break-all; }
  #result a { color: #0078d4; }
  .error { color: #c00; }
</style>
</head>
<body>
<h1>📁 临时文件上传</h1>
<p>上传的文件将在到期后自动删除。</p>
<form id="uploadForm">
  <div class="form-group">
    <label for="fileInput">选择文件：</label>
    <input type="file" id="fileInput" name="file" required>
  </div>
  <button type="submit">上传文件</button>
</form>
<div id="result"></div>
<script>
document.getElementById('uploadForm').addEventListener('submit', async function(e) {
  e.preventDefault();
  const fileInput = document.getElementById('fileInput');
  const result = document.getElementById('result');
  if (!fileInput.files.length) return;
  const formData = new FormData();
  formData.append('file', fileInput.files[0]);
  result.style.display = 'block';
  result.innerHTML = '上传中...';
  try {
    const resp = await fetch('/upload', { method: 'POST', body: formData });
    const data = await resp.json();
    if (!resp.ok) {
      result.innerHTML = '<span class="error">错误：' + (data.error || resp.statusText) + '</span>';
      return;
    }
    result.innerHTML =
      '<strong>上传成功！</strong><br>' +
      '文件名：' + data.name + '<br>' +
      '大小：' + data.size + ' 字节<br>' +
      '过期时间：' + data.expires_at + '<br>' +
      '下载链接：<a href="' + data.download_url + '">' + location.origin + data.download_url + '</a>';
  } catch(err) {
    result.innerHTML = '<span class="error">请求失败：' + err.message + '</span>';
  }
});
</script>
</body>
</html>`
