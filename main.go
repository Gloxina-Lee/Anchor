package main

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"anchor/internal/anchor"
)

func main() {
	dataDir := env("ANCHOR_DATA_DIR", "./data")
	webDir := env("ANCHOR_WEB_DIR", "./web/dist")
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		log.Fatal(err)
	}
	store, err := anchor.OpenStore(filepath.Join(dataDir, "anchor.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()

	server := &http.Server{
		Addr:              env("ANCHOR_LISTEN", ":8080"),
		Handler:           anchor.NewServer(store, webDir, os.Getenv("ANCHOR_INSECURE_COOKIES") == "true"),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       20 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	log.Printf("Anchor listening on %s", server.Addr)
	log.Fatal(server.ListenAndServe())
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
