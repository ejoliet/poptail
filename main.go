// Command poptail shares a live tail -f as a temporary, end-to-end-encrypted
// URL. See README.md for the full spec.
package main

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

// version is overridden at release time via -ldflags "-X main.version=...".
var version = "dev"

// repeatable collects a flag given more than once (-redact).
type repeatable []string

func (r *repeatable) String() string     { return strings.Join(*r, ",") }
func (r *repeatable) Set(v string) error { *r = append(*r, v); return nil }

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("poptail", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: poptail [flags] <file>\n       <command> | poptail [flags] -")
		fs.PrintDefaults()
	}
	var (
		nLines    = fs.Int("n", 50, "initial lines to show")
		bufSize   = fs.Int("buffer", 2000, "ring buffer size for late joiners")
		noRedact  = fs.Bool("no-redact", false, "disable secret masking")
		local     = fs.Bool("local", false, "skip tunnel; serve on LAN IP only")
		expire    = fs.Duration("expire", 0, "self-terminate after duration, e.g. 2h")
		protocol  = fs.String("protocol", "auto", "cloudflared edge protocol: auto|quic|http2")
		qr        = fs.Bool("qr", false, "print QR code of share URL in terminal")
		showVer   = fs.Bool("version", false, "print version and exit")
		redactPat repeatable
	)
	fs.Var(&redactPat, "redact", "extra redaction regex (repeatable)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVer {
		fmt.Fprintln(stdout, "poptail", version)
		return 0
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 1
	}
	src := fs.Arg(0)

	// Register signal handling before anything slow (tunnel spawn can take
	// seconds): a Ctrl-C during startup must still shut down cleanly instead
	// of killing the process mid-wiring.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	switch *protocol {
	case "auto", "quic", "http2":
	default:
		fmt.Fprintln(stderr, "poptail: -protocol must be auto, quic or http2")
		return 2
	}

	// Resolve cloudflared before touching the source: fail fast with the
	// install hint instead of after the tailer is already attached.
	var cfBin string
	if !*local {
		var err error
		if cfBin, err = findCloudflared(); err != nil {
			fmt.Fprintln(stderr, "poptail:", err)
			fmt.Fprintln(stderr, "poptail: or run with -local to serve on the LAN without a tunnel")
			return 1
		}
	}

	var red *redactor
	if !*noRedact {
		var err error
		if red, err = newRedactor(redactPat...); err != nil {
			fmt.Fprintln(stderr, "poptail: bad -redact pattern:", err)
			return 1
		}
	} else if len(redactPat) > 0 {
		fmt.Fprintln(stderr, "poptail: -redact has no effect with -no-redact")
		return 1
	}

	enc, err := newEncryptor()
	if err != nil {
		fmt.Fprintln(stderr, "poptail:", err)
		return 1
	}
	h := newHub(enc, *bufSize)

	lines, stopSource, err := openSource(src, *nLines)
	if err != nil {
		fmt.Fprintln(stderr, "poptail:", err)
		return 1
	}
	defer stopSource()

	// Tunnel mode binds loopback only (cloudflared connects locally; nothing
	// is exposed to the LAN). -local binds the LAN IP instead.
	bindLAN := func() (net.Listener, string, error) {
		host, err := lanIP()
		if err != nil {
			fmt.Fprintln(stderr, "poptail: no LAN address found, serving on loopback only:", err)
			host = "127.0.0.1"
		}
		ln, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
		if err != nil {
			return nil, "", err
		}
		return ln, "http://" + ln.Addr().String() + "/", nil
	}

	var ln net.Listener
	var shareURL string // printed with the key fragment appended
	var tun *exec.Cmd
	if *local {
		var err error
		if ln, shareURL, err = bindLAN(); err != nil {
			fmt.Fprintln(stderr, "poptail:", err)
			return 1
		}
	} else {
		var err error
		if ln, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			fmt.Fprintln(stderr, "poptail:", err)
			return 1
		}
		port := ln.Addr().(*net.TCPAddr).Port
		var tunnelURL string
		if tun, tunnelURL, err = startTunnel(cfBin, port, *protocol); err != nil {
			// README tunnel contract: on spawn/parse failure print the error
			// and fall back to a LAN URL; exit non-zero only if that is also
			// impossible.
			fmt.Fprintln(stderr, "poptail: tunnel failed:", err)
			fmt.Fprintln(stderr, "poptail: falling back to LAN-only (as if -local)")
			_ = ln.Close()
			if ln, shareURL, err = bindLAN(); err != nil {
				fmt.Fprintln(stderr, "poptail:", err)
				return 1
			}
		} else {
			shareURL = tunnelURL + "/"
		}
	}
	defer func() {
		if tun != nil && tun.Process != nil {
			// AIDEV-NOTE: no orphan cloudflared (acceptance criterion).
			_ = tun.Process.Kill()
			_ = tun.Wait()
		}
	}()

	srv := &http.Server{Handler: newMux(h), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()

	done := make(chan struct{})
	var once bool
	stop := func() {
		if !once {
			once = true
			close(done)
		}
	}

	// Pipeline: source -> redact -> seal -> ring buffer.
	go func() {
		for line := range lines {
			if red != nil {
				line = red.apply(line)
			}
			if err := h.publish(line); err != nil {
				// AIDEV-NOTE: encryption failure must never fall through to
				// publishing plaintext — kill the link instead.
				fmt.Fprintln(stderr, "poptail: encrypt failed, stopping:", err)
				stop()
				return
			}
		}
		// Source ended (pipe closed / file gone): keep serving so viewers can
		// still read the buffer until Ctrl-C.
	}()

	name := src
	if src == "-" {
		name = "stdin"
	}
	fmt.Fprintf(stdout, "poptail: sharing %s (redaction: %s)\n", name, onOff(red != nil))
	// AIDEV-NOTE: the key is printed only as part of the share URL, never logged
	// separately, and never reaches the server side of any request.
	link := shareURL + enc.keyFragment()
	fmt.Fprintf(stdout, "poptail: link: %s\n", link)
	fmt.Fprintln(stdout, "poptail: Ctrl-C to kill the link")
	if *qr {
		if code, err := qrcode.New(link, qrcode.Low); err != nil {
			fmt.Fprintln(stderr, "poptail: qr:", err)
		} else {
			fmt.Fprint(stdout, code.ToSmallString(false))
		}
	}

	if *expire > 0 {
		t := time.AfterFunc(*expire, func() {
			fmt.Fprintln(stderr, "poptail: expired after", *expire)
			stop()
		})
		defer t.Stop()
	}

	select {
	case <-sig:
	case <-done:
	}
	// Close, not Shutdown: SSE handlers never return on their own, and killing
	// the link is exactly the intended semantic.
	_ = srv.Close()
	return 0
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// lanIP returns the first IPv4 address of an up, non-loopback interface.
// AIDEV-NOTE: on a machine with a VPN this can pick the tunnel interface;
// that is still a real address the URL is reachable on.
func lanIP() (string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			if ip4 := ipn.IP.To4(); ip4 != nil && !ip4.IsLinkLocalUnicast() {
				return ip4.String(), nil
			}
		}
	}
	return "", errors.New("no non-loopback IPv4 interface")
}
