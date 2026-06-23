// Command clipsrv runs the central clipboard/file sync server.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/sherifhamad/shixo-msn/internal/server"
)

func main() {
	var cfgPath string
	flag.StringVar(&cfgPath, "config", "/etc/clip/config.toml", "path to TOML config")
	flag.Parse()

	cfg, err := loadConfig(cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if cfg.Token == "" {
		log.Fatal("config: token must be set")
	}
	if cfg.DataDir == "" {
		cfg.DataDir = "/var/lib/clip"
	}
	if cfg.Listen == "" {
		cfg.Listen = ":6303"
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		log.Fatalf("data dir: %v", err)
	}

	db, err := server.OpenDB(filepath.Join(cfg.DataDir, "clip.db"))
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer db.Close()

	store, err := server.NewFileStore(cfg.DataDir)
	if err != nil {
		log.Fatalf("store: %v", err)
	}

	hub := server.NewHub()
	srv := server.New(cfg, db, store, hub)

	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 30 * time.Second,
		// no write timeout: large uploads/downloads need unbounded time
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("clipsrv listening on %s (data: %s)", cfg.Listen, cfg.DataDir)
		var err error
		if cfg.TLSCertFile != "" && cfg.TLSKeyFile != "" {
			err = httpSrv.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile)
		} else {
			err = httpSrv.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			log.Fatalf("serve: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	httpSrv.Shutdown(shutdownCtx)
}

func loadConfig(path string) (server.Config, error) {
	var c server.Config
	_, err := toml.DecodeFile(path, &c)
	return c, err
}
