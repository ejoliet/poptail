# poptail

**Share a live `tail -f` as a temporary, end-to-end-encrypted URL. One binary. Link dies when you Ctrl-C.**

Type: RDD spec (Type A) — agent implements after spike gates pass.
Status: BUILD phase. All three spike gates **GO** (2026-08-08):
- Spike 1 (transport): phone over LTE, QUIC + forced http2.
- Spike 2 (crypto): ciphertext-only on wire through tunnel; decrypt verified
  in Safari + Chrome + Firefox.
- Spike 3 (firehose): server flat at ~12.7 MB over 1,000 lines/s × 60s, all
  events contiguous; mobile Safari stayed responsive at ~870 rx/s
  (decrypt-bound), gaps 0, fails 0.

Build Order progress: phase 0 (scaffold + Makefile + CI) done 2026-08-08;
phase 1 (tailer + redactor + crypto) done 2026-08-11; phase 2 (server +
embedded viewer + `-local`) done 2026-08-11; phase 3 (tunnel manager) done
2026-08-11 (gate passed: end-to-end via trycloudflare, user-verified);
phase 4 (release matrix) done 2026-08-11 — `make cross` emits stripped,
version-stamped binaries for all 6 platforms (6.1-6.8 MB, ceiling 15 MB),
`make checksums` adds SHA256SUMS, tag push (`v*`) triggers the GitHub release
workflow, and `install.sh` is the checksum-verified `curl | sh` installer.
33 tests incl. integration tests driving the real binary in both modes.
`make build test lint cross` all green, `-race` clean over repeated runs.
Remaining before public: first tag + clean-VM run check (human), ship-check.

All CLI-contract flags are live. Phase 3 deviation: cloudflared auto-download
(steps 3 of the tunnel spec) is deferred — Open Question 1 (trusted checksum
source) is unresolved, and executing an unverified download is the wrong
default. Missing binary → clear install hint + `-local` suggestion.
Note: `-local` viewers need `localhost` — WebCrypto does not exist on plain
`http://<lan-ip>` origins (non-secure context); the tunnel's https URL is the
cross-device path. Spikes kept under `spike1/ spike2/ spike3/`, excluded from
lint.

---

## Purpose

**Problem.** "Look at my build log" today means screensharing, pasting stale chunks into Slack, or granting SSH access. All heavy, all wrong for a 10-minute debugging session.

**Solution.** `poptail /var/log/build.log` prints a `https://<random>.trycloudflare.com/#k=<key>` link. Anyone with the link watches the log live, read-only, in a browser. Log lines are AES-GCM encrypted client-side; the key lives in the URL fragment and never reaches Cloudflare. Kill the process, the link is dead.

**Who benefits.** Anyone running long jobs on remote boxes: CI debugging, pipeline ops, pair-debugging across institutions and networks.

## Install

```bash
curl -sSfL https://raw.githubusercontent.com/ejoliet/poptail/main/install.sh | sh
```

Detects OS/arch (macOS/Linux, amd64/arm64), downloads the latest release
binary, verifies it against the release's SHA256SUMS, installs to
`~/.local/bin` (override: `POPTAIL_INSTALL_DIR`; pin a tag:
`POPTAIL_VERSION=v0.1.0`). Windows: download the `.exe` from releases.
Or: `go install github.com/ejoliet/poptail@latest`.

## Architecture

```
poptail <file>   |   cmd | poptail -
  ├─ tailer: nxadm/tail (file mode, rotation-aware) or bufio.Scanner on stdin
  ├─ redactor: default-on secret masking (AWS keys, Bearer/JWT, PEM blocks)
  ├─ encryptor: AES-256-GCM per line, random key + per-line nonce
  ├─ ring buffer: last N encrypted lines (default 2000) for late joiners
  ├─ HTTP server on 127.0.0.1:<random port>
  │    ├─ GET /        → embedded single-file viewer (go:embed, vanilla JS)
  │    └─ GET /stream  → SSE (id: monotonic seq, heartbeat every 20s,
  │                      Last-Event-ID resume from ring buffer)
  ├─ cloudflared manager: ensure binary → spawn quick tunnel → parse URL
  └─ stdout: share URL with #k=<base64url key> appended
```

