// spike3.go — poptail Spike 3 (THROWAWAY — not production code)
// Spike 2 crypto + firehose-ready viewer. Gate: 1,000 lines/s for 60s —
// viewer responsive, memory flat (ring buffer), no SSE backpressure stall.
// Viewer: rAF-batched DOM writes, 5,000-line cap with trim (Open Question 3),
// live stats (rx rate, gaps, decrypt fails).
//
// Usage:
//   go run ./spike3 /tmp/spike.log
//   some-cmd | go run ./spike3 -
//
// Requires: Go 1.22+, cloudflared on PATH.
package main

import (
	"bufio"
	"compress/gzip"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
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

// AIDEV-NOTE: 2000 matches the v1 default. At 1,000 lines/s this is only a 2s
// reconnect window — a viewer that falls further behind jumps to oldest
// available (ring semantics, by design).
const bufferSize = 2000 // replay buffer for late joiners / reconnects

// AIDEV-NOTE: seq is 1-based; Last-Event-ID: N means "I have N, send N+1 onward".
// Buffer stores ciphertext (base64 of nonce||ct), so replay needs no re-encryption
// and plaintext never sits in server memory beyond the publish call.
type hub struct {
	mu    sync.Mutex
	gcm   cipher.AEAD
	lines []string // ring of last bufferSize encrypted lines
	first int      // seq of lines[0]
	next  int      // seq to assign to the next line (== first+len(lines))
	subs  map[chan int]struct{}
}

func newHub(gcm cipher.AEAD) *hub {
	return &hub{gcm: gcm, first: 1, next: 1, subs: make(map[chan int]struct{})}
}

func (h *hub) publish(line string) {
	h.mu.Lock()
	// AIDEV-NOTE: seq as AAD → reorder/replay tampering fails decryption in viewer.
	// AAD encoding = ASCII decimal, matching JS String(e.lastEventId).
	nonce := make([]byte, h.gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		panic(err) // spike: crypto/rand failure is not recoverable
	}
	ct := h.gcm.Seal(nonce, nonce, []byte(line), []byte(strconv.Itoa(h.next)))
	h.lines = append(h.lines, base64.StdEncoding.EncodeToString(ct))
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

		flush := func() {
			start, batch := h.since(last)
			for i, line := range batch {
				fmt.Fprintf(out, "id: %d\ndata: %s\n\n", start+i, line)
				last = start + i
			}
			if len(batch) > 0 {
				doFlush()
			}
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

// AIDEV-NOTE: decrypts are chained on one promise so lines render in seq order
// even though WebCrypto resolves async. AAD must match server: ASCII decimal seq.
// AIDEV-NOTE: firehose viewer — decrypted lines land in a pending array; one
// requestAnimationFrame loop appends them as a single text node per frame and
// trims the <pre> to maxLines. Per-event DOM writes die at 1,000 lines/s.
const viewerHTML = `<!doctype html><meta charset="utf-8">
<title>poptail spike3</title>
<style>body{background:#111;color:#0f0;font:12px/1.4 monospace;margin:0}
pre{margin:0;padding:8px;white-space:pre-wrap;word-break:break-all}
#s{position:fixed;top:4px;right:8px;color:#888;background:#111}</style>
<div id="s">connecting…</div><pre id="log"></pre>
<script>
const log=document.getElementById('log'),s=document.getElementById('s');
const td=new TextDecoder(),te=new TextEncoder();
const maxLines=5000; // Open Question 3: DOM cap with trim
let pend=[],domLines=0,total=0,winCount=0,rate=0,lastId=0,gaps=0,fails=0,state='connecting…';
function b64u(x){x=x.replace(/-/g,'+').replace(/_/g,'/');return Uint8Array.from(atob(x),c=>c.charCodeAt(0))}
if(!location.hash.startsWith('#k=')){s.textContent='missing #k= key in URL';throw new Error('no key')}
let chain=crypto.subtle.importKey('raw',b64u(location.hash.slice(3)),'AES-GCM',false,['decrypt']);
const es=new EventSource('/stream');
es.onopen=()=>state='live';
es.onerror=()=>state='reconnecting…'; // EventSource auto-retries with Last-Event-ID
es.onmessage=e=>{chain=chain.then(async key=>{
  const b=Uint8Array.from(atob(e.data),c=>c.charCodeAt(0));
  const id=+e.lastEventId;
  if(lastId&&id>lastId+1)gaps+=id-lastId-1; // ring-buffer jump (fell >buffer behind)
  lastId=id;
  try{
    const pt=await crypto.subtle.decrypt(
      {name:'AES-GCM',iv:b.slice(0,12),additionalData:te.encode(e.lastEventId)},key,b.slice(12));
    pend.push(td.decode(pt));total++;winCount++;
  }catch(err){fails++}
  return key;
});};
function tick(){
  if(pend.length){
    log.appendChild(document.createTextNode(pend.join('\n')+'\n'));
    domLines+=pend.length;pend=[];
    if(domLines>maxLines){ // trim: rebuild pre with last maxLines
      log.textContent=log.textContent.split('\n').slice(-maxLines-1).join('\n');
      domLines=maxLines;
    }
    window.scrollTo(0,document.body.scrollHeight);
  }
  requestAnimationFrame(tick);
}
requestAnimationFrame(tick);
setInterval(()=>{rate=winCount;winCount=0;
  s.textContent=state+' | '+total+' rx | '+rate+'/s | gaps '+gaps+' | fails '+fails;},1000);
</script>`

func startTunnel(port int) (*exec.Cmd, string, error) {
	cmd := exec.Command("cloudflared", "tunnel", "--url", fmt.Sprintf("http://127.0.0.1:%d", port))
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
		fmt.Fprintln(os.Stderr, "usage: spike3 <file>  |  cmd | spike3 -")
		os.Exit(1)
	}
	src := os.Args[1]

	key := make([]byte, 32) // AES-256
	if _, err := rand.Read(key); err != nil {
		fmt.Fprintln(os.Stderr, "spike3:", err)
		os.Exit(1)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		fmt.Fprintln(os.Stderr, "spike3:", err)
		os.Exit(1)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		fmt.Fprintln(os.Stderr, "spike3:", err)
		os.Exit(1)
	}
	frag := "#k=" + base64.RawURLEncoding.EncodeToString(key)
	h := newHub(gcm)

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
			fmt.Fprintln(os.Stderr, "spike3:", err)
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
		fmt.Fprintln(os.Stderr, "spike3:", err)
		os.Exit(1)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	go func() { _ = http.Serve(ln, mux) }()
	// AIDEV-NOTE: key printed only as part of the share URL, never logged separately.
	fmt.Printf("spike3: local http://127.0.0.1:%d/%s\n", port, frag)

	tun, url, err := startTunnel(port)
	if err != nil {
		fmt.Fprintln(os.Stderr, "spike3: tunnel failed:", err)
		fmt.Fprintln(os.Stderr, "spike3: local URL still works on this machine")
	} else {
		fmt.Printf("spike3: share %s/%s\n", url, frag)
	}
	fmt.Println("spike3: Ctrl-C to kill the link")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	if tun != nil && tun.Process != nil {
		_ = tun.Process.Kill() // AIDEV-NOTE: no orphan cloudflared (acceptance criterion)
	}
}
