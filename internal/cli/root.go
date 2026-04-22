package cli

import (
	"os"

	"github.com/spf13/cobra"
)

var version = "dev"

// SetVersion sets the version string for the CLI.
func SetVersion(v string) {
	version = v
}

// GetVersion returns the current version string.
func GetVersion() string {
	return version
}

// NewRootCmd creates the root wiki-server command with all subcommands.
func NewRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "wiki-server",
		Short:        "Wiki server and vault management CLI",
		Long:         "wiki-server provides an HTTP server for the wiki vault and CLI commands for vault maintenance.",
		SilenceUsage: true,
	}

	// Persistent flag: --vault
	defaultVault := os.Getenv("WIKI_VAULT_DIR")
	if defaultVault == "" {
		defaultVault = "/data/vault"
	}
	cmd.PersistentFlags().String("vault", defaultVault, "path to wiki vault directory (env: WIKI_VAULT_DIR)")

	cmd.AddCommand(newServeCmd())
	cmd.AddCommand(newLintCmd())
	cmd.AddCommand(newDirectoryCmd())
	cmd.AddCommand(newLogCmd())
	cmd.AddCommand(newActivityCmd())

	return cmd
}

// Execute runs the root command.
func Execute() error {
	return NewRootCmd().Execute()
}
