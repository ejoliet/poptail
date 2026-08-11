package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func nextLine(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case line, ok := <-ch:
		if !ok {
			t.Fatal("line channel closed unexpectedly")
		}
		return line
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for line")
		return ""
	}
}

func TestScanLines(t *testing.T) {
	long := strings.Repeat("y", 100_000)
	ch := scanLines(strings.NewReader("one\ntwo\n" + long + "\n"))
	for _, want := range []string{"one", "two", long} {
		if got := nextLine(t, ch); got != want {
			t.Errorf("got %.20q want %.20q", got, want)
		}
	}
	if _, ok := <-ch; ok {
		t.Error("channel must close at EOF")
	}
}

func TestOpenSourceMissingFile(t *testing.T) {
	if _, err := openSource(filepath.Join(t.TempDir(), "nope.log")); err == nil {
		t.Error("missing file must error (exit-1 contract), not wait for creation")
	}
}

func TestTailFileFollowsAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	// Pre-existing content must be skipped (start at EOF).
	if err := os.WriteFile(path, []byte("old line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ch, err := openSource(path)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	time.Sleep(200 * time.Millisecond) // let tail attach before appending
	if _, err := f.WriteString("new one\nnew two\n"); err != nil {
		t.Fatal(err)
	}
	if got := nextLine(t, ch); got != "new one" {
		t.Errorf("got %q want %q (old content must not replay)", got, "new one")
	}
	if got := nextLine(t, ch); got != "new two" {
		t.Errorf("got %q want %q", got, "new two")
	}
}

func TestTailFileEmitsRotatedMarkerOnTruncate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rot.log")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	ch, err := openSource(path)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	appendLine := func(s string) {
		t.Helper()
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString(s + "\n"); err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
	}
	appendLine("first")
	appendLine("second")
	if got := nextLine(t, ch); got != "first" {
		t.Fatalf("got %q want first", got)
	}
	if got := nextLine(t, ch); got != "second" {
		t.Fatalf("got %q want second", got)
	}
	// Truncate = logrotate copytruncate style.
	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond) // let tail notice truncation
	appendLine("fresh")
	if got := nextLine(t, ch); got != rotatedMarker {
		t.Errorf("got %q want rotation marker before post-truncate lines", got)
	}
	if got := nextLine(t, ch); got != "fresh" {
		t.Errorf("got %q want %q", got, "fresh")
	}
}
