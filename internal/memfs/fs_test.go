package memfs

import (
	"errors"
	"io"
	"io/fs"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSnapshot_AddFile_MaterializesParents(t *testing.T) {
	s := NewSnapshot()
	mt := time.Unix(1000, 0)
	if err := s.AddFile("docs/faq/index.html", []byte("<html>"), mt); err != nil {
		t.Fatalf("AddFile: %v", err)
	}
	for _, want := range []string{"docs", "docs/faq", "docs/faq/index.html"} {
		if _, ok := s.entries[want]; !ok {
			t.Errorf("expected entry %q in snapshot", want)
		}
	}
	if s.Files() != 1 {
		t.Errorf("Files(): got %d want 1", s.Files())
	}
	if s.Bytes() != int64(len("<html>")) {
		t.Errorf("Bytes(): got %d", s.Bytes())
	}
}

// TestSnapshot_AddFile_OverwriteCorrectsBytes guards against double-
// counting. Re-adding the same key (e.g., during a partial rebuild) must
// subtract the previous file's byte count before adding the new one so
// Bytes() reflects the current snapshot, not cumulative writes.
func TestSnapshot_AddFile_OverwriteCorrectsBytes(t *testing.T) {
	s := NewSnapshot()
	mt := time.Unix(1000, 0)
	if err := s.AddFile("index.html", []byte("first-version"), mt); err != nil {
		t.Fatalf("AddFile 1: %v", err)
	}
	if err := s.AddFile("index.html", []byte("rewritten"), mt); err != nil {
		t.Fatalf("AddFile 2: %v", err)
	}
	if s.Files() != 1 {
		t.Errorf("Files: got %d want 1", s.Files())
	}
	if got, want := s.Bytes(), int64(len("rewritten")); got != want {
		t.Errorf("Bytes after overwrite: got %d want %d", got, want)
	}
}

func TestSnapshot_AddFile_RejectsDisallowedPaths(t *testing.T) {
	s := NewSnapshot()
	cases := []string{"", ".", "..", "a/..", "a/./b", "", "/leading", "a//b"}
	for _, c := range cases {
		if err := s.AddFile(c, nil, time.Time{}); err == nil {
			t.Errorf("AddFile(%q): want error, got nil", c)
		}
	}
}

func TestFS_OpenAndReadSeek(t *testing.T) {
	s := NewSnapshot()
	body := []byte("hello world")
	if err := s.AddFile("index.html", body, time.Unix(1000, 0)); err != nil {
		t.Fatal(err)
	}
	f := New()
	f.Store(s)

	file, err := f.Open("index.html")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = file.Close() }()

	got, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != string(body) {
		t.Errorf("content mismatch: got %q want %q", got, body)
	}

	seeker, ok := file.(io.Seeker)
	if !ok {
		t.Fatal("memFile must implement io.Seeker so http.ServeContent handles Range correctly")
	}
	if _, err := seeker.Seek(6, io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	rest, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("ReadAll after Seek: %v", err)
	}
	if string(rest) != "world" {
		t.Errorf("post-seek content: got %q want %q", rest, "world")
	}
}

func TestFS_OpenNotExist(t *testing.T) {
	f := New()
	_, err := f.Open("missing.html")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("want fs.ErrNotExist, got %v", err)
	}
}

func TestFS_ReadDirSortedAndScoped(t *testing.T) {
	s := NewSnapshot()
	mt := time.Unix(1000, 0)
	_ = s.AddFile("index.html", []byte("root"), mt)
	_ = s.AddFile("a.html", []byte("a"), mt)
	_ = s.AddFile("z.html", []byte("z"), mt)
	_ = s.AddFile("docs/nested.html", []byte("n"), mt)
	f := New()
	f.Store(s)

	entries, err := f.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	got := make([]string, len(entries))
	for i, e := range entries {
		got[i] = e.Name()
	}
	want := []string{"a.html", "docs", "index.html", "z.html"}
	if len(got) != len(want) {
		t.Fatalf("len mismatch: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("index %d: got %q want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

// TestFS_AtomicSwap_NoTornReads fires many concurrent Opens across a
// Store that replaces the snapshot mid-flight. Each goroutine must see
// either the old content or the new content — never a mix and never an
// error. Runs under -race.
func TestFS_AtomicSwap_NoTornReads(t *testing.T) {
	f := New()

	old := NewSnapshot()
	_ = old.AddFile("page.html", []byte("OLD"), time.Unix(1, 0))
	f.Store(old)

	next := NewSnapshot()
	_ = next.AddFile("page.html", []byte("NEW"), time.Unix(2, 0))

	const readers = 16
	const iters = 500
	var (
		wg    sync.WaitGroup
		start = make(chan struct{})
		bad   int64
	)
	wg.Add(readers + 1)
	for i := 0; i < readers; i++ {
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < iters; j++ {
				file, err := f.Open("page.html")
				if err != nil {
					atomic.AddInt64(&bad, 1)
					return
				}
				b, _ := io.ReadAll(file)
				_ = file.Close()
				if string(b) != "OLD" && string(b) != "NEW" {
					atomic.AddInt64(&bad, 1)
					return
				}
			}
		}()
	}
	go func() {
		defer wg.Done()
		<-start
		for j := 0; j < iters; j++ {
			if j%2 == 0 {
				f.Store(next)
			} else {
				f.Store(old)
			}
		}
	}()
	close(start)
	wg.Wait()

	if bad != 0 {
		t.Fatalf("torn reads or errors: %d", bad)
	}
}
