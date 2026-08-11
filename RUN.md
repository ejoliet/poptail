# Spike 1 — run & gate

Requires: Go 1.22+, cloudflared on PATH (`brew install cloudflared`).

```bash
cd poptail-spike1
go mod tidy
# terminal 1: synthetic log
while true; do date >> /tmp/spike.log; sleep 1; done
# terminal 2
go run ./spike1 /tmp/spike.log     # prints https://<x>.trycloudflare.com
# stdin mode
kubectl logs -f deploy/foo | go run ./spike1 -
```

Note: /stream is gzip-encoded when the client accepts gzip — required,
Cloudflare's quick-tunnel edge buffers uncompressed streams until EOF
(see implementation-notes.md). Curl it with `curl -N --compressed`.

Verified in sandbox (no tunnel egress there): live SSE with ids,
Last-Event-ID replay (17 -> resumes at 18), viewer HTML, stdin mode,
clean error path when cloudflared missing.

## GO/NO-GO gate (you, on phone over LTE)
1. Open share URL. Lines appear < 2s.
2. Watch 15 min (heartbeat keeps tunnel alive).
3. Airplane mode 30s, back on -> viewer resumes with NO missing lines.
4. Repeat once forcing TCP: edit spike1.go startTunnel to add
   "--protocol", "http2" -> gate must still hold.
   (--protocol is a hidden flag, not in `cloudflared tunnel --help`, but valid.
   Do NOT use --http2-origin — that switches origin-side HTTP/2, which the
   plain net/http server rejects.)

Gate PASSED 2026-08-08 (QUIC + forced http2, phone over LTE).

NO-GO fallback: chunked long-polling with seq cursor (see README Next Steps).

# Spike 2 — run & gate

Spike 1 transport + per-line AES-256-GCM. Key only in URL fragment (#k=…),
never sent to the server or Cloudflare. Wire carries base64(nonce||ciphertext);
seq is AES-GCM AAD, so reordered/replayed events fail decryption in the viewer.

```bash
# terminal 1: synthetic log
while true; do date >> /tmp/spike.log; sleep 1; done
# terminal 2
go run ./spike2 /tmp/spike.log    # prints share URL with #k=<key>
```

Verified 2026-08-08 (machine checks): curl on local AND tunnel /stream shows
ciphertext only; Last-Event-ID: 3 resumes at id 4; captured wire event
decrypts with the fragment key; wrong-seq AAD rejected.

## GO/NO-GO gate (you, in browsers)
1. Open share URL (with #k=) in Safari, Chrome, Firefox — lines decrypt live.
2. `curl -N --compressed <url>/stream` — ciphertext only, no plaintext.
3. Reload mid-stream — replay decrypts (status shows "live", no DECRYPT FAIL).

Gate PASSED 2026-08-08 (Safari + Chrome + Firefox).

# Spike 3 — run & gate

Spike 2 crypto + firehose-ready viewer: rAF-batched DOM writes, 5,000-line
DOM cap with trim (Open Question 3), live stats bar
(state | total rx | rate/s | gaps | fails). Replay buffer 2000 (v1 default) —
at 1,000 lines/s that is a 2s reconnect window; falling further behind jumps
to oldest available (ring semantics, gap counted in stats).

```bash
# terminal 1: firehose, 1,000 lines/s for 60s (60,000 lines), then idle
perl -e '$|=1;my $i=0;while($i<60000){for(1..10){printf "fh line %06d payload abcdefghijklmnopqrstuvwxyz\n",++$i}select(undef,undef,undef,0.01)}' \
  | go run ./spike3 -
```

Machine-verified 2026-08-08: 1,000 lines/s × 60s — server RSS flat
(12.65→12.74 MB over final 30s), curl consumer received ids 591→60000
contiguous (first 590 rotated out of ring before consumer attached, by
design), no SSE stall, last event delivered at generation pace.

## GO/NO-GO gate (you, on phone — mobile Safari)
1. Open share URL during firehose. Stats bar shows ~1000/s, fails 0.
2. Page stays responsive: scrolling works, stats keep updating, no freeze.
3. After 60s burst ends, viewer idles clean (heartbeat keeps stream alive).

Gate PASSED 2026-08-08 (mobile Safari: ~870 rx/s, gaps 0, fails 0, responsive).

# Build phase

All spike gates GO. Phase 0 (scaffold + Makefile + CI) done 2026-08-08:

```bash
make build   # go build ./...
make test    # go test ./...
make lint    # go vet + golangci-lint (spikes excluded — throwaway)
make cross   # compile check darwin/linux/windows × amd64/arm64
```

Phase 1 (tailer + redactor + crypto) done 2026-08-11.
Phase 2 (server + embedded viewer + `-local`) done 2026-08-11.
Phase 3 (cloudflared tunnel manager, `-qr`, `-protocol`) done 2026-08-11.

## Running v1

Requires cloudflared on PATH (`brew install cloudflared`) or
`POPTAIL_CLOUDFLARED=/path/to/binary` — auto-download deferred
(checksum source unresolved, see implementation-notes.md).

```bash
go build -o poptail .

# terminal 1: synthetic log
while true; do date >> /tmp/poptail.log; sleep 1; done
# terminal 2: prints https://<random>.trycloudflare.com/#k=<key>
./poptail -n 50 /tmp/poptail.log
# stdin mode
kubectl logs -f deploy/foo | ./poptail -
# phone: QR of the share URL in the terminal
./poptail -qr /tmp/poptail.log
# egress UDP 7844 blocked → force TCP
./poptail -protocol http2 /tmp/poptail.log
```

Open the printed URL — fragment (`#k=...`) included — anywhere.
Viewer has pause + filter; stats bar shows state | rx | rate/s | gaps | fails.

**`-local` caveat:** WebCrypto only exists on https or localhost origins, so
a `-local` link opened as `http://<lan-ip>` shows "WebCrypto unavailable" by
design. Same-machine viewing: replace the LAN IP with `127.0.0.1`. Cross-device:
use the tunnel (default mode). Tunnel failure falls back to LAN automatically.

```bash
curl -sN --compressed '<share-url-without-#k>stream'      # ciphertext only
curl -sN -H 'Last-Event-ID: 3' '<...>stream'              # resumes at id 4
go test -race -count=3 ./...                              # incl. both integration tests
```

## Phase 3 gate (human): end-to-end via trycloudflare on macOS + Linux
1. `./poptail /tmp/poptail.log` → open share URL on phone (LTE, not wifi).
2. Lines appear < 2s; reload mid-stream decrypts (ring replay).
3. Ctrl-C → link dead, `pgrep cloudflared` shows no NEW orphan
   (a pre-existing `cloudflared tunnel run --token` system service is not ours).
4. Repeat once with `-protocol http2`.
5. Linux box: repeat step 1-3 once.

Next: phase 4 — cross-compile release matrix (`-o dist/…`, binaries < 15 MB,
clean-VM run check).
