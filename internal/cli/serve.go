package cli

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jedwards1230/home-wiki/internal/server"
	"github.com/spf13/cobra"
)

func newServeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the wiki HTTP server",
		RunE:  runServe,
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

func runServe(cmd *cobra.Command, _ []string) error {
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
	return httpSrv.Shutdown(shutdownCtx)
}
