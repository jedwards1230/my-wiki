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
	"github.com/jedwards1230/home-wiki/internal/mcpserver"
	"github.com/jedwards1230/home-wiki/internal/server"
	"github.com/jedwards1230/home-wiki/internal/vault"
	"github.com/spf13/cobra"
)

func newServeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the wiki server",
		Long:  "Start the wiki HTTP server with static site, API, and optionally MCP transport.\n\nUse --mcp-port to start the MCP server alongside the HTTP server in the same process.\nThe 'serve mcp' subcommand is still available for running the MCP server standalone.",
	}

	// Add subcommands
	cmd.AddCommand(newServeHTTPCmd())
	cmd.AddCommand(newServeMCPCmd())

	// Default to http if no subcommand given
	cmd.RunE = runServeHTTP

	// Flags shared with http subcommand
	cmd.Flags().String("port", envOr("WIKI_PORT", "8080"), "HTTP port (env: WIKI_PORT)")
	cmd.Flags().String("public-dir", envOr("WIKI_PUBLIC_DIR", "/data/public"), "path to Quartz public output (env: WIKI_PUBLIC_DIR)")
	cmd.Flags().Int("mcp-port", 0, "MCP server port; when non-zero, starts MCP alongside HTTP (env: WIKI_MCP_PORT)")

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
	cmd.Flags().Int("mcp-port", 0, "MCP server port; when non-zero, starts MCP alongside HTTP (env: WIKI_MCP_PORT)")

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
	mcpPort, _ := cmd.Flags().GetInt("mcp-port")

	// Support env var for mcp-port when flag is at default
	if mcpPort == 0 {
		if envVal := os.Getenv("WIKI_MCP_PORT"); envVal != "" {
			fmt.Sscanf(envVal, "%d", &mcpPort)
		}
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

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

	srv := server.New(cfg, publicFS, vaultFS, logger,
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

	// Collect servers that need graceful shutdown
	servers := []*http.Server{httpSrv}

	// Start HTTP server
	errCh := make(chan error, 2)
	go func() {
		logger.Info("starting wiki-server", "version", version, "port", port, "publicDir", publicDir, "vaultDir", vaultDir)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("HTTP server failed: %w", err)
		}
	}()

	// Optionally start MCP server in the same process
	if mcpPort > 0 {
		mcpSrv := mcpserver.New(v)
		httpTransport := mcpserver.NewStreamableHTTPServer(mcpSrv)

		mux := http.NewServeMux()
		mux.Handle("/mcp", httpTransport)
		mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte("ok"))
		})

		mcpHTTPSrv := &http.Server{
			Addr:    fmt.Sprintf(":%d", mcpPort),
			Handler: mux,
		}
		servers = append(servers, mcpHTTPSrv)

		go func() {
			logger.Info("starting wiki MCP server", "port", mcpPort, "vaultDir", vaultDir)
			if err := mcpHTTPSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				errCh <- fmt.Errorf("MCP server failed: %w", err)
			}
		}()
	}

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Shut down all servers
	var firstErr error
	for _, s := range servers {
		if err := s.Shutdown(shutdownCtx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func newServeMCPCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Start a standalone MCP server (streamable-http transport)",
		RunE:  runServeMCP,
	}

	cmd.Flags().String("port", envOr("WIKI_MCP_PORT", "8081"), "MCP server port (env: WIKI_MCP_PORT)")

	return cmd
}

func runServeMCP(cmd *cobra.Command, _ []string) error {
	port, _ := cmd.Flags().GetString("port")
	vaultDir, _ := cmd.Root().Flags().GetString("vault")

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	v := vault.New(vaultDir)
	mcpSrv := mcpserver.New(v)
	httpTransport := mcpserver.NewStreamableHTTPServer(mcpSrv)

	mux := http.NewServeMux()
	mux.Handle("/mcp", httpTransport)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	})

	httpSrv := &http.Server{
		Addr:    ":" + port,
		Handler: mux,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("starting wiki MCP server", "port", port, "vaultDir", vaultDir)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("MCP server failed: %w", err)
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	logger.Info("shutting down MCP server")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return httpSrv.Shutdown(shutdownCtx)
}