> 💡 Fragment-key pattern proven in door-post-it. Cloudflare edge relays ciphertext only.
> 💡 Quick tunnels require no account; URL lives only while the process runs — matches poptail semantics exactly.

## Recommended Stack

| Layer | Chosen | Why | Rejected |
|-------|--------|-----|----------|
| Language | Go 1.22+ | Single static binary (user requirement), trivial cross-compile | Node (needs runtime) |
| Tail | `nxadm/tail` | Cross-platform incl. Windows; truncation/rotation detection; active drop-in replacement for abandoned hpcloud/tail | `go-faster/tail` (Linux/Darwin only), hand-rolled fsnotify (rotation edge cases) |
| CLI parsing | stdlib `flag` | One command, ~8 flags; no framework needed | cobra, urfave/cli (bloat) |
| HTTP + SSE | stdlib `net/http` | SSE is ~30 lines with `http.Flusher` | r3labs/sse (unneeded dep) |
| Crypto | stdlib `crypto/aes` + GCM | AEAD, WebCrypto-compatible in viewer | libsodium binding (cgo breaks static build) |
| Viewer | single HTML file, vanilla JS, `go:embed` | Locked stack: no build step | any framework |
| Tunnel | `cloudflared` quick tunnel, auto-downloaded | No account, ephemeral URL, works through egress-only firewalls | PeerJS/WebRTC (headless + symmetric NAT needs TURN), ngrok (account + interstitial) |

## Repository Layout

```
poptail/
├── main.go              # flag parsing, wiring, shutdown
├── tailer.go            # file/stdin sources → chan Line
├── redact.go            # default patterns + custom regex flag
├── crypto.go            # key gen, per-line seal; seq as AAD
├── server.go            # HTTP, SSE, ring buffer
├── tunnel.go            # cloudflared ensure/download/spawn/parse
├── viewer/index.html    # embedded viewer (WebCrypto decrypt, autoscroll, pause, search)
├── *_test.go
├── Makefile             # build, test, cross-compile matrix, lint
└── README.md
```

## CLI Contract

```
poptail [flags] <file>
<command> | poptail [flags] -

Flags:
  -n int          initial lines to show (default 50)
  -buffer int     ring buffer size for late joiners (default 2000)
  -no-redact      disable secret masking (default: masking ON)
  -redact string  extra redaction regex (repeatable)
  -qr             print QR code of share URL in terminal
  -expire dur     self-terminate after duration, e.g. 2h (default: none)
  -local          skip tunnel; serve on LAN IP only
  -protocol str   cloudflared edge protocol: auto|quic|http2 (default auto;
                  use http2 when egress UDP 7844 is blocked)
  -version
```

Output contract (stdout, parseable):
```
poptail: sharing /var/log/build.log (redaction: on)
poptail: link: https://abc-def.trycloudflare.com/#k=Qm9v...
poptail: Ctrl-C to kill the link
```

## Configuration Reference

| Env var | Type | Default | Purpose |
|---------|------|---------|---------|
| `POPTAIL_CLOUDFLARED` | path | auto | Use existing cloudflared binary; skip download |
| `POPTAIL_CACHE_DIR` | path | `~/.poptail` | Downloaded binary cache |
| `NO_COLOR` | bool | unset | Disable terminal color |

No secrets required. No account required.

## cloudflared Auto-Download (tunnel.go)

1. `POPTAIL_CLOUDFLARED` set → use it.
2. `cloudflared` on PATH → use it.
3. Else download latest release binary for GOOS/GOARCH from `github.com/cloudflare/cloudflared` releases → verify against published checksums → chmod +x → cache in `POPTAIL_CACHE_DIR/bin/`.
4. Spawn `cloudflared tunnel --url http://127.0.0.1:<port>`; parse `https://*.trycloudflare.com` from stderr with 30s timeout.
5. On parse timeout or spawn failure → print LAN fallback URL and the error; exit non-zero only if `-local` also impossible.

