package cli_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestServeMCPStdio_Integration spawns the wiki-server binary as a subprocess
// in stdio mode, sends an initialize and tools/list JSON-RPC request, and
// verifies that:
//  1. stdout contains valid JSON-RPC response framing only (no log garbage)
//  2. stderr may have logs but is non-fatal
//  3. tools/list returns the expected tool set including "whoami"
//
// This test is gated by `-short` and the WIKI_SKIP_SUBPROCESS_TESTS env var
// for sandboxed CI environments that cannot spawn child processes.
func TestServeMCPStdio_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess integration test in -short mode")
	}
	if os.Getenv("WIKI_SKIP_SUBPROCESS_TESTS") != "" {
		t.Skip("WIKI_SKIP_SUBPROCESS_TESTS set; skipping subprocess integration test")
	}

	// Build the binary into a temp dir so we don't pollute the worktree.
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "wiki-server")
	buildCmd := exec.Command("go", "build", "-o", binPath, "../../cmd/wiki-server")
	buildOut, err := buildCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build failed: %v\n%s", err, buildOut)
	}

	// Minimal vault fixture.
	vaultDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(vaultDir, "index.md"),
		[]byte("---\ntitle: Home\ntags:\n  - root\ndate: 2026-01-01\n---\n\nHello.\n"),
		0o600); err != nil {
		t.Fatalf("seed vault: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binPath, "--vault", vaultDir, "serve", "mcp-stdio", "--instance-name", "test-wiki")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("StderrPipe: %v", err)
	}

	if err := cmd.Start(); err != nil {
		t.Fatalf("start subprocess: %v", err)
	}

	// Drain stderr in the background — failure to drain blocks the child.
	stderrCh := make(chan []byte, 1)
	go func() {
		buf, _ := io.ReadAll(stderr)
		stderrCh <- buf
	}()

	// Send JSON-RPC initialize.
	initReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "test-client", "version": "0.0.0"},
		},
	}
	if err := writeJSONRPC(stdin, initReq); err != nil {
		t.Fatalf("write initialize: %v", err)
	}

	// Send tools/list.
	listReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
		"params":  map[string]any{},
	}
	if err := writeJSONRPC(stdin, listReq); err != nil {
		t.Fatalf("write tools/list: %v", err)
	}

	// Read responses. Each JSON-RPC response is a single line of JSON.
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var responses []map[string]any
	deadline := time.Now().Add(15 * time.Second)
	for len(responses) < 2 && time.Now().Before(deadline) {
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				t.Fatalf("stdout scanner: %v", err)
			}
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var msg map[string]any
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Fatalf("stdout contained non-JSON line (protocol corruption): %q\nerror: %v", line, err)
		}
		// Per JSON-RPC, responses must include jsonrpc:2.0.
		if msg["jsonrpc"] != "2.0" {
			t.Fatalf("response missing jsonrpc:2.0 field: %v", msg)
		}
		responses = append(responses, msg)
	}

	// Close stdin so the child can drain and exit.
	_ = stdin.Close()

	// Wait for clean exit (the child receives EOF on stdin and Listen returns).
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	select {
	case <-waitCh:
		// Child exited (any reason — stdio Listen returns nil on EOF).
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		<-waitCh
		t.Fatal("subprocess did not exit after stdin close")
	}

	stderrBytes := <-stderrCh

	if len(responses) < 2 {
		t.Fatalf("expected 2 responses, got %d. stderr:\n%s", len(responses), stderrBytes)
	}

	// Verify the second response (tools/list) includes the whoami tool.
	listResp := responses[1]
	result, ok := listResp["result"].(map[string]any)
	if !ok {
		t.Fatalf("tools/list response missing result: %v\nstderr:\n%s", listResp, stderrBytes)
	}
	tools, ok := result["tools"].([]any)
	if !ok {
		t.Fatalf("tools/list result missing tools array: %v", result)
	}

	var foundWhoami bool
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if tool["name"] == "whoami" {
			foundWhoami = true
			break
		}
	}
	if !foundWhoami {
		t.Errorf("expected whoami tool in tools/list response, got %d tools", len(tools))
	}
}

// writeJSONRPC marshals msg and writes it followed by a newline to w.
// stdio MCP framing is line-delimited JSON.
func writeJSONRPC(w io.Writer, msg map[string]any) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if _, err := w.Write(append(data, '\n')); err != nil {
		return err
	}
	return nil
}
