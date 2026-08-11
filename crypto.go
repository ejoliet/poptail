package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strconv"
)

// encryptor seals log lines with AES-256-GCM before they reach the server's
// ring buffer, so plaintext never outlives the publish call.
//
// Wire format (proven in spike2 across Safari/Chrome/Firefox):
//
//	SSE data     = base64.StdEncoding(nonce[12] || ciphertext)
//	URL fragment = #k= + base64.RawURLEncoding(key[32])
//	AAD          = ASCII-decimal seq, matching JS String(e.lastEventId)
type encryptor struct {
	gcm cipher.AEAD
	key []byte
}

// newEncryptor creates an encryptor with a fresh random 256-bit key.
func newEncryptor() (*encryptor, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return newEncryptorWithKey(key)
}

func newEncryptorWithKey(key []byte) (*encryptor, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &encryptor{gcm: gcm, key: key}, nil
}

// keyFragment returns the URL fragment carrying the key, e.g. "#k=Qm9v...".
// AIDEV-NOTE: the fragment never leaves the browser in requests, so the key
// never reaches Cloudflare. Print it only as part of the share URL.
func (e *encryptor) keyFragment() string {
	return "#k=" + base64.RawURLEncoding.EncodeToString(e.key)
}

// seal encrypts one line with a fresh random nonce and seq as AAD.
// AIDEV-NOTE: seq as AAD → reorder/replay tampering fails decryption in viewer.
func (e *encryptor) seal(seq int, line string) (string, error) {
	nonce := make([]byte, e.gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return e.sealWithNonce(nonce, seq, line), nil
}

func (e *encryptor) sealWithNonce(nonce []byte, seq int, line string) string {
	ct := e.gcm.Seal(append([]byte{}, nonce...), nonce, []byte(line), aad(seq))
	return base64.StdEncoding.EncodeToString(ct)
}

// open is the inverse of seal. Production decrypt happens in the browser via
// WebCrypto; this exists for round-trip tests and stays behind the same
// wire-format rules.
func (e *encryptor) open(seq int, data string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return "", err
	}
	ns := e.gcm.NonceSize()
	if len(raw) < ns {
		return "", errors.New("ciphertext shorter than nonce")
	}
	pt, err := e.gcm.Open(nil, raw[:ns], raw[ns:], aad(seq))
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

func aad(seq int) []byte { return []byte(strconv.Itoa(seq)) }