> ⚠️ Quick tunnel limits: ~200 in-flight requests (429 beyond), no uptime SLA, possible long-lived-connection drops. SSE resume via Last-Event-ID is therefore mandatory, not optional.

## Kubernetes / EKS Usage

**Preferred: run poptail where kubectl runs, not in the pod.**

```bash
kubectl logs -f deploy/webserver | poptail -
```

No pod changes, works with distroless/read-only containers, log data leaves the cluster only via kubectl's authenticated channel.

**In-pod mode (only when tailing a file not on stdout):**

| Constraint | Handling |
|-----------|----------|
| Egress | cloudflared needs outbound port 7844 (UDP for quic, TCP for http2) + HTTPS to `api.trycloudflare.com`. If UDP blocked → `-protocol http2`. Allowlist-only egress needs a rule first. |
| Read-only rootfs | Static binary OK; set `POPTAIL_CACHE_DIR` to an emptyDir/`/tmp`, or bake cloudflared into the image |
| Getting binary in | `kubectl cp` (needs `tar` in container) or `kubectl debug` ephemeral container carrying poptail |
| Non-root securityContext | Fully supported — no privileged ports, no capabilities |

> ⚠️ In-pod mode routes log data through Cloudflare's edge (as ciphertext). May need institutional sign-off; prefer the kubectl pipe.

## Error Handling

| Error | Behavior |
|-------|----------|
| File not found / unreadable | exit 1, clear message |
| File rotated/truncated | nxadm/tail ReOpen: continue seamlessly, emit `--- rotated ---` marker line |
| cloudflared download fails | offer `-local` fallback, print manual install hint |
| Tunnel drops mid-session | cloudflared auto-reconnects; viewer reconnects via SSE retry + Last-Event-ID |
| Viewer joins late | replay ring buffer from seq 0 or Last-Event-ID |
| SIGINT/SIGTERM | kill cloudflared child, close SSE streams, exit 0 |

## Security Invariants

- Redaction ON by default (cullroom rule: assume logs contain secrets).
- Key only in URL fragment; server never logs the key; key not derivable from anything server-side.
- Seq number as AES-GCM additional authenticated data → reorder/replay tampering detectable in viewer.
- Read-only: no endpoint accepts input other than `Last-Event-ID` header.
- Runs fully unprivileged: no root, no capabilities, no ports < 1024, outbound-only network. Only permission needed is read access to the tailed file.
- Run ship-check before any release or repo publication.

## Testing

- Unit: redactor patterns, crypto round-trip (Go seal → reference WebCrypto vectors), ring buffer resume logic.
- Integration: spawn poptail against a synthetic log writer; curl SSE; assert ciphertext-only on wire.
- Manual spike gates below are the real acceptance tests.

## Non-Goals (v1)

- No multi-file / multiplexed tails.
- No persistence, history export, or search server-side.
- No auth beyond the link itself (link = capability).
- No named/stable URLs (that's a v2 with user's own Cloudflare account).
- No Windows *service* mode (binary runs fine interactively on Windows).

## Open Questions

1. Checksum verification source for cloudflared releases — pin format may change; resolve during Spike 1.
2. ~~`-qr` in v1 or defer?~~ **Resolved 2026-08-08: `-qr` is in v1.** Use `skip2/go-qrcode` (pure Go, no cgo, terminal output via `ToSmallString`); hand-rolled ASCII QR rejected as needless code.
3. Viewer line cap: 5,000 DOM lines with virtual trim — confirm smooth on mobile Safari during Spike 3.

## Agent Build Instructions

> Implement only after Spike 1 gate is GO. Resolve Open Questions first.

### Spike 1 Brief (agent-ready, build THIS first)

Deliverable: ONE throwaway file `spike1.go` (~100-150 lines) + nothing else. Not production code. No tests, no Makefile, no repo scaffold.

