package main

import (
	"bufio"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

type sseEvent struct {
	id   int
	data string
}

func newTestHub(t *testing.T, size int) *hub {
	t.Helper()
	e, err := newEncryptorWithKey(testKey())
	if err != nil {
		t.Fatal(err)
	}
	return newHub(e, size)
}

func mustPublish(t *testing.T, h *hub, lines ...string) {
	t.Helper()
	for _, l := range lines {
		if err := h.publish(l); err != nil {
			t.Fatal(err)
		}
	}
}

// readEvents reads exactly n SSE events (skipping comments) or fails.
func readEvents(t *testing.T, r io.Reader, n int) []sseEvent {
	t.Helper()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var out []sseEvent
	var cur sseEvent
	for len(out) < n && sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "id: "):
			id, err := strconv.Atoi(strings.TrimPrefix(line, "id: "))
			if err != nil {
				t.Fatalf("bad id line %q: %v", line, err)
			}
			cur.id = id
		case strings.HasPrefix(line, "data: "):
			cur.data = strings.TrimPrefix(line, "data: ")
			out = append(out, cur)
			cur = sseEvent{}
		}
	}
	if len(out) < n {
		t.Fatalf("got %d events, want %d (err: %v)", len(out), n, sc.Err())
	}
	return out
}

// streamRequest opens /stream and returns the response; caller closes the body.
func streamRequest(t *testing.T, url, lastEventID string, gzipped bool) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url+"/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	if gzipped {
		req.Header.Set("Accept-Encoding", "gzip")
	}
	// DisableCompression stops the transport adding its own Accept-Encoding and
	// transparently decoding, so the test controls exactly what is on the wire.
	c := &http.Client{Transport: &http.Transport{DisableCompression: true}, Timeout: 10 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	return resp
}

func TestHubRingEvictsOldest(t *testing.T) {
	h := newTestHub(t, 3)
	mustPublish(t, h, "a", "b", "c", "d", "e")
	start, batch := h.since(0)
	if start != 3 || len(batch) != 3 {
		t.Fatalf("start=%d len=%d, want 3/3 (oldest two evicted)", start, len(batch))
	}
	// Seq must survive eviction: line "c" is still seq 3.
	got, err := h.enc.open(3, batch[0])
	if err != nil {
		t.Fatal(err)
	}
	if got != "c" {
		t.Errorf("got %q want %q", got, "c")
	}
}

func TestHubSinceResumesAfterSeq(t *testing.T) {
	h := newTestHub(t, 10)
	mustPublish(t, h, "a", "b", "c", "d")
	start, batch := h.since(2)
	if start != 3 {
		t.Errorf("start=%d, want 3 (Last-Event-ID 2 means send 3 onward)", start)
	}
	if len(batch) != 2 {
		t.Fatalf("len=%d, want 2", len(batch))
	}
	if _, empty := h.since(4); len(empty) != 0 {
		t.Error("since(latest) must return nothing")
	}
	if _, ahead := h.since(99); len(ahead) != 0 {
		t.Error("since(beyond latest) must return nothing, not panic")
	}
}

func TestSSEStreamsCiphertextOnly(t *testing.T) {
	h := newTestHub(t, 10)
	srv := httptest.NewServer(newMux(h))
	defer srv.Close()

	const secret = "TOPSECRET-plaintext-marker"
	mustPublish(t, h, secret, "second line")

	resp := streamRequest(t, srv.URL, "", false)
	defer func() { _ = resp.Body.Close() }()
	if enc := resp.Header.Get("Content-Encoding"); enc != "" {
		t.Errorf("Content-Encoding %q without Accept-Encoding: gzip", enc)
	}
	events := readEvents(t, resp.Body, 2)

	for _, e := range events {
		if strings.Contains(e.data, secret) || strings.Contains(e.data, "second line") {
			t.Fatalf("PLAINTEXT ON THE WIRE: %q", e.data)
		}
	}
	if events[0].id != 1 || events[1].id != 2 {
		t.Fatalf("ids %d,%d want 1,2", events[0].id, events[1].id)
	}
	got, err := h.enc.open(events[0].id, events[0].data)
	if err != nil {
		t.Fatal(err)
	}
	if got != secret {
		t.Errorf("decrypted %q want %q", got, secret)
	}
}

func TestSSEResumeFromLastEventID(t *testing.T) {
	h := newTestHub(t, 10)
	srv := httptest.NewServer(newMux(h))
	defer srv.Close()
	mustPublish(t, h, "one", "two", "three", "four")

	resp := streamRequest(t, srv.URL, "2", false)
	defer func() { _ = resp.Body.Close() }()
	events := readEvents(t, resp.Body, 2)
	if events[0].id != 3 {
		t.Errorf("first replayed id %d, want 3", events[0].id)
	}
	got, err := h.enc.open(events[0].id, events[0].data)
	if err != nil {
		t.Fatal(err)
	}
	if got != "three" {
		t.Errorf("got %q want %q", got, "three")
	}
}

func TestSSELiveDelivery(t *testing.T) {
	h := newTestHub(t, 10)
	srv := httptest.NewServer(newMux(h))
	defer srv.Close()

	resp := streamRequest(t, srv.URL, "", false)
	defer func() { _ = resp.Body.Close() }()

	go func() {
		time.Sleep(50 * time.Millisecond) // after the handler is subscribed
		_ = h.publish("live line")
	}()
	events := readEvents(t, resp.Body, 1)
	got, err := h.enc.open(events[0].id, events[0].data)
	if err != nil {
		t.Fatal(err)
	}
	if got != "live line" {
		t.Errorf("got %q want %q", got, "live line")
	}
}

// TestSSEGzipPath guards the Cloudflare workaround: with Accept-Encoding: gzip
// the origin must respond gzip-encoded (the edge otherwise buffers SSE to EOF).
func TestSSEGzipPath(t *testing.T) {
	h := newTestHub(t, 10)
	srv := httptest.NewServer(newMux(h))
	defer srv.Close()
	mustPublish(t, h, "gzipped line")

	resp := streamRequest(t, srv.URL, "", true)
	defer func() { _ = resp.Body.Close() }()
	if got := resp.Header.Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding %q, want gzip (Cloudflare SSE workaround)", got)
	}
	zr, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	events := readEvents(t, zr, 1)
	got, err := h.enc.open(events[0].id, events[0].data)
	if err != nil {
		t.Fatal(err)
	}
	if got != "gzipped line" {
		t.Errorf("got %q want %q", got, "gzipped line")
	}
}

func TestViewerServed(t *testing.T) {
	h := newTestHub(t, 10)
	srv := httptest.NewServer(newMux(h))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type %q", ct)
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "connect-src 'self'") {
		t.Errorf("CSP missing or too loose: %q", csp)
	}
	for _, want := range []string{"EventSource", "AES-GCM", "#k="} {
		if !strings.Contains(string(body), want) {
			t.Errorf("embedded viewer missing %q", want)
		}
	}
}

func TestUnknownPath404(t *testing.T) {
	h := newTestHub(t, 10)
	srv := httptest.NewServer(newMux(h))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/etc/passwd")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status %d, want 404", resp.StatusCode)
	}
}
