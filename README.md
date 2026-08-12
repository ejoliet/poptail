# poptail

**Share a live `tail -f` as a temporary, end-to-end-encrypted URL. One binary. Link dies when you Ctrl-C.**

```bash
poptail /var/log/build.log
```

```
poptail: sharing /var/log/build.log (redaction: on)
poptail: link: https://abc-def.trycloudflare.com/#k=Qm9v...
poptail: Ctrl-C to kill the link
```

Anyone with the link watches the log live, read-only, in a browser. Log lines are AES-256-GCM encrypted client-side; the key lives in the URL fragment and never reaches Cloudflare. Kill the process, the link is dead.

## Why

"Look at my build log" today means screensharing, pasting stale chunks into Slack, or granting SSH access. All heavy, all wrong for a 10-minute debugging session. poptail is the lightweight answer: one command, one ephemeral link, zero accounts.

## Install

```bash
curl -sSfL https://raw.githubusercontent.com/ejoliet/poptail/main/install.sh | sh
```

Detects OS/arch (macOS/Linux, amd64/arm64), downloads the latest release
binary, verifies it against the release's SHA256SUMS, installs to
`~/.local/bin` (override: `POPTAIL_INSTALL_DIR`; pin a tag:
`POPTAIL_VERSION=v0.1.0`). Windows: download the `.exe` from releases.
Or: `go install github.com/ejoliet/poptail@latest`.

## Examples

### 1. Watch a long test run from your phone

```bash
make test 2>&1 | poptail -qr -
```

QR code prints in the terminal. Scan with your phone, walk away from the desk. Link dies on Ctrl-C.

### 2. Share a log without leaking secrets

```bash
poptail /var/log/app.log
```

Redaction is **on by default**: AWS keys, Bearer/JWT tokens, and PEM blocks are masked before encryption. Add your own patterns with `-redact '<regex>'`, or disable with `-no-redact` if you're sure.

### 3. Stream remote logs through your laptop

```bash
ssh prod-box 'tail -f /var/log/nginx/error.log' | poptail -
kubectl logs -f deploy/webserver | poptail -
```

Nothing installed on the remote host or in the pod. Log data leaves the box only via your existing authenticated channel (SSH/kubectl); poptail encrypts and tunnels from your machine.

### 4. Monitor an ML training run

```bash
python -u train.py 2>&1 | poptail -qr -expire 8h -
```

Kick off training, scan the QR, check loss curves from anywhere. `-expire 8h` kills the link automatically even if you forget.

### Nothing showing with `cmd | poptail -`?

Your producer is block-buffering. Most programs (python, grep, awk, node) switch stdout from line-buffered to block-buffered (4–64 KB) when piped — lines sit in the producer's buffer and never reach poptail. Fixes:

```bash
python -u script.py | poptail -        # or PYTHONUNBUFFERED=1
grep --line-buffered ERROR f | poptail -
stdbuf -oL cmd | poptail -             # Linux / brew coreutils (gstdbuf)
script -q /dev/null cmd | poptail -    # macOS fallback
```

## CLI

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

Note: `-local` viewers need `localhost` — WebCrypto does not exist on plain
`http://<lan-ip>` origins (non-secure context); the tunnel's https URL is the
cross-device path.

## Security

- **Redaction ON by default** — assume logs contain secrets.
- **End-to-end encrypted** — AES-256-GCM per line; the key travels only in the URL fragment (`#k=...`), which browsers never send to the server. Cloudflare's edge relays ciphertext only.
- **Tamper-evident** — sequence number is GCM additional authenticated data; reorder/replay is detectable in the viewer.
- **Read-only** — no endpoint accepts input other than the `Last-Event-ID` header.
- **Unprivileged** — no root, no capabilities, no ports < 1024, outbound-only network. Only permission needed is read access to the tailed file.
- **Ephemeral** — link = capability; Ctrl-C (or `-expire`) kills it. No account, no persistence.

## How it works

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
  ├─ cloudflared manager: spawn quick tunnel → parse URL
  └─ stdout: share URL with #k=<base64url key> appended
```

Single static Go binary, stdlib + `nxadm/tail` + `skip2/go-qrcode`, no cgo. Viewer is one embedded HTML file decrypting with WebCrypto — no build step, no framework.

### cloudflared

poptail needs `cloudflared` for the tunnel:

1. `POPTAIL_CLOUDFLARED` env var set → use it.
2. `cloudflared` on PATH → use it.
3. Missing → clear install hint + `-local` suggestion.

Quick tunnels require no Cloudflare account; the URL lives only while the process runs. Limits: ~200 in-flight requests, no uptime SLA, possible long-lived-connection drops — the viewer resumes seamlessly via SSE `Last-Event-ID`.

## Configuration

| Env var | Type | Default | Purpose |
|---------|------|---------|---------|
| `POPTAIL_CLOUDFLARED` | path | auto | Use existing cloudflared binary |
| `POPTAIL_CACHE_DIR` | path | `~/.poptail` | Binary cache |
| `NO_COLOR` | bool | unset | Disable terminal color |

No secrets required. No account required.

## Kubernetes / EKS

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

## Behavior details

| Situation | Behavior |
|-----------|----------|
| File not found / unreadable | exit 1, clear message |
| File rotated/truncated | continues seamlessly, emits `--- rotated ---` marker line |
| Tunnel drops mid-session | cloudflared auto-reconnects; viewer reconnects via SSE retry + Last-Event-ID |
| Viewer joins late | replays ring buffer (last `-buffer` lines) |
| SIGINT/SIGTERM | kills cloudflared child, closes SSE streams, exit 0 |
| 500 MB log | startup < 1s (seeks to end, doesn't read the file) |

## Non-goals (v1)

- No multi-file / multiplexed tails.
- No persistence, history export, or server-side search.
- No auth beyond the link itself (link = capability).
- No named/stable URLs (v2, with your own Cloudflare account).

## Development

```bash
make build          # local binary
make test           # unit + integration (drives the real binary)
make lint           # golangci-lint
make cross          # darwin/linux/windows × amd64/arm64
```
