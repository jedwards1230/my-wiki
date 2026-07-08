package cli

import (
	"io/fs"
	"log/slog"

	"github.com/jedwards1230/my-wiki/internal/memfs"
	"github.com/jedwards1230/my-wiki/internal/render"
	"github.com/jedwards1230/my-wiki/internal/vault"
)

// buildNativePublicFS returns the *memfs.FS that the native renderer
// populates, the render.Builder that drives it, and an optional closer.
//
// The returned fs.FS is the same *memfs.FS the caller stores Build()
// results into — sharing the instance is intentional: the server reads
// through the FS, the rebuild notifier writes a fresh snapshot via
// FS.Store, and concurrent readers always see a consistent view thanks
// to memfs's atomic pointer swap.
//
// The closer is currently a no-op; reserved so the signature can grow a
// real teardown later without touching call sites. It is a real
// func() error { return nil } (not a bare nil) so callers can invoke it
// unconditionally without a nil-func panic.
func buildNativePublicFS(v *vault.Vault, logger *slog.Logger) (fs.FS, func() error, *render.Builder, error) {
	mf := memfs.New()
	builder := render.NewBuilder(render.BuilderConfig{
		Vault:     v,
		SiteTitle: envOr(EnvSiteTitle, "My Wiki"),
		BaseURL:   envOr(EnvBaseURL, ""),
		Logger:    logger,
	})
	return mf, func() error { return nil }, builder, nil
}
