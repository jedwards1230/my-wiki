package cli

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jedwards1230/home-wiki/internal/api"
	"github.com/jedwards1230/home-wiki/internal/server"
	"github.com/jedwards1230/home-wiki/internal/vault"
	"github.com/spf13/cobra"
)

func newServeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the wiki server",
		Long:  "Start the wiki HTTP server with static site, API, or MCP transport.",
	}

	// Add subcommands
	cmd.AddCommand(newServeHTTPCmd())

	// Default to http if no subcommand given
	cmd.RunE = runServeHTTP

	// Flags shared with http subcommand
	cmd.Flags().String("port", envOr("WIKI_PORT", "8080"), "HTTP port (env: WIKI_PORT)")
	cmd.Flags().String("public-dir", envOr("WIKI_PUBLIC_DIR", "/data/public"), "path to Quartz public output (env: WIKI_PUBLIC_DIR)")

	return cmd
}

func newServeHTTPCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "http",
		Short: "Start the HTTP server (static site + API)",
		RunE:  runServeHTTP,
	}

	cmd.Flags().String("port", envOr("WIKI_PORT", "8080"), "HTTP port (env: WIKI_PORT)")
	cmd.Flags().String("public-dir", envOr("WIKI_PUBLIC_DIR", "/data/public"), "path to Quartz public output (env: WIKI_PUBLIC_DIR)")

	return cmd
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func runServeHTTP(cmd *cobra.Command, _ []string) error {
	port, _ := cmd.Flags().GetString("port")
	publicDir, _ := cmd.Flags().GetString("public-dir")
	vaultDir, _ := cmd.Root().Flags().GetString("vault")

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	cfg := server.Config{
		PublicDir: publicDir,
		VaultDir:  vaultDir,
		Port:      port,
	}

	publicFS := os.DirFS(publicDir)
	vaultFS := os.DirFS(vaultDir)

	// Build API handler
	v := vault.New(vaultDir)
	apiHandler := api.NewHandler(v)

	srv := server.New(cfg, publicFS, vaultFS,
		server.WithAPIRoutes(apiHandler.RegisterRoutes),
	)

	// Graceful shutdown on SIGTERM/SIGINT
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// Poll for readiness with cancellation
	go func() {
		indexPath := publicDir + "/index.html"
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := os.Stat(indexPath); err == nil {
					srv.SetReady()
					logger.Info("server ready", "publicDir", publicDir)
					return
				}
			}
		}
	}()

	httpSrv := &http.Server{
		Addr:    ":" + port,
		Handler: srv.Handler(),
	}

	// Start server in goroutine, propagate error via channel
	errCh := make(chan error, 1)
	go func() {
		logger.Info("starting wiki-server", "version", version, "port", port, "publicDir", publicDir, "vaultDir", vaultDir)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("server failed: %w", err)
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return httpSrv.Shutdown(shutdownCtx)
}
