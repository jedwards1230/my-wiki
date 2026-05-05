package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"

	"github.com/spf13/cobra"
)

// labelBase is the reverse-DNS prefix for all wiki-server LaunchAgent labels.
// Per-install labels append a suffix derived from --instance-name so that
// home-wiki and work-wiki installs don't collide on the same machine.
const labelBase = "cloud.lilbro.home-wiki"

// plistTemplate renders a daily-lint LaunchAgent. Logs go under
// ~/Library/Logs/home-wiki/<instance>/ so multiple installs stay tidy.
const plistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>{{.Label}}</string>
	<key>ProgramArguments</key>
	<array>
		<string>{{.BinaryPath}}</string>
		<string>--vault</string>
		<string>{{.VaultDir}}</string>
		<string>lint</string>
	</array>
	<key>StartCalendarInterval</key>
	<dict>
		<key>Hour</key>
		<integer>{{.Hour}}</integer>
		<key>Minute</key>
		<integer>{{.Minute}}</integer>
	</dict>
	<key>StandardOutPath</key>
	<string>{{.StdoutLog}}</string>
	<key>StandardErrorPath</key>
	<string>{{.StderrLog}}</string>
	<key>RunAtLoad</key>
	<false/>
</dict>
</plist>
`

type plistConfig struct {
	Label      string
	BinaryPath string
	VaultDir   string
	Hour       int
	Minute     int
	StdoutLog  string
	StderrLog  string
}

func newLaunchdCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "launchd",
		Short: "Manage macOS LaunchAgent for scheduled vault lint",
		Long: `Install, remove, or inspect the macOS LaunchAgent that runs ` + "`wiki-server lint`" + ` on a daily schedule.

