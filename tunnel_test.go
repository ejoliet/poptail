package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// fakeCloudflared writes an executable script that mimics cloudflared's
// stderr banner (URL on stderr, then stays alive like the real thing).
func fakeCloudflared(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake")
	}
	path := filepath.Join(t.TempDir(), "cloudflared")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStartTunnelParsesURL(t *testing.T) {
	bin := fakeCloudflared(t,
		`echo "INF ready" >&2
echo "INF |  https://fake-words-here.trycloudflare.com  |" >&2
sleep 30
`)
	cmd, url, err := startTunnel(bin, 12345, "auto")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	if url != "https://fake-words-here.trycloudflare.com" {
		t.Errorf("parsed %q", url)
	}
}

func TestStartTunnelPassesProtocolFlag(t *testing.T) {
	// The fake echoes its args back inside the URL-bearing line so the test
	// can assert what cloudflared was invoked with.
	bin := fakeCloudflared(t,
		`echo "args:$* https://args-echo.trycloudflare.com" >&2
sleep 30
`)
	cmd, _, err := startTunnel(bin, 12345, "http2")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	// Re-read what the script received via its own process args.
	if got := cmd.Args; len(got) != 6 || got[4] != "--protocol" || got[5] != "http2" {
		t.Errorf("cloudflared args = %v, want ... --protocol http2", got)
	}

	cmdAuto, _, err := startTunnel(bin, 12345, "auto")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmdAuto.Process.Kill(); _ = cmdAuto.Wait() }()
	if got := cmdAuto.Args; len(got) != 4 {
		t.Errorf("auto must not pass --protocol, got %v", got)
	}
}

func TestStartTunnelTimeoutKillsChild(t *testing.T) {
	bin := fakeCloudflared(t,
		`echo "INF no url ever" >&2
sleep 30
`)
	old := tunnelURLTimeout
	tunnelURLTimeout = 300 * time.Millisecond
	defer func() { tunnelURLTimeout = old }()

	start := time.Now()
	_, _, err := startTunnel(bin, 12345, "auto")
	if err == nil {
		t.Fatal("want timeout error")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("timeout took %s", d)
	}
	// startTunnel Waits after Kill, so no zombie child remains on success of
	// this path; nothing further to assert without ps-scraping.
}

func TestFindCloudflaredEnvOverride(t *testing.T) {
	bin := fakeCloudflared(t, "exit 0\n")
	t.Setenv("POPTAIL_CLOUDFLARED", bin)
	got, err := findCloudflared()
	if err != nil {
		t.Fatal(err)
	}
	if got != bin {
		t.Errorf("got %q want %q", got, bin)
	}

	t.Setenv("POPTAIL_CLOUDFLARED", filepath.Join(t.TempDir(), "missing"))
	if _, err := findCloudflared(); err == nil {
		t.Error("bogus POPTAIL_CLOUDFLARED must error, not fall through to PATH")
	}
}
