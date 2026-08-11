package main

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"strings"

	"github.com/nxadm/tail"
)

// rotatedMarker is injected into the stream when the tailed file is
// rotated or truncated (README error-handling contract).
const rotatedMarker = "--- rotated ---"

// maxBackfillBytes caps the backward read for -n so a file of very long lines
// cannot turn startup into a full scan (acceptance criterion: 500 MB log,
// startup < 1s).
const maxBackfillBytes = 8 << 20

// openSource returns a channel of raw log lines: nxadm/tail on a file
// (rotation-aware, seeded with the last n lines then following), or stdin
// when src is "-". The channel closes when the source ends.
//
// The returned stop func releases the file watcher. AIDEV-NOTE: this is not
// optional bookkeeping — nxadm/tail shares one process-wide watcher, so an
// un-stopped tailer keeps watching a path that may be gone and starves later
// tailers in the same process (caught by repeated test runs).
func openSource(src string, n int) (<-chan string, func(), error) {
	if src == "-" {
		// A blocked read on stdin cannot be unblocked portably; process exit
		// is the only stop for pipe mode.
		return scanLines(os.Stdin), func() {}, nil // -n is moot on a pipe: nothing buffered yet
	}
	return tailFile(src, n)
}

// scanLines streams lines from r; lines up to 1 MiB.
func scanLines(r io.Reader) <-chan string {
	out := make(chan string)
	go func() {
		defer close(out)
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			out <- sc.Text()
		}
	}()
	return out
}

func tailFile(path string, n int) (<-chan string, func(), error) {
	// AIDEV-NOTE: tail.TailFile with ReOpen waits for missing files (tail -F
	// semantics); the README contract is exit 1 on unreadable file, so the
	// backfill read below doubles as the readability probe.
	initial, off, err := lastLines(path, n)
	if err != nil {
		return nil, nil, err
	}
	// AIDEV-NOTE: follow from exactly where the backfill stopped, not from EOF —
	// otherwise lines written between the two would be lost (or duplicated).
	// AIDEV-NOTE: Poll (stat every ~250ms) instead of fsnotify. Measured
	// 2026-08-11: with the event watcher, a write landing between "read to EOF"
	// and "watcher registered" is missed and the line stays invisible until the
	// NEXT write — on a quiet log that is indefinite. Polling has no such
	// window and also survives filesystems where fsnotify silently no-ops
	// (NFS, some container mounts). Cost: one stat per 250ms; latency floor
	// well inside the <2s spike gate.
	t, err := tail.TailFile(path, tail.Config{
		Follow:   true,
		ReOpen:   true,
		Poll:     true,
		Location: &tail.SeekInfo{Offset: off, Whence: io.SeekStart},
		Logger:   tail.DiscardingLogger,
	})
	if err != nil {
		return nil, nil, err
	}
	out := make(chan string)
	go func() {
		defer close(out)
		for _, line := range initial {
			out <- line
		}
		lastOff := off
		for line := range t.Lines {
			if line.Err != nil {
				continue // transient read error; tail retries internally
			}
			// AIDEV-NOTE: offset going backwards = truncation or reopen after
			// rotation. ponytail: misses a rotation where the new file is
			// already longer than the old one; detect via logger sniffing if
			// that ever matters.
			if line.SeekInfo.Offset < lastOff {
				out <- rotatedMarker
			}
			lastOff = line.SeekInfo.Offset
			out <- line.Text
		}
	}()
	stop := func() {
		_ = t.Stop()
		t.Cleanup() // drop the shared watcher's entry for this path
	}
	return out, stop, nil
}

// lastLines reads up to the final n lines of path and returns them with the
// offset it stopped at (the file size at open time), reading backwards in
// chunks so a huge log costs a seek, not a scan.
func lastLines(path string, n int) ([]string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = f.Close() }()

	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, 0, err
	}
	if n <= 0 || size == 0 {
		return nil, size, nil
	}

	const chunk = 64 << 10
	var buf []byte
	start := size
	truncated := false // buf may begin mid-line
	for start > 0 && bytes.Count(buf, []byte("\n")) <= n {
		step := int64(chunk)
		if start < step {
			step = start
		}
		if size-(start-step) > maxBackfillBytes {
			truncated = true
			break
		}
		start -= step
		b := make([]byte, step)
		if _, err := f.ReadAt(b, start); err != nil && err != io.EOF {
			return nil, 0, err
		}
		buf = append(b, buf...)
	}
	if start > 0 {
		truncated = true
	}

	text := strings.TrimSuffix(string(buf), "\n")
	if text == "" {
		return nil, size, nil
	}
	lines := strings.Split(text, "\n")
	if truncated && len(lines) > 0 {
		lines = lines[1:] // first element is a partial line: drop it
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, size, nil
}