The plist label is derived from --instance-name so multiple installs (e.g. home-wiki and work-wiki) coexist. macOS only — other platforms return an error.`,
	}
	cmd.AddCommand(newLaunchdInstallCmd())
	cmd.AddCommand(newLaunchdUninstallCmd())
	cmd.AddCommand(newLaunchdStatusCmd())
	return cmd
}

func newLaunchdInstallCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install the daily-lint LaunchAgent",
		RunE:  runLaunchdInstall,
	}
	cmd.Flags().Int("hour", 9, "hour (0-23) at which to run lint")
	cmd.Flags().Int("minute", 0, "minute (0-59) at which to run lint")
	return cmd
}

func newLaunchdUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the daily-lint LaunchAgent",
		RunE:  runLaunchdUninstall,
	}
}

func newLaunchdStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the LaunchAgent's installation and run state",
		RunE:  runLaunchdStatus,
	}
}

func runLaunchdInstall(cmd *cobra.Command, _ []string) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("launchd is macOS-only; current OS: %s", runtime.GOOS)
	}
	cfg, err := buildPlistConfig(cmd)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(cfg.StdoutLog), 0o755); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}

	plistPath, err := plistPathFor(cfg.Label)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return fmt.Errorf("create LaunchAgents dir: %w", err)
	}

	rendered, err := renderPlist(cfg)
	if err != nil {
		return err
	}
	if err := os.WriteFile(plistPath, rendered, 0o644); err != nil {
		return fmt.Errorf("write plist: %w", err)
	}

	// Best-effort unload first in case a previous version is loaded.
	_ = exec.Command("launchctl", "unload", plistPath).Run()
	if out, err := exec.Command("launchctl", "load", plistPath).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl load: %w\n%s", err, out)
	}

	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "installed: %s\n", plistPath)
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "label:     %s\n", cfg.Label)
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "schedule:  %02d:%02d daily\n", cfg.Hour, cfg.Minute)
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "logs:      %s\n", cfg.StdoutLog)
	return nil
}

func runLaunchdUninstall(cmd *cobra.Command, _ []string) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("launchd is macOS-only; current OS: %s", runtime.GOOS)
	}
	label := labelFromCmd(cmd)
	plistPath, err := plistPathFor(label)
	if err != nil {
		return err
	}

	if _, err := os.Stat(plistPath); os.IsNotExist(err) {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "not installed: %s\n", plistPath)
		return nil
	}

	if out, err := exec.Command("launchctl", "unload", plistPath).CombinedOutput(); err != nil {
		// Don't fail uninstall on unload error — file removal still useful.
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: launchctl unload: %v\n%s", err, out)
	}
	if err := os.Remove(plistPath); err != nil {
		return fmt.Errorf("remove plist: %w", err)
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "removed: %s\n", plistPath)
	return nil
}

func runLaunchdStatus(cmd *cobra.Command, _ []string) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("launchd is macOS-only; current OS: %s", runtime.GOOS)
	}
	label := labelFromCmd(cmd)
	plistPath, err := plistPathFor(label)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	_, _ = fmt.Fprintf(out, "label: %s\n", label)
	_, _ = fmt.Fprintf(out, "plist: %s\n", plistPath)
	if _, err := os.Stat(plistPath); os.IsNotExist(err) {
		_, _ = fmt.Fprintln(out, "state: not installed")
		return nil
	}
	_, _ = fmt.Fprintln(out, "state: installed")

	listOut, err := exec.Command("launchctl", "list", label).CombinedOutput()
	if err != nil {
		_, _ = fmt.Fprintf(out, "launchctl list: not currently loaded (%v)\n", err)
		return nil
	}
	_, _ = fmt.Fprintln(out, "launchctl list:")
	_, _ = fmt.Fprintln(out, strings.TrimRight(string(listOut), "\n"))
	return nil
}

func buildPlistConfig(cmd *cobra.Command) (plistConfig, error) {
	vaultDir, _ := cmd.Flags().GetString("vault")
	if vaultDir == "" {
		return plistConfig{}, fmt.Errorf("--vault is required (or set WIKI_VAULT_DIR)")
	}
	hour, _ := cmd.Flags().GetInt("hour")
	minute, _ := cmd.Flags().GetInt("minute")
	if hour < 0 || hour > 23 {
		return plistConfig{}, fmt.Errorf("--hour must be 0-23 (got %d)", hour)
	}
	if minute < 0 || minute > 59 {
		return plistConfig{}, fmt.Errorf("--minute must be 0-59 (got %d)", minute)
	}

	binPath, err := os.Executable()
	if err != nil {
		return plistConfig{}, fmt.Errorf("resolve binary path: %w", err)
	}
	binPath, err = filepath.EvalSymlinks(binPath)
	if err != nil {
		return plistConfig{}, fmt.Errorf("eval binary symlinks: %w", err)
	}

	label := labelFromCmd(cmd)
	logDir, err := logDirFor(label)
	if err != nil {
		return plistConfig{}, err
	}

	return plistConfig{
		Label:      label,
		BinaryPath: binPath,
		VaultDir:   vaultDir,
		Hour:       hour,
		Minute:     minute,
		StdoutLog:  filepath.Join(logDir, "lint.log"),
		StderrLog:  filepath.Join(logDir, "lint.err.log"),
	}, nil
}

// labelFromCmd builds the LaunchAgent label from --instance-name. Empty
// instance-name yields the unsuffixed base label; non-empty appends ".<name>"
// so multiple installs coexist.
func labelFromCmd(cmd *cobra.Command) string {
	instance, _ := cmd.Flags().GetString("instance-name")
	instance = strings.TrimSpace(instance)
	if instance == "" {
		return labelBase + ".lint"
	}
	return labelBase + ".lint." + sanitizeLabel(instance)
}

// sanitizeLabel replaces any non-[A-Za-z0-9._-] runes with '-' so an arbitrary
// instance-name produces a valid plist label and filename.
func sanitizeLabel(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}

func plistPathFor(label string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist"), nil
}

func logDirFor(label string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, "Library", "Logs", "home-wiki", label), nil
}

func renderPlist(cfg plistConfig) ([]byte, error) {
	tmpl, err := template.New("plist").Parse(plistTemplate)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, cfg); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
