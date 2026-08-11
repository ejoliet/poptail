package main

import (
	"strings"
	"testing"
)

// Fixtures use AWS's documented example credentials — not real secrets.

func mustRedactor(t *testing.T, extra ...string) *redactor {
	t.Helper()
	r, err := newRedactor(extra...)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestDefaultRules(t *testing.T) {
	r := mustRedactor(t)
	cases := []struct {
		name, in string
		leaked   string // substring that must NOT survive
	}{
		{"aws access key id", "creds: AKIAIOSFODNN7EXAMPLE region us-east-1", "AKIAIOSFODNN7EXAMPLE"},
		{"aws sts key id", "using ASIAIOSFODNN7EXAMPLE now", "ASIAIOSFODNN7EXAMPLE"},
		{"aws secret assignment", `AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY`, "wJalrXUtnFEMI"},
		{"secret yaml", `secret_key: "0123456789abcdefASDF"`, "0123456789abcdefASDF"},
		{"bearer", "Authorization: Bearer abc.def-123_tok", "abc.def-123_tok"},
		{"jwt bare", "token=eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2lnbmF0dXJl found", "c2lnbmF0dXJl"},
	}
	for _, c := range cases {
		got := r.apply(c.in)
		if strings.Contains(got, c.leaked) {
			t.Errorf("%s: secret survived: %q", c.name, got)
		}
		if !strings.Contains(got, redactedMark) {
			t.Errorf("%s: no redaction mark in %q", c.name, got)
		}
	}
}

func TestBenignLinesUntouched(t *testing.T) {
	r := mustRedactor(t)
	for _, line := range []string{
		"2026-08-11 build step 3/9 compiling",
		// 40-char git SHA shares the AWS-secret alphabet; must survive.
		"commit 3b18e512dba79e4c8300dd08aeb37f8e728b8dad (HEAD -> main)",
		"GET /stream 200 12ms",
		"",
	} {
		if got := r.apply(line); got != line {
			t.Errorf("benign line altered:\n in  %q\n out %q", line, got)
		}
	}
}

func TestPEMBlockMasked(t *testing.T) {
	r := mustRedactor(t)
	lines := []string{
		"before",
		"-----BEGIN RSA PRIVATE KEY-----",
		"bm90IGEgcmVhbCBrZXkgYm9keQ==",
		"-----END RSA PRIVATE KEY-----",
		"after",
	}
	want := []string{"before", redactedMark, redactedMark, redactedMark, "after"}
	for i, in := range lines {
		if got := r.apply(in); got != want[i] {
			t.Errorf("line %d: got %q want %q", i, got, want[i])
		}
	}
	if r.inPEM {
		t.Error("redactor stuck in PEM state after END marker")
	}
}

func TestExtraPattern(t *testing.T) {
	r := mustRedactor(t, `card=\d{16}`)
	got := r.apply("charge card=4111111111111111 ok")
	if strings.Contains(got, "4111111111111111") {
		t.Errorf("extra pattern not applied: %q", got)
	}
}

func TestBadExtraPatternErrors(t *testing.T) {
	if _, err := newRedactor(`(unclosed`); err == nil {
		t.Error("invalid extra regex must error, not silently drop")
	}
}
