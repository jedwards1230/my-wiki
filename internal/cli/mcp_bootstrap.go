package cli

import (
	"log/slog"

	"github.com/jedwards1230/home-wiki/internal/dispatch"
	"github.com/jedwards1230/home-wiki/internal/notify"
	"github.com/jedwards1230/home-wiki/internal/service"
	"github.com/jedwards1230/home-wiki/internal/vault"
)

// buildPageService constructs a PageService wired with the standard mutation
// pipeline used by every MCP transport (and the REST API): activity logging,
// optional rebuild notification, and optional webhook dispatch routing.
//
// notifier may be nil (e.g. stdio mode) — the activity callback skips dirty
// marking when notifier is nil. dispatchRouter may be nil when webhooks are
// not configured.
func buildPageService(
	v *vault.Vault,
	notifier *notify.RebuildNotifier,
	dispatchRouter *dispatch.EventRouter,
	logger *slog.Logger,
) *service.PageService {
	activitySvc := service.NewActivityService(v.Storage)
	onMutation := makeActivityCallback(activitySvc, notifier, v.Dir, logger)
	if dispatchRouter != nil {
		onMutation = mutationAdapter(dispatchRouter, onMutation)
	}
	return service.NewPageService(v.Storage,
		service.WithExcludedDirs(v.ExcludedDirs),
		service.WithOnMutation(onMutation),
	)
}
