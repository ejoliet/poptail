// spike1.go — poptail Spike 1 (THROWAWAY — not production code)
// Scope: tail file/stdin → SSE with Last-Event-ID replay → cloudflared quick tunnel.
// Out of scope: crypto, redaction, flags, download logic. See poptail-README.md.
//
// Usage:
//   go run spike1.go /tmp/spike.log
//   some-cmd | go run spike1.go -
//
// Requires: Go 1.22+, cloudflared on PATH.
package main

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nxadm/tail"
)

const bufferSize = 500 // replay buffer for late joiners / reconnects

// AIDEV-NOTE: seq is 1-based; Last-Event-ID: N means "I have N, send N+1 onward".
type hub struct {
	mu    sync.Mutex
	lines []string // ring of last bufferSize lines
	first int      // seq of lines[0]
	next  int      // seq to assign to the next line (== first+len(lines))
	subs  map[chan int]struct{}
}

func newHub() *hub {
	return &hub{first: 1, next: 1, subs: make(map[chan int]struct{})}
}

func (h *hub) publish(line string) {
	h.mu.Lock()
	h.lines = append(h.lines, line)
	if len(h.lines) > bufferSize {
		h.lines = h.lines[1:]
		h.first++
	}
	h.next++
	for ch := range h.subs {
		select { // non-blocking wake-up; slow subs catch up from buffer
		case ch <- h.next:
		default:
		}
	}
	h.mu.Unlock()
}

// since returns lines with seq > afterSeq that are still in the buffer.
func (h *hub) since(afterSeq int) (start int, out []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	from := afterSeq + 1
	if from < h.first {
		from = h.first // gap: viewer was too far behind; oldest available wins
	}
	idx := from - h.first
	if idx < len(h.lines) {
		out = append(out, h.lines[idx:]...)
	}
	return from, out
}

func (h *hub) subscribe() chan int {
	ch := make(chan int, 1)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *hub) unsubscribe(ch chan int) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
}

func sseHandler(h *hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")

		// AIDEV-NOTE: Cloudflare quick tunnels buffer uncompressed streaming
		// responses until EOF (edge holds the body to gzip it), which kills SSE.
		// Sending gzip from the origin makes the edge pass bytes through live.
		// Verified empirically 2026-08-08; plain SSE = 0 bytes until close.
		var out io.Writer = w
		doFlush := fl.Flush
		if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			w.Header().Set("Content-Encoding", "gzip")
			gz := gzip.NewWriter(w)
			defer gz.Close()
			out = gz
			doFlush = func() {
				_ = gz.Flush()
				fl.Flush()
			}
		}

		last := 0
		if v := r.Header.Get("Last-Event-ID"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				last = n
			}
		}
		sub := h.subscribe()
		defer h.unsubscribe(sub)
		heartbeat := time.NewTicker(20 * time.Second)
		defer heartbeat.Stop()

		flush := func() bool {
			start, batch := h.since(last)
			for i, line := range batch {
				fmt.Fprintf(out, "id: %d\ndata: %s\n\n", start+i, line)
				last = start + i
			}
			if len(batch) > 0 {
				doFlush()
			}
			return true
		}
		fmt.Fprint(out, ": connected\n\n") // push headers+first bytes through edge now
		doFlush()
		flush() // replay on connect

		for {
			select {
			case <-r.Context().Done():
				return
			case <-sub:
				flush()
			case <-heartbeat.C:
				fmt.Fprint(out, ": hb\n\n")
				doFlush()
			}
		}
	}
}

const viewerHTML = `<!doctype html><meta charset="utf-8">
<title>poptail spike</title>
<style>body{background:#111;color:#0f0;font:12px/1.4 monospace;margin:0}
pre{margin:0;padding:8px;white-space:pre-wrap;word-break:break-all}
#s{position:fixed;top:4px;right:8px;color:#888}</style>
<div id="s">connecting…</div><pre id="log"></pre>
<script>
const log=document.getElementById('log'),s=document.getElementById('s');
const es=new EventSource('/stream');
es.onopen=()=>s.textContent='live';
es.onerror=()=>s.textContent='reconnecting…'; // EventSource auto-retries with Last-Event-ID
es.onmessage=e=>{log.textContent+=e.data+'\n';window.scrollTo(0,document.body.scrollHeight);};
</script>`

func startTunnel(port int) (*exec.Cmd, string, error) {
	cmd := exec.Command("cloudflared", "tunnel", "--protocol","http2","--url",fmt.Sprintf("http://127.0.0.1:%d", port))
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, "", err
	}
	if err := cmd.Start(); err != nil {
		return nil, "", fmt.Errorf("is cloudflared installed and on PATH? %w", err)
	}
	urlRe := regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)
	urlCh := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			if m := urlRe.FindString(sc.Text()); m != "" {
				select {
				case urlCh <- m:
				default:
				}
			}
		}
	}()
	select {
	case u := <-urlCh:
		return cmd, u, nil
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		return nil, "", fmt.Errorf("timed out waiting for trycloudflare URL")
	}
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: spike1 <file>  |  cmd | spike1 -")
		os.Exit(1)
	}
	src := os.Args[1]
	h := newHub()

	// Source: file (start at end, follow rotation) or stdin.
	if src == "-" {
		go func() {
			sc := bufio.NewScanner(os.Stdin)
			sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
			for sc.Scan() {
				h.publish(sc.Text())
			}
		}()
	} else {
		t, err := tail.TailFile(src, tail.Config{
			Follow: true, ReOpen: true,
			Location: &tail.SeekInfo{Offset: 0, Whence: 2}, // seek to end
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "spike1:", err)
			os.Exit(1)
		}
		go func() {
			for line := range t.Lines {
				h.publish(line.Text)
			}
		}()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/stream", sseHandler(h))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, viewerHTML)
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0") // random high port, loopback only
	if err != nil {
		fmt.Fprintln(os.Stderr, "spike1:", err)
		os.Exit(1)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	go func() { _ = http.Serve(ln, mux) }()
	fmt.Printf("spike1: local http://127.0.0.1:%d\n", port)

	tun, url, err := startTunnel(port)
	if err != nil {
		fmt.Fprintln(os.Stderr, "spike1: tunnel failed:", err)
		fmt.Fprintln(os.Stderr, "spike1: local URL still works on this machine")
	} else {
		fmt.Printf("spike1: share %s\n", url)
	}
	fmt.Println("spike1: Ctrl-C to kill the link")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	if tun != nil && tun.Process != nil {
		_ = tun.Process.Kill() // AIDEV-NOTE: no orphan cloudflared (acceptance criterion)
	}
}
