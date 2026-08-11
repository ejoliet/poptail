package main

import (
	"encoding/base64"
	"strings"
	"testing"
)

func testKey() []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	return key
}

func TestSealOpenRoundTrip(t *testing.T) {
	e, err := newEncryptor()
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		"plain build output",
		"",
		"unicode ✓ éàü 日本語",
		strings.Repeat("x", 100_000),
	} {
		sealed, err := e.seal(7, line)
		if err != nil {
			t.Fatal(err)
		}
		got, err := e.open(7, sealed)
		if err != nil {
			t.Fatalf("open(%.20q): %v", line, err)
		}
		if got != line {
			t.Errorf("round trip mismatch for %.20q", line)
		}
	}
}

func TestWrongSeqAADFailsDecrypt(t *testing.T) {
	e, err := newEncryptor()
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := e.seal(7, "secret line")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.open(8, sealed); err == nil {
		t.Error("decrypt with wrong seq AAD must fail (replay/reorder detection)")
	}
}

func TestTamperedCiphertextFailsDecrypt(t *testing.T) {
	e, err := newEncryptor()
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := e.seal(1, "secret line")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.StdEncoding.DecodeString(sealed)
	raw[len(raw)-1] ^= 0x01
	if _, err := e.open(1, base64.StdEncoding.EncodeToString(raw)); err == nil {
		t.Error("tampered ciphertext must fail decryption")
	}
}

func TestNoncesAreFresh(t *testing.T) {
	e, err := newEncryptor()
	if err != nil {
		t.Fatal(err)
	}
	a, _ := e.seal(1, "same line")
	b, _ := e.seal(1, "same line")
	if a == b {
		t.Error("two seals of the same line must differ (fresh nonce each)")
	}
}

func TestKeyFragmentFormat(t *testing.T) {
	e, err := newEncryptorWithKey(testKey())
	if err != nil {
		t.Fatal(err)
	}
	frag := e.keyFragment()
	if !strings.HasPrefix(frag, "#k=") {
		t.Fatalf("fragment %q missing #k= prefix", frag)
	}
	// base64url raw (no padding), matching the viewer's b64u() decoder.
	key, err := base64.RawURLEncoding.DecodeString(frag[3:])
	if err != nil {
		t.Fatalf("fragment not raw base64url: %v", err)
	}
	if len(key) != 32 {
		t.Fatalf("key length %d, want 32 (AES-256)", len(key))
	}
}

// TestWireFormatGolden pins the exact wire format the embedded viewer's
// WebCrypto decrypt expects: base64std(nonce[12]||ct), AAD = ASCII-decimal
// seq. Golden generated from this stdlib implementation; cross-implementation
// decrypt was verified live in spike2 (Safari/Chrome/Firefox) and is
// re-verified by the phase 2 integration test.
func TestWireFormatGolden(t *testing.T) {
	e, err := newEncryptorWithKey(testKey())
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, 12)
	for i := range nonce {
		nonce[i] = byte(i)
	}
	const golden = "AAECAwQFBgcICQoLL2e6d6rFsnT9Nfbi3ac3gt094V+uY+nYH3ydLyc="
	if got := e.sealWithNonce(nonce, 42, "hello poptail"); got != golden {
		t.Errorf("wire format drifted:\n got %s\nwant %s", got, golden)
	}
}