**In scope (all four required — gate depends on them):**
1. Tail: `nxadm/tail` on `os.Args[1]`, or `bufio.Scanner` on stdin if arg is `-`. Start at end of file.
2. SSE at `GET /stream`: monotonic `id:` per line, in-memory slice of last 500 lines, replay from `Last-Event-ID` on reconnect, comment heartbeat every 20s.
3. Viewer at `GET /`: inline HTML string constant. `<pre>` + `EventSource` + autoscroll. Nothing else.
4. Tunnel: assume `cloudflared` on PATH (no download logic). Spawn `cloudflared tunnel --url http://127.0.0.1:<port>`, regex the `https://*.trycloudflare.com` URL from stderr (30s timeout), print it. Kill child on SIGINT.

**Explicitly OUT of scope:** encryption, redaction, all flags, QR, expire, `-local`, `-protocol`, cloudflared download, ring-buffer tuning, viewer features (pause/search), Windows.

**Self-check script the agent must provide alongside spike1.go:**
```bash
# terminal 1: synthetic log
while true; do date >> /tmp/spike.log; sleep 1; done
# terminal 2
go run spike1.go /tmp/spike.log   # prints tunnel URL
# terminal 3: verify stream + resume
curl -sN <url>/stream | head -5
curl -sN -H "Last-Event-ID: 3" <url>/stream | head -3   # must replay from id 4
```

Human gate (Emmanuel, not agent): open URL on phone over LTE, watch 15 min, toggle airplane mode, confirm no gaps. Repeat once with UDP 7844 blocked or `cloudflared --protocol http2`.

### Spike Gates (GO/NO-GO, in order)

| # | Spike | Gate |
|---|-------|------|
| 1 | Transport: tail → SSE → quick tunnel → phone on LTE | Lines < 2s latency; survives 15 min; kill wifi on viewer → reconnect resumes with no gaps. Repeat once from a network with UDP 7844 blocked (or force `-protocol http2`) — gate holds over TCP fallback too |
| 2 | Crypto: fragment key, WebCrypto decrypt | `curl` on tunnel URL shows ciphertext only; decrypt works in Safari + Chrome + Firefox |
| 3 | Firehose: 1,000 lines/s for 60s | Viewer responsive; memory flat (ring buffer); no SSE backpressure stall |

### Build Order (post-GO)

| Phase | Deliverable | Done when |
|-------|-------------|-----------|
| 0 | Scaffold + Makefile + CI | `make lint test` passes |
| 1 | tailer + redactor + crypto | unit tests pass |
| 2 | server + embedded viewer | integration test passes locally with `-local` |
| 3 | tunnel manager | end-to-end via trycloudflare works on macOS + Linux |
| 4 | cross-compile matrix (darwin/linux/windows, amd64/arm64) | binaries < 15 MB, run on clean VMs |

### Constraints

- Go stdlib + nxadm/tail + skip2/go-qrcode (for `-qr`) only. No cgo (static build).
- `AIDEV-` comments at every non-obvious decision point.
- Secrets never logged, never in test fixtures.
- `golangci-lint` clean.

### Acceptance Criteria

- [ ] All three spike gates GO
- [ ] `cat` a 500 MB log then tail it: startup < 1s (seek to end, don't read file)
- [ ] Stdin mode: `kubectl logs -f pod | poptail -` works
- [ ] Redaction masks AWS key + Bearer token fixtures by default
- [ ] Ctrl-C leaves no orphan cloudflared process
- [ ] Ship-check passes before repo goes public

## Next Steps

1. Spike 1 (half day): ~100-line throwaway Go file — tail, SSE, spawn cloudflared, no crypto. Test from phone on LTE.
2. GO → Spike 2 (crypto), Spike 3 (firehose).
3. GO → scaffold repo per Build Order.
4. NO-GO on Spike 1 (long-lived SSE unusable through quick tunnels) → fallback design: chunked long-polling with seq cursor; re-run gate.
