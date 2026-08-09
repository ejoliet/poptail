# Spike 1 — run & gate

Requires: Go 1.22+, cloudflared on PATH (`brew install cloudflared`).

```bash
cd poptail-spike1
go mod tidy
# terminal 1: synthetic log
while true; do date >> /tmp/spike.log; sleep 1; done
# terminal 2
go run spike1.go /tmp/spike.log     # prints https://<x>.trycloudflare.com
# stdin mode
kubectl logs -f deploy/foo | go run spike1.go -
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
