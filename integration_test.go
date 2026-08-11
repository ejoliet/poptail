package main

import (
	"bufio"
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestIntegrationLocal is the phase 2 gate: build the real binary, run it in
// -local mode against stdin, and verify the whole pipeline end to end —
// stdout contract, embedded viewer, ciphertext-only SSE, redaction, resume,
// and clean exit on SIGTERM.
func TestIntegrationLocal(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	if runtime.GOOS == "windows" {
		t.Skip("test drives the process with SIGTERM")
	}

	bin := filepath.Join(t.TempDir(), "poptail")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}

	cmd := exec.Command(bin, "-local", "-buffer", "50", "-")
	// -local must not require cloudflared at all.
	cmd.Env = append(os.Environ(), "PATH=/nonexistent", "POPTAIL_CLOUDFLARED=")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	// --- stdout contract (README: three parseable lines) ---
	sc := bufio.NewScanner(stdout)
	readLine := func() string {
		t.Helper()
		if !sc.Scan() {
			t.Fatalf("no more stdout (err: %v)", sc.Err())
		}
		return sc.Text()
	}
	if got := readLine(); got != "poptail: sharing stdin (redaction: on)" {
		t.Errorf("line 1 = %q", got)
	}
	linkLine := readLine()
	if !strings.HasPrefix(linkLine, "poptail: link: http://") {
		t.Fatalf("line 2 = %q", linkLine)
	}
	if got := readLine(); got != "poptail: Ctrl-C to kill the link" {
		t.Errorf("line 3 = %q", got)
	}

	rawURL := strings.TrimPrefix(linkLine, "poptail: link: ")
	base, frag, ok := strings.Cut(rawURL, "#k=")
	if !ok {
		t.Fatalf("no #k= fragment in %q", rawURL)
	}
	base = strings.TrimSuffix(base, "/")
	key, err := base64.RawURLEncoding.DecodeString(frag)
	if err != nil {
		t.Fatalf("fragment not raw base64url: %v", err)
	}
	enc, err := newEncryptorWithKey(key)
	if err != nil {
		t.Fatal(err)
	}

	// --- embedded viewer is served ---
	resp, err := http.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	page, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(page), "EventSource") {
		t.Error("viewer not served at /")
	}

	// --- feed lines, one carrying a secret that redaction must mask ---
	const secret = "AKIAIOSFODNN7EXAMPLE" // AWS documented example key, not real
	if _, err := io.WriteString(stdin, "hello world\ncreds "+secret+"\nthird line\n"); err != nil {
		t.Fatal(err)
	}

	streamResp := streamRequest(t, base, "", false)
	defer func() { _ = streamResp.Body.Close() }()
	events := readEvents(t, streamResp.Body, 3)

	var plain []string
	for _, e := range events {
		if strings.Contains(e.data, "hello world") || strings.Contains(e.data, secret) {
			t.Fatalf("PLAINTEXT ON THE WIRE at id %d: %q", e.id, e.data)
		}
		got, err := enc.open(e.id, e.data)
		if err != nil {
			t.Fatalf("decrypt id %d with fragment key: %v", e.id, err)
		}
		plain = append(plain, got)
	}
	if plain[0] != "hello world" {
		t.Errorf("line 1 decrypted to %q", plain[0])
	}
	if strings.Contains(plain[1], secret) {
		t.Errorf("redaction did not mask the AWS key: %q", plain[1])
	}
	if !strings.Contains(plain[1], redactedMark) {
		t.Errorf("no redaction mark in %q", plain[1])
	}

	// --- resume: Last-Event-ID replays from the next seq ---
	resumeResp := streamRequest(t, base, "1", false)
	defer func() { _ = resumeResp.Body.Close() }()
	resumed := readEvents(t, resumeResp.Body, 1)
	if resumed[0].id != 2 {
		t.Errorf("resumed at id %d, want 2", resumed[0].id)
	}

	// --- SIGTERM: clean exit 0, link dead ---
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()
	select {
	case err := <-waitErr:
		if err != nil {
			t.Errorf("exit: %v, want clean exit 0", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("did not exit within 5s of SIGTERM")
	}
	if _, err := http.Get(base + "/"); err == nil {
		t.Error("server still answering after shutdown; link must die")
	}
}

// TestIntegrationTunnelMode drives the binary in default (tunnel) mode with a
// fake cloudflared, verifying the wiring: binary resolved via
// POPTAIL_CLOUDFLARED, quick-tunnel URL parsed from stderr, link printed with
// the fragment, clean exit killing the child. The through-edge path needs
// real egress — that stays a manual gate.
func TestIntegrationTunnelMode(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake + SIGTERM")
	}
	bin := filepath.Join(t.TempDir(), "poptail")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}
	fake := fakeCloudflared(t,
		`echo "INF |  https://fake-integration.trycloudflare.com  |" >&2
sleep 30
`)

	cmd := exec.Command(bin, "-")
	cmd.Env = append(os.Environ(), "POPTAIL_CLOUDFLARED="+fake)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close() }()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	sc := bufio.NewScanner(stdout)
	var link string
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "poptail: link: ") {
			link = strings.TrimPrefix(sc.Text(), "poptail: link: ")
			break
		}
	}
	if !strings.HasPrefix(link, "https://fake-integration.trycloudflare.com/#k=") {
		t.Fatalf("link = %q, want fake tunnel URL with fragment", link)
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()
	select {
	case err := <-waitErr:
		if err != nil {
			t.Errorf("exit: %v, want clean exit 0", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("did not exit within 5s of SIGTERM")
	}
}

func TestVersionFlag(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	bin := filepath.Join(t.TempDir(), "poptail")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}
	out, err := exec.Command(bin, "-version").Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(out), "poptail ") {
		t.Errorf("-version printed %q", out)
	}
}
