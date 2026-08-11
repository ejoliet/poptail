# poptail — Wow Pack Handoff

> Post-Gate-3 handoff. Adds the two features no incumbent (sshx, wush, bitbang) ships:
> QR-in-terminal onboarding and cryptographically verifiable session receipts.

**Author**: Emmanuel Joliet
**Date**: 2026-08-11
**Status**: Spec — ready for agent implementation
**Repo**: `ejoliet/poptail`

---

## Context

Gates 1–3 passed:

| # | Spike | Result |
|---|-------|--------|
| 1 | Transport: tail → SSE → quick tunnel → phone on LTE | ✅ <2s latency, 15 min stable, reconnect no-gap, holds on http2/TCP fallback |
| 2 | Crypto: fragment key, WebCrypto decrypt | ✅ tunnel sees ciphertext only; Safari + Chrome + Firefox |
| 3 | Firehose: 1,000 lines/s × 60s | ✅ flat memory (ring buffer), no SSE stall |

Competitive gap analysis (2026-08): none of sshx, wush, bitbang offer (a) QR onboarding
from the terminal, or (b) a verifiable record of what was streamed. Both are cheap on
top of poptail's existing architecture. These two features ARE the launch differentiator.

> 💡 Positioning line: "Run one command. Point your phone at the terminal.
> Watch the log live — and walk away with a signed receipt of every line you saw."

---

## Feature W1 — `--qr`: QR-in-terminal onboarding

### Behavior

```bash
poptail /var/log/app.log --qr
# prints:
#   https://<tunnel>.trycloudflare.com/s/ab12#k=<base64url-key>
#   [Unicode half-block QR of the SAME full URL, fragment included]
```

- QR encodes the complete URL **including the `#k=` fragment**. Scanning = full onboarding; nothing else to type.
- Render with Unicode half-blocks (`▀▄█ `) so one QR module = half a character cell. Fits a 40×80 terminal for a typical quick-tunnel URL at EC level L.
- Detect background: print both normal and `--qr-invert` hint, or auto-render dark-on-light (scanners require dark modules; light terminal themes break naive rendering — this is the known killer).
- `--no-qr` and plain-URL fallback always printed above the QR (copy/paste path never removed).

### Dependency decision (zero-dep invariant)

| Option | Pros | Cons | Verdict |
|--------|------|------|---------|
| `skip2/go-qrcode` dep | Proven | Breaks zero-dep invariant | ❌ |
| Vendor minimal QR encoder (~600 LOC, MIT, byte mode + EC-L only) | Keeps static binary, no go.mod entry | Maintenance of vendored code | ✅ chosen |
| Shell out to `qrencode` | Zero code | Runtime dep, not portable | ❌ |

> 💡 Vendored file lives at `internal/qr/qr.go` with `AIDEV-VENDORED:` header comment
> noting upstream + commit. Byte mode only; URL is ASCII so no kanji/numeric modes needed.

### Gate W1 (GO/NO-GO)

- [ ] iPhone camera + Android camera scan the QR from macOS Terminal.app AND a light-theme terminal (default font size, arm's length)
- [ ] Scanned URL opens viewer and decrypts — zero manual typing
- [ ] QR fits 80×24 terminal for the longest observed quick-tunnel URL; if not, fail loudly with the plain URL (never a corrupt QR)

---

## Feature W2 — Signed session receipts

### What it is

A tamper-evident export proving "these exact lines were streamed in this session, in this
order." Same pattern as popdrop's chunk-digest chain, applied to log lines. Target users:
incident reviews, support handoffs, compliance-ish "what did you see" disputes.

### Design

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
- Reconnect gaps (Gate 1 proved no-gap, but be honest): any missed line ranges recorded in `gaps[]`; receipt is then explicitly partial.

**Verify CLI:**

```bash
poptail verify receipt.json scrollback.txt
# → OK: 48,201 lines, chain head matches, signature valid, 0 gaps
# → FAIL: line 3,117 mismatch (tampered or wrong file)
```

> ⚠️ Honest claim only: receipt proves integrity + order of the streamed lines under the
> session pubkey. It does NOT prove the host machine's identity or that the log wasn't
> forged before poptail read it. Say so in README — over-claiming here burns trust.

### Gate W2 (GO/NO-GO)

- [ ] Receipt verifies after a forced viewer reconnect mid-stream (gaps handled or empty)
- [ ] Flipping one byte in the exported scrollback → verify FAILS at the correct line
- [ ] Chain adds no measurable lag at 1,000 lines/s (re-run Gate 3 with chain on)
- [ ] `poptail verify` runs offline, stdlib only

---

## Invariants (carry-over + new)

- **Never commit a private key** (cullroom invariant). Session Ed25519 keys are ephemeral, in-memory only. Any future persistent signing key: `gitignore` BEFORE keygen.
- Key stays in the URL fragment; server/tunnel never sees plaintext or key. QR must not leak the URL anywhere else (no logging of the full URL).
- Single static Go binary; viewer stays single-file vanilla JS, no build step.
- Zero external Go deps; QR encoder is vendored, not imported.
- Run `ship-check` skill before any public push or Lemon Squeezy link.

---

## File map deltas

| File | Change |
|------|--------|
| `internal/qr/qr.go` | NEW — vendored minimal QR encoder + half-block renderer |
| `main.go` | `--qr`, `--qr-invert`, `--no-qr` flags; print block |
| `internal/receipt/chain.go` | NEW — SHA-256 chain + Ed25519 sign (stdlib `crypto/ed25519`) |
| `cmd/verify.go` | NEW — `poptail verify` subcommand |
| `viewer.html` | chain mirror in JS (WebCrypto SHA-256), gap tracker, Export receipt button |
| `README.md` | wow demo GIF slot, receipt honesty paragraph, verify usage |

## Build order

| Phase | Deliverable | Done when |
|-------|-------------|-----------|
| 1 | W1 QR render | Gate W1 passes |
| 2 | W2 host chain + sign | unit tests: chain determinism, sig verify |
| 3 | W2 viewer mirror + export | Gate W2 passes end-to-end |
| 4 | Re-run Gates 1–3 with both features on | all original gates still hold |

## Non-goals (this pack)

- Write access / input of any kind — poptail stays read-only broadcast by design
- Multi-viewer presence, chat, cursors (sshx's territory)
- Persistent host identity keys (v2 premium candidate: "verified host" receipts)
- Animated-QR transport (crashbeam's lane, not poptail's)

## Open questions

- [ ] Receipt in free tier or Pro ($39)? Lean: chain+export free, `verify` free, "verified host identity" Pro later.
- [ ] `--receipt-interval` periodic checkpoint signatures in v1, or final-only? Lean final-only.

## Next steps

1. Vendor QR encoder, wire `--qr`, run Gate W1 on both phones + light theme.
2. Implement chain + sign + verify (Phase 2–3), run Gate W2.
3. Re-run Gates 1–3 with features enabled.
4. `ship-check`, then record the demo GIF: one command → phone scan → live log → receipt export.
