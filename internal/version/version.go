// Package version provides the application version string.
// Set via ldflags at build time: -ldflags "-X github.com/jedwards1230/home-wiki/internal/version.Value=x.y.z"
package version

// Value is the application version. Overridden at build time via ldflags.
var Value = "dev"
