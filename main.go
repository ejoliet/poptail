// Command poptail shares a live tail -f as a temporary, end-to-end-encrypted
// URL. See README.md for the full spec.
package main

import (
	"fmt"
	"os"
)

// AIDEV-NOTE: phase 1 ships the pipeline pieces (tailer, redactor, encryptor)
// with unit tests only. Flag parsing, server and tunnel wiring land in
// phases 2-3 (README Build Order).
func main() {
	fmt.Fprintln(os.Stderr, "poptail: server not wired yet (build phase 1 of 4); see README Build Order")
	os.Exit(2)
}
