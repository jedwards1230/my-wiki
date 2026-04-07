package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jedwards1230/home-wiki/internal/server"
)

var version = "dev"

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	publicDir := envOr("WIKI_PUBLIC_DIR", "/data/public")
	vaultDir := envOr("WIKI_VAULT_DIR", "/data/vault")
	port := envOr("WIKI_PORT", "8080")

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	cfg := server.Config{
		PublicDir: publicDir,
		VaultDir:  vaultDir,
		Port:      port,
	}

	publicFS := os.DirFS(publicDir)
	vaultFS := os.DirFS(vaultDir)

	srv := server.New(cfg, publicFS, vaultFS)

	// Poll for readiness: wait until publicDir/index.html exists
	go func() {
		indexPath := publicDir + "/index.html"
		for {
			if _, err := os.Stat(indexPath); err == nil {
				srv.SetReady()
				logger.Info("server ready", "publicDir", publicDir)
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
	}()

	httpSrv := &http.Server{
		Addr:    ":" + port,
		Handler: srv.Handler(),
	}

	// Graceful shutdown on SIGTERM/SIGINT
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	go func() {
		logger.Info("starting wiki-server", "version", version, "port", port, "publicDir", publicDir, "vaultDir", vaultDir)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown error", "error", err)
	}
}
