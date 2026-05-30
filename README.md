# file

临时文件上传、下载工具 – a lightweight HTTP service for temporary file sharing.

## Features

- **Upload** files via `POST /upload` (multipart/form-data)
- **Download** files via `GET /download/{id}` or `GET /download/{id}/{filename}`
- **Inspect metadata** via `GET /info/{id}`
- Files are automatically **deleted after a configurable TTL** (default 24 h)
- Simple **web UI** served at `/`
- Path-traversal protection on all file IDs
- No external dependencies – pure Go standard library

## Quick Start

```bash
go run .
```

Open <http://localhost:8080> in your browser to use the web UI.

## CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-addr` | `:8080` | Listen address |
| `-dir` | `./uploads` | Directory where files are stored |
| `-max-size` | `100` | Maximum upload size in MB |
| `-ttl` | `24h` | File time-to-live (e.g. `1h`, `30m`, `48h`) |

## API

### Upload

```
POST /upload
Content-Type: multipart/form-data

file=<file data>
```

**Response (201 Created)**

```json
{
  "id":           "0123456789abcdef0123456789abcdef",
  "name":         "hello.txt",
  "size":         13,
  "content_type": "text/plain",
  "uploaded_at":  "2024-01-01T00:00:00Z",
  "expires_at":   "2024-01-02T00:00:00Z",
  "download_url": "/download/0123456789abcdef0123456789abcdef/hello.txt",
  "info_url":     "/info/0123456789abcdef0123456789abcdef"
}
```

### Download

```
GET /download/{id}
GET /download/{id}/{filename}
```

Returns the file with appropriate `Content-Type` and `Content-Disposition` headers.
Returns **410 Gone** if the file has expired.

### Info

```
GET /info/{id}
```

Returns the same JSON metadata as the upload response (without `info_url`).

## Example (curl)

```bash
# Upload
curl -F "file=@/path/to/file.txt" http://localhost:8080/upload

# Download
curl -O http://localhost:8080/download/<id>/file.txt
```

## Building

```bash
go build -o fileserver .
./fileserver -addr :9000 -ttl 1h
```

## Testing

```bash
go test ./...
```
