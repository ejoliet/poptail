package main

import "regexp"

// redactedMark replaces anything a redaction rule matches.
const redactedMark = "[REDACTED]"

// redactor masks secrets in log lines before encryption. Default ON
// (security invariant: assume logs contain secrets).
//
// AIDEV-NOTE: apply is stateful (multi-line PEM blocks) and therefore NOT
// goroutine-safe. One redactor per source, called from the single tailer
// pipeline goroutine.
type redactor struct {
	rules []rule
	inPEM bool
}

type rule struct {
	re   *regexp.Regexp
	repl string
}

var (
	pemBegin = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`)
	pemEnd   = regexp.MustCompile(`-----END [A-Z0-9 ]*PRIVATE KEY-----`)
)

// AIDEV-NOTE: rule order matters — JWT before Bearer so a "Bearer eyJ..."
// token masks as one unit either way. The AWS secret rule is contextual
// (requires a secret-ish prefix) so 40-char git SHAs in build logs survive.
var defaultRules = []rule{
	// AWS access key ID
	{regexp.MustCompile(`\b(?:AKIA|ASIA|ABIA|ACCA)[A-Z0-9]{16}\b`), redactedMark},
	// secret-ish assignment: SECRET_KEY=..., aws_secret_access_key: "..."
	// (no leading \b: underscore is a word char, so AWS_SECRET has no boundary)
	{regexp.MustCompile(`(?i)(secret[\w-]*\s*[:=]\s*["']?)[A-Za-z0-9/+=]{16,}`), "${1}" + redactedMark},
	// JWT (three dot-separated base64url parts starting eyJ)
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b`), redactedMark},
	// Bearer <token>
	{regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9\-._~+/]+=*`), "Bearer " + redactedMark},
}

// newRedactor builds a redactor from the default rules plus optional extra
// user-supplied patterns (-redact flag); extras mask their whole match.
func newRedactor(extra ...string) (*redactor, error) {
	rules := make([]rule, 0, len(defaultRules)+len(extra))
	rules = append(rules, defaultRules...)
	for _, p := range extra {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, err
		}
		rules = append(rules, rule{re, redactedMark})
	}
	return &redactor{rules: rules}, nil
}

// apply masks secrets in one line. Whole lines inside a PEM private-key block
// (BEGIN through END inclusive) are replaced entirely.
func (r *redactor) apply(line string) string {
	if r.inPEM {
		if pemEnd.MatchString(line) {
			r.inPEM = false
		}
		return redactedMark
	}
	if pemBegin.MatchString(line) {
		if !pemEnd.MatchString(line) { // single-line block: don't latch state
			r.inPEM = true
		}
		return redactedMark
	}
	for _, ru := range r.rules {
		line = ru.re.ReplaceAllString(line, ru.repl)
	}
	return line
}
