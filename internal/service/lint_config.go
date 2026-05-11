package service

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v2"
)

// LintConfig is the externalized, schema-coupled portion of the lint rules.
// Loaded from meta/lint-config.yaml in the vault; fields left unset fall back
// to defaults (matching the values documented in meta/schema.md).
//
// Keep this struct narrow: only values that are likely to drift between the
// schema and the code belong here. Things that are fundamental to the file
// format (e.g. YAML frontmatter parsing, wikilink syntax) stay in code.
type LintConfig struct {
	Clippings ClippingsConfig `yaml:"clippings"`
	Stub      StubConfig      `yaml:"stub"`
}

// ClippingsConfig governs the `clippings` lint check.
type ClippingsConfig struct {
	// Tag is the canonical singular tag that identifies a clipping
	// descriptor. Schema default: "clipping".
	Tag string `yaml:"tag"`
	// RawPathPrefix is the path under raw/ where verbatim clipping bodies
	// live. Descriptors must contain at least one link including this
	// substring in the body. Schema default: "raw/clippings/".
	RawPathPrefix string `yaml:"raw_path_prefix"`
}

// StubConfig governs the `stub` lint check that surfaces stray vault-root
// markdown files that look like Obsidian-created placeholders (created
// when a user clicks a wikilink to a non-existent page).
type StubConfig struct {
	// MinIdleSeconds is the mtime cooldown — a file isn't flagged as a
	// stub until it has been untouched for at least this long. Protects
	// against thrashing on a file the user is actively editing. Schema
	// default: 3600 (1 hour).
	MinIdleSeconds int `yaml:"min_idle_seconds"`
}

// DefaultLintConfig returns the config used when meta/lint-config.yaml is
// absent. Default values mirror meta/schema.md.
func DefaultLintConfig() LintConfig {
	return LintConfig{
		Clippings: ClippingsConfig{
			Tag:           "clipping",
			RawPathPrefix: "raw/clippings/",
		},
		Stub: StubConfig{
			MinIdleSeconds: 3600,
		},
	}
}

// LintConfigPath returns the conventional location of the lint config file
// inside a vault.
func LintConfigPath(vaultDir string) string {
	return filepath.Join(vaultDir, "meta", "lint-config.yaml")
}

// LoadLintConfig reads meta/lint-config.yaml from the vault and overlays
// any set fields on top of DefaultLintConfig(). Missing file is not an
// error — defaults apply. A present-but-malformed file returns the
// defaults plus the parse error so callers can surface it.
//
// Per-field merge: if the file omits a field (or sets it to the zero
// value), the default wins. This lets a partial config file override
// only the values you care about.
func LoadLintConfig(vaultDir string) (LintConfig, error) {
	cfg := DefaultLintConfig()
	path := LintConfigPath(vaultDir)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("read %s: %w", path, err)
	}
	var fromFile LintConfig
	if err := yaml.UnmarshalStrict(data, &fromFile); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	if fromFile.Clippings.Tag != "" {
		cfg.Clippings.Tag = fromFile.Clippings.Tag
	}
	if fromFile.Clippings.RawPathPrefix != "" {
		cfg.Clippings.RawPathPrefix = fromFile.Clippings.RawPathPrefix
	}
	if fromFile.Stub.MinIdleSeconds > 0 {
		cfg.Stub.MinIdleSeconds = fromFile.Stub.MinIdleSeconds
	}
	return cfg, nil
}
