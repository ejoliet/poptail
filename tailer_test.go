package main

import (
	"os"
	"path/filepath"
	"strconv"
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
	if _, _, err := openSource(filepath.Join(t.TempDir(), "nope.log"), 0); err == nil {
		t.Error("missing file must error (exit-1 contract), not wait for creation")
	}
}

func TestLastLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	var sb strings.Builder
	for i := 1; i <= 500; i++ {
		// 200-byte padding pushes the file past one 64 KiB backward chunk.
		sb.WriteString("line " + strings.Repeat("x", 200) + " " + strconv.Itoa(i) + "\n")
	}
	body := sb.String()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	lines, off, err := lastLines(path, 3)
	if err != nil {
		t.Fatal(err)
	}
	if off != int64(len(body)) {
		t.Errorf("offset %d, want file size %d", off, len(body))
	}
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}
	if !strings.HasSuffix(lines[2], " 500") || !strings.HasSuffix(lines[0], " 498") {
		t.Errorf("wrong tail window: first=%.20q last=%.20q", lines[0], lines[2])
	}

	// n larger than the file: everything, no partial first line.
	all, _, err := lastLines(path, 10_000)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 500 {
		t.Errorf("got %d lines, want all 500", len(all))
	}
	if !strings.HasSuffix(all[0], " 1") {
		t.Errorf("first line looks truncated: %.20q", all[0])
	}

	// n <= 0 reads nothing but still reports EOF offset.
	none, off0, err := lastLines(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 || off0 != int64(len(body)) {
		t.Errorf("n=0 gave %d lines, offset %d", len(none), off0)
	}
}

func TestLastLinesEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.log")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	lines, off, err := lastLines(path, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 0 || off != 0 {
		t.Errorf("empty file gave %d lines, offset %d", len(lines), off)
	}
}

func TestTailFileBackfillThenFollow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	if err := os.WriteFile(path, []byte("old one\nold two\nold three\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ch, stop, err := openSource(path, 2) // -n 2: last two existing lines, then live
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	if got := nextLine(t, ch); got != "old two" {
		t.Errorf("backfill[0] = %q, want %q", got, "old two")
	}
	if got := nextLine(t, ch); got != "old three" {
		t.Errorf("backfill[1] = %q, want %q", got, "old three")
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString("new one\n"); err != nil {
		t.Fatal(err)
	}
	if got := nextLine(t, ch); got != "new one" {
		t.Errorf("live line = %q", got)
	}
}

func TestTailFileSkipsHistoryWhenNIsZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	if err := os.WriteFile(path, []byte("old line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ch, stop, err := openSource(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	time.Sleep(200 * time.Millisecond) // let tail attach
	if _, err := f.WriteString("new one\n"); err != nil {
		t.Fatal(err)
	}
	if got := nextLine(t, ch); got != "new one" {
		t.Errorf("got %q want %q (history must not replay)", got, "new one")
	}
}

func TestTailFileEmitsRotatedMarkerOnTruncate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rot.log")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	ch, stop, err := openSource(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
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
