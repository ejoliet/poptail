package main

import (
	"bufio"
	"io"
	"os"

	"github.com/nxadm/tail"
)

// rotatedMarker is injected into the stream when the tailed file is
// rotated or truncated (README error-handling contract).
const rotatedMarker = "--- rotated ---"

// openSource returns a channel of raw log lines: nxadm/tail on a file
// (follow + rotation-aware, starting at EOF so a 500 MB log starts in <1s),
// or stdin when src is "-". The channel closes when the source ends.
func openSource(src string) (<-chan string, error) {
	if src == "-" {
		return scanLines(os.Stdin), nil
	}
	return tailFile(src)
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

func tailFile(path string) (<-chan string, error) {
	// AIDEV-NOTE: tail.TailFile with ReOpen waits for missing files (tail -F
	// semantics); the README contract is exit 1 on unreadable file, so probe
	// readability up front.
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	_ = f.Close()

	t, err := tail.TailFile(path, tail.Config{
		Follow:   true,
		ReOpen:   true,
		Location: &tail.SeekInfo{Whence: io.SeekEnd},
		Logger:   tail.DiscardingLogger,
	})
	if err != nil {
		return nil, err
	}
	out := make(chan string)
	go func() {
		defer close(out)
		lastOff := int64(-1)
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
	return out, nil
}
