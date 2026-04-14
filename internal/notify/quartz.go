package notify

import (
	"context"
	"log/slog"
	"os/exec"
	"sync"
	"time"
)

// QuartzBuilder triggers one-shot Quartz builds. It serializes builds so
// only one runs at a time and coalesces requests that arrive during a build
// into a single follow-up build.
type QuartzBuilder struct {
	quartzDir string // directory containing the Quartz project
	vaultDir  string // --directory flag value for npx quartz build
	outputDir string // --output flag value for npx quartz build
	logger    *slog.Logger
	command   []string // build command; defaults to ["npx", "quartz", "build"]

	mu           sync.Mutex
	building     bool
	pendingBuild bool
}

// NewQuartzBuilder creates a builder that runs `npx quartz build` in quartzDir
// with the given vault and output directories.
func NewQuartzBuilder(quartzDir, vaultDir, outputDir string, logger *slog.Logger) *QuartzBuilder {
	return &QuartzBuilder{
		quartzDir: quartzDir,
		vaultDir:  vaultDir,
		outputDir: outputDir,
		logger:    logger,
		command:   []string{"npx", "quartz", "build"},
	}
}

// Build triggers a Quartz build. If a build is already in progress the
// request is coalesced — one more build will run after the current one
// finishes. Returns immediately; builds run in a background goroutine.
func (q *QuartzBuilder) Build() {
	q.mu.Lock()
	if q.building {
		q.pendingBuild = true
		q.mu.Unlock()
		return
	}
	q.building = true
	q.mu.Unlock()

	go q.runBuild()
}

func (q *QuartzBuilder) runBuild() {
	for {
		start := time.Now()
		q.logger.Info("quartz build starting")

		args := append(q.command[1:],
			"--directory", q.vaultDir,
			"--output", q.outputDir,
		)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		cmd := exec.CommandContext(ctx, q.command[0], args...)
		cmd.Dir = q.quartzDir

		output, err := cmd.CombinedOutput()
		cancel()

		elapsed := time.Since(start)
		if err != nil {
			out := string(output)
			if len(out) > 2000 {
				out = "..." + out[len(out)-2000:]
			}
			q.logger.Error("quartz build failed", "error", err, "elapsed", elapsed, "output", out)
		} else {
			q.logger.Info("quartz build completed", "elapsed", elapsed)
		}

		q.mu.Lock()
		if q.pendingBuild {
			q.pendingBuild = false
			q.mu.Unlock()
			continue // another build was requested while we were building
		}
		q.building = false
		q.mu.Unlock()
		return
	}
}
