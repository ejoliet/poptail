# Developing poptail

Requires Go 1.22+ and `cloudflared` on PATH (`brew install cloudflared`) or
`POPTAIL_CLOUDFLARED=/path/to/binary`. Auto-download is deliberately deferred —
no trusted checksum source pinned yet, see `implementation-notes.md`.

```bash
make build   # go build ./...
make test    # go test ./...
make lint    # go vet + golangci-lint
make cross   # darwin/linux/windows × amd64/arm64, 15 MB ceiling enforced
```

## Running it locally

```bash
go build -o poptail .

# terminal 1: synthetic log
while true; do date >> /tmp/poptail.log; sleep 1; done

# terminal 2: prints https://<random>.trycloudflare.com/#k=<key>
./poptail -n 50 /tmp/poptail.log
./poptail -qr /tmp/poptail.log            # QR of the share URL in the terminal
./poptail -protocol http2 /tmp/poptail.log # egress UDP 7844 blocked → force TCP
kubectl logs -f deploy/foo | ./poptail -   # stdin mode
```

Open the printed URL with the `#k=...` fragment included — without it the
viewer has no key. Viewer has pause + filter; stats bar shows
state | rx | rate/s | gaps | fails.

**`-local` caveat:** WebCrypto exists only on https or localhost origins, so a
`-local` link opened as `http://<lan-ip>` shows "WebCrypto unavailable" by
design. Same-machine viewing: replace the LAN IP with `127.0.0.1`.
Cross-device: use the tunnel (default mode). Tunnel failure falls back to LAN
automatically.

## Verifying the wire

`/stream` is gzip-encoded whenever the client accepts gzip — required, because
Cloudflare's quick-tunnel edge buffers uncompressed streams until EOF
(`implementation-notes.md` has the full finding). Curl it with `--compressed`.

```bash
curl -sN --compressed '<share-url-without-#k>stream'   # ciphertext only, no plaintext
curl -sN -H 'Last-Event-ID: 3' '<...>stream'           # must resume at id 4
go test -race -count=3 ./...                           # incl. both integration tests
```

## Cutting a release

```bash
make cross       # dist/poptail_<os>_<arch>[.exe] ×6, stripped, <15 MB enforced
make checksums   # + dist/SHA256SUMS
git tag v0.1.0 && git push origin v0.1.0   # triggers .github/workflows/release.yml
```

Then confirm on a clean box:

1. Release has 6 binaries + SHA256SUMS attached.
2. `curl -sSfL https://raw.githubusercontent.com/ejoliet/poptail/main/install.sh | sh`
   on a fresh macOS/Linux machine (or `docker run --rm -it debian sh` + curl);
   `poptail -version` prints the tag.
3. `poptail /tmp/x.log` works there (cloudflared on PATH, or expect the install hint).
