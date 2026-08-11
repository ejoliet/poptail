package main

import (
	"compress/gzip"
	_ "embed"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed viewer/index.html
var viewerHTML []byte

const heartbeatInterval = 20 * time.Second

// hub is the encrypted ring buffer plus fan-out to connected viewers.
//
// AIDEV-NOTE: seq is 1-based; Last-Event-ID: N means "I have N, send N+1 onward".
// The buffer stores ciphertext (base64 of nonce||ct), so replay needs no
// re-encryption and plaintext never sits in server memory beyond publish.
type hub struct {
	mu    sync.Mutex
	enc   *encryptor
	size  int
	lines []string // ring of the last size encrypted lines
	first int      // seq of lines[0]
	next  int      // seq to assign to the next line (== first+len(lines))
	subs  map[chan struct{}]struct{}
}

func newHub(enc *encryptor, size int) *hub {
	if size < 1 {
		size = 1
	}
	return &hub{
		enc:   enc,
		size:  size,
		first: 1,
		next:  1,
		subs:  make(map[chan struct{}]struct{}),
	}
}

// publish seals one line and appends it to the ring, waking subscribers.
// AIDEV-NOTE: on seal failure the line is dropped, never buffered in the
// clear — an unencryptable line must not reach the wire.
func (h *hub) publish(line string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	sealed, err := h.enc.seal(h.next, line)
	if err != nil {
		return err
	}
	h.lines = append(h.lines, sealed)
	if len(h.lines) > h.size {
		drop := len(h.lines) - h.size
		h.lines = h.lines[drop:]
		h.first += drop
	}
	h.next++
	for ch := range h.subs {
		select { // non-blocking wake-up; slow subs catch up from the buffer
		case ch <- struct{}{}:
		default:
		}
	}
	return nil
}

// since returns the lines with seq > afterSeq that are still in the buffer,
// and the seq of the first one returned.
func (h *hub) since(afterSeq int) (start int, out []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	from := afterSeq + 1
	if from < h.first {
		from = h.first // gap: viewer fell further behind than the ring; oldest wins
	}
	if idx := from - h.first; idx >= 0 && idx < len(h.lines) {
		out = append(out, h.lines[idx:]...)
	}
	return from, out
}

func (h *hub) subscribe() chan struct{} {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *hub) unsubscribe(ch chan struct{}) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
}

// newMux wires the two read-only endpoints: the embedded viewer and the SSE
// stream. No endpoint accepts input other than the Last-Event-ID header.
func newMux(h *hub) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/stream", sseHandler(h))
	mux.HandleFunc("/", viewerHandler)
	return mux
}

func viewerHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// AIDEV-NOTE: the viewer needs nothing but its own origin. This CSP means
	// injected log content (rendered as text, never HTML) still cannot load or
	// phone home to anything — including exfiltrating the fragment key.
	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; form-action 'none'")
	_, _ = w.Write(viewerHTML)
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
		// responses until EOF (the edge holds the body to gzip it), which kills
		// SSE. Sending gzip from the origin makes the edge pass bytes through
		// live. Verified empirically 2026-08-08; plain SSE = 0 bytes until close.
		var out io.Writer = w
		doFlush := fl.Flush
		if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			w.Header().Set("Content-Encoding", "gzip")
			gz := gzip.NewWriter(w)
			defer func() { _ = gz.Close() }()
			out = gz
			doFlush = func() {
				_ = gz.Flush()
				fl.Flush()
			}
		}

		last := 0
		if v := r.Header.Get("Last-Event-ID"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				last = n
			}
		}
		sub := h.subscribe()
		defer h.unsubscribe(sub)
		heartbeat := time.NewTicker(heartbeatInterval)
		defer heartbeat.Stop()

		send := func() {
			start, batch := h.since(last)
			for i, line := range batch {
				fmt.Fprintf(out, "id: %d\ndata: %s\n\n", start+i, line)
				last = start + i
			}
			if len(batch) > 0 {
				doFlush()
			}
		}
		fmt.Fprint(out, ": connected\n\n") // push headers + first bytes through the edge now
		doFlush()
		send() // replay on connect

		for {
			select {
			case <-r.Context().Done():
				return
			case <-sub:
				send()
			case <-heartbeat.C:
				fmt.Fprint(out, ": hb\n\n")
				doFlush()
			}
		}
	}
}
