//go:build integration

package cli_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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
// This file is gated by the `integration` build tag. Run with:
//
//	go test -tags=integration ./internal/cli/
//
// Default `go test ./...` skips this test (file is not compiled), so unit-test
// runs stay fast and don't shell out to `go build`.
func TestServeMCPStdio_Integration(t *testing.T) {
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

	cmd := exec.CommandContext(ctx, binPath, "--vault", vaultDir, "serve", "mcp", "stdio", "--instance-name", "test-wiki")
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

	// Pump stdout lines into a channel so the read loop is bounded by a
	// timer rather than blocking on scanner.Scan() until EOF.
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineCh := make(chan string)
	scanErrCh := make(chan error, 1)
	go func() {
		defer close(lineCh)
		for scanner.Scan() {
			lineCh <- scanner.Text()
		}
		scanErrCh <- scanner.Err()
	}()

	// Collect responses by JSON-RPC id (responses are not guaranteed to
	// arrive in request order).
	responses := make(map[float64]map[string]any)
	readDeadline := time.NewTimer(15 * time.Second)
	defer readDeadline.Stop()

readLoop:
	for len(responses) < 2 {
		select {
		case line, ok := <-lineCh:
			if !ok {
				// stdout closed before we got both responses
				break readLoop
			}
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var msg map[string]any
			if err := json.Unmarshal([]byte(line), &msg); err != nil {
				t.Fatalf("stdout contained non-JSON line (protocol corruption): %q\nerror: %v", line, err)
			}
			if msg["jsonrpc"] != "2.0" {
				t.Fatalf("response missing jsonrpc:2.0 field: %v", msg)
			}
			id, ok := msg["id"].(float64)
			if !ok {
				// Notifications have no id; ignore for this test.
				continue
			}
			responses[id] = msg
		case <-readDeadline.C:
			t.Fatalf("timeout waiting for responses; got %d, stderr will follow shutdown", len(responses))
		case <-ctx.Done():
			t.Fatalf("context done while reading: %v", ctx.Err())
		}
	}

	// Close stdin so the child can drain and exit.
	_ = stdin.Close()

	// Wait for clean exit and capture the exit error.
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	var waitErr error
	select {
	case waitErr = <-waitCh:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		<-waitCh
		t.Fatal("subprocess did not exit after stdin close")
	}

	stderrBytes := <-stderrCh

	if waitErr != nil {
		t.Fatalf("subprocess exited non-zero: %v\nstderr:\n%s", waitErr, stderrBytes)
	}
	if scanErr := <-scanErrCh; scanErr != nil {
		t.Fatalf("stdout scanner: %v", scanErr)
	}

	if len(responses) < 2 {
		t.Fatalf("expected 2 responses, got %d. stderr:\n%s", len(responses), stderrBytes)
	}

	// Verify the tools/list response (id=2) includes the whoami tool.
	listResp, ok := responses[2]
	if !ok {
		t.Fatalf("missing response for tools/list (id=2); got ids=%v\nstderr:\n%s", responseIDs(responses), stderrBytes)
	}
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

// responseIDs returns the response ids in sorted order for stable
// diagnostic output (map iteration order would otherwise be random).
func responseIDs(responses map[float64]map[string]any) []float64 {
	ids := make([]float64, 0, len(responses))
	for id := range responses {
		ids = append(ids, id)
	}
	sort.Float64s(ids)
	return ids
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
