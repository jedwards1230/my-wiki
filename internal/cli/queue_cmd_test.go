package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupQueueVault(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	os.MkdirAll(filepath.Join(dir, "raw"), 0o755)
	os.MkdirAll(filepath.Join(dir, "meta"), 0o755)

	files := map[string]string{
		"raw/unprocessed.md": "---\ntitle: Unprocessed\nsource: https://example.com\ndate-added: 2026-01-15\n---\n\nContent.\n",
		"raw/processed.md":   "---\ntitle: Processed\nsource: https://example.com\ndate-added: 2026-01-10\ningested: true\n---\n\nDone.\n",
		"raw/also-new.md":    "---\ntitle: Also New\nsource: https://example.com/2\ndate-added: 2026-02-01\n---\n\nMore content.\n",
	}
	for name, content := range files {
		os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
	}

	return dir
}

func TestQueueList(t *testing.T) {
	dir := setupQueueVault(t)

	// Capture stdout
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"--vault", dir, "queue"})
	err := cmd.Execute()

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatal(err)
	}

	var buf [4096]byte
	n, _ := r.Read(buf[:])
	output := string(buf[:n])

	// Should show unprocessed files
	if !strings.Contains(output, "raw/unprocessed.md") {
		t.Errorf("expected unprocessed.md in output, got:\n%s", output)
	}
	if !strings.Contains(output, "raw/also-new.md") {
		t.Errorf("expected also-new.md in output, got:\n%s", output)
	}
	// Should NOT show processed file
	if strings.Contains(output, "raw/processed.md") {
		t.Errorf("should not show processed.md, got:\n%s", output)
	}
}

func TestQueueCount(t *testing.T) {
	dir := setupQueueVault(t)

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"--vault", dir, "queue", "--count"})
	err := cmd.Execute()

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatal(err)
	}

	var buf [4096]byte
	n, _ := r.Read(buf[:])
	output := string(buf[:n])

	if !strings.Contains(output, "2 unprocessed") {
		t.Errorf("expected '2 unprocessed', got:\n%s", output)
	}
}

func TestQueueGenerate(t *testing.T) {
	dir := setupQueueVault(t)

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"--vault", dir, "queue", "--generate"})
	err := cmd.Execute()
	if err != nil {
		t.Fatal(err)
	}

	queueFile := filepath.Join(dir, "meta", "ingest-queue.md")
	data, err := os.ReadFile(queueFile)
	if err != nil {
		t.Fatalf("queue file not created: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "Ingest Queue") {
		t.Error("missing title in generated queue file")
	}
	if !strings.Contains(content, "raw/unprocessed.md") {
		t.Error("missing unprocessed.md in generated queue")
	}
	if strings.Contains(content, "raw/processed.md") {
		t.Error("processed.md should not be in queue")
	}
}

func TestQueueEmpty(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "raw"), 0o755)

	// Only processed files
	os.WriteFile(filepath.Join(dir, "raw/done.md"), []byte("---\ntitle: Done\ningested: true\n---\n"), 0o644)

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"--vault", dir, "queue"})
	err := cmd.Execute()

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatal(err)
	}

	var buf [4096]byte
	n, _ := r.Read(buf[:])
	output := string(buf[:n])

	if !strings.Contains(output, "All raw sources have been ingested") {
		t.Errorf("expected empty queue message, got:\n%s", output)
	}
}
