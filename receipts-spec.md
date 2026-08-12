# poptail — Signed session receipts (spec)

> Unbuilt. Design work for the one differentiator no incumbent (sshx, wush, bitbang)
> ships: a cryptographically verifiable record of what was streamed.

**Author**: Emmanuel Joliet
**Date**: 2026-08-11
**Status**: Spec — ready for implementation
**Repo**: `ejoliet/poptail`

---

## Context

v1 shipped: tail → redact → AES-256-GCM → SSE → cloudflared quick tunnel, with an
embedded WebCrypto viewer. The other half of the original "wow pack" — QR-in-terminal
onboarding — is **done**, as the `-qr` flag. It uses `skip2/go-qrcode` (pure Go, no
cgo) rather than the vendored ~600 LOC encoder this document originally specified;
poptail accepted a second direct dependency instead of maintaining vendored crypto-adjacent
code. That decision is recorded in `implementation-notes.md` (2026-08-08).

What remains unbuilt is the receipt feature below.

> 💡 Positioning line: "Run one command. Point your phone at the terminal.
> Watch the log live — and walk away with a signed receipt of every line you saw."

---

## What it is

A tamper-evident export proving "these exact lines were streamed in this session, in this
order." Same pattern as popdrop's chunk-digest chain, applied to log lines. Target users:
incident reviews, support handoffs, compliance-ish "what did you see" disputes.

## Design

**Hash chain (host side, over plaintext before encryption):**

```
h_0 = SHA256("poptail-receipt-v1" || session_id)
h_n = SHA256(h_{n-1} || line_n_bytes)
```

- Host keeps only `h_n` (32 bytes) + line counter. O(1) memory — firehose-safe.
- On session end (or `--receipt-interval`), host signs: `sig = Ed25519(sk_session, h_n || count || start_ts || end_ts)`.
- **Session keypair is ephemeral**: generated in-memory at startup, public key embedded in the viewer URL metadata (first SSE event), private key never touches disk.

**Viewer side:**

- Viewer computes the same chain over decrypted lines it received.
- "Export receipt" button → `receipt.json`: `{session_id, pubkey, count, start_ts, end_ts, chain_head, sig, gaps:[...]}` plus optional full scrollback.
- Reconnect gaps: any missed line ranges recorded in `gaps[]`; receipt is then explicitly partial.

**Verify CLI:**

```bash
poptail verify receipt.json scrollback.txt
# → OK: 48,201 lines, chain head matches, signature valid, 0 gaps
# → FAIL: line 3,117 mismatch (tampered or wrong file)
```

> ⚠️ Honest claim only: receipt proves integrity + order of the streamed lines under the
> session pubkey. It does NOT prove the host machine's identity or that the log wasn't
> forged before poptail read it. Say so in README — over-claiming here burns trust.

## Gate (GO/NO-GO)

- [ ] Receipt verifies after a forced viewer reconnect mid-stream (gaps handled or empty)
- [ ] Flipping one byte in the exported scrollback → verify FAILS at the correct line
- [ ] Chain adds no measurable lag at 1,000 lines/s (re-run the firehose check with chain on)
- [ ] `poptail verify` runs offline, stdlib only

## Invariants

- **Never commit a private key.** Session Ed25519 keys are ephemeral, in-memory only. Any future persistent signing key: `gitignore` BEFORE keygen.
- Key stays in the URL fragment; server/tunnel never sees plaintext or key.
- Single static Go binary; viewer stays single-file vanilla JS, no build step.
- Redaction runs BEFORE the chain — the receipt attests to what was streamed, which is the redacted text.
- Run `ship-check` before any public push.

## File map deltas

| File | Change |
|------|--------|
| `receipt.go` | NEW — SHA-256 chain + Ed25519 sign (stdlib `crypto/ed25519`) |
| `main.go` | `verify` subcommand; receipt export wiring |
| `viewer/index.html` | chain mirror in JS (WebCrypto SHA-256), gap tracker, Export receipt button |
| `README.md` | receipt honesty paragraph, verify usage |

## Build order

| Phase | Deliverable | Done when |
|-------|-------------|-----------|
| 1 | host chain + sign | unit tests: chain determinism, sig verify |
| 2 | viewer mirror + export | gate passes end-to-end |
| 3 | `poptail verify` | offline verify on real exports, tamper-detect test |

## Non-goals

- Write access / input of any kind — poptail stays read-only broadcast by design
- Multi-viewer presence, chat, cursors (sshx's territory)
- Persistent host identity keys (v2 candidate: "verified host" receipts)

## Open questions

- [ ] `--receipt-interval` periodic checkpoint signatures, or final-only? Lean final-only.
- [ ] Receipt covers redacted lines only — is a "N secrets masked" count in the receipt useful, or does it leak signal? Lean: include the count, not the positions.
