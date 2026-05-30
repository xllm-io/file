package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"
)

func main() {
	addr := flag.String("addr", ":8080", "HTTP server listen address")
	uploadDir := flag.String("dir", "./uploads", "Directory to store uploaded files")
	maxSize := flag.Int64("max-size", 100, "Maximum file size in MB")
	ttl := flag.Duration("ttl", 24*time.Hour, "Time-to-live for uploaded files (e.g. 24h, 1h30m)")
	flag.Parse()

	store, err := NewFileStore(*uploadDir, *ttl)
	if err != nil {
		log.Fatalf("failed to initialize file store: %v", err)
	}

	srv := NewServer(store, *maxSize*1024*1024)

	fmt.Printf("Temporary file server listening on %s\n", *addr)
	fmt.Printf("Upload directory: %s\n", *uploadDir)
	fmt.Printf("Max file size: %d MB\n", *maxSize)
	fmt.Printf("File TTL: %s\n", *ttl)

	if err := http.ListenAndServe(*addr, srv); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
