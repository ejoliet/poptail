package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"time"
)

// var, not const: tests shorten it to exercise the timeout path.
var tunnelURLTimeout = 30 * time.Second

var tunnelURLRe = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)

// findCloudflared resolves the cloudflared binary: POPTAIL_CLOUDFLARED env
// var first, then PATH.
//
// AIDEV-NOTE: DEVIATION from README tunnel.go step 3 — no auto-download yet.
// Open Question 1 (trusted checksum source for cloudflared releases) is still
// unresolved, and downloading + executing an unverified binary is the wrong
// conservative default. Until it is resolved, a missing binary gets a clear
// install hint instead.
func findCloudflared() (string, error) {
	if p := os.Getenv("POPTAIL_CLOUDFLARED"); p != "" {
		if _, err := exec.LookPath(p); err != nil {
			return "", fmt.Errorf("POPTAIL_CLOUDFLARED=%s: %w", p, err)
		}
		return p, nil
	}
	p, err := exec.LookPath("cloudflared")
	if err != nil {
		return "", errors.New("cloudflared not found on PATH (install: brew install cloudflared, " +
			"or https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/, " +
			"or set POPTAIL_CLOUDFLARED)")
	}
	return p, nil
}

// startTunnel spawns a cloudflared quick tunnel to the local port and returns
// the running process plus the public https URL parsed from its stderr.
// protocol is auto|quic|http2; the caller kills the returned process on
// shutdown (acceptance criterion: no orphan cloudflared).
func startTunnel(bin string, port int, protocol string) (*exec.Cmd, string, error) {
	args := []string{"tunnel", "--url", fmt.Sprintf("http://127.0.0.1:%d", port)}
	if protocol != "" && protocol != "auto" {
		// AIDEV-NOTE: --protocol is a hidden-but-valid cloudflared flag (absent
		// from tunnel --help). http2 is the escape hatch when egress UDP 7844
		// is blocked. Do NOT confuse with --http2-origin (origin-side HTTP/2,
		// which our plain net/http server rejects). Proven in spike 1.
		args = append(args, "--protocol", protocol)
	}
	cmd := exec.Command(bin, args...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, "", err
	}
	if err := cmd.Start(); err != nil {
		return nil, "", err
	}
	urlCh := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			if m := tunnelURLRe.FindString(sc.Text()); m != "" {
				select {
				case urlCh <- m:
				default:
				}
			}
			// keep draining so cloudflared never blocks on a full stderr pipe
		}
	}()
	select {
	case u := <-urlCh:
		return cmd, u, nil
	case <-time.After(tunnelURLTimeout):
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, "", fmt.Errorf("no trycloudflare URL within %s", tunnelURLTimeout)
	}
}
