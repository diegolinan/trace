package main

import (
	"regexp"
	"strings"
)

// The one place secrets are removed before anything is stored (a single redaction choke point).
// The raw copy is not redacted: it is a byte copy of a transcript that already sits on this disk.
var secretPatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{"private-key", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`)},
	{"openrouter-key", regexp.MustCompile(`sk-or-v1-[A-Za-z0-9]{20,}`)},
	{"sk-key", regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`)},
	{"github-token", regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{30,}`)},
	{"aws-key", regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)},
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`)},
	{"bearer", regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/-]{20,}=*`)},
	{"assigned-secret", regexp.MustCompile(`(?i)\b[A-Z0-9_]*(?:API_KEY|APIKEY|TOKEN|SECRET|PASSWORD)\s*[:=]\s*["']?[A-Za-z0-9._~+/-]{16,}`)},
}

// redact replaces secrets with a marker and says which kinds it removed ("" when none).
func redact(s string) (string, string) {
	var hit []string
	for _, p := range secretPatterns {
		if p.re.MatchString(s) {
			s = p.re.ReplaceAllString(s, "[REDACTED:"+p.name+"]")
			hit = append(hit, p.name)
		}
	}
	return s, strings.Join(hit, ",")
}

type anchorHit struct{ typ, value string }

var (
	shaRe  = regexp.MustCompile(`\b[0-9a-f]{7,40}\b`)
	pathRe = regexp.MustCompile(`(?:[A-Za-z]:[\\/])?(?:[\w.-]+[\\/])+[\w.-]+\.(?:md|go|mjs|js|ts|json|jsonl|py|sh|txt|yaml|yml|toml|db|ps1|html|csv)\b`)
	urlRe  = regexp.MustCompile(`https?://[^\s<>"'\x60)\]]+`)
)

// anchors extracts code-made tags. Commit candidates come back as type "sha?" and are kept only if git knows them.
func anchors(text, kind, toolName string) []anchorHit {
	var out []anchorHit
	seen := map[string]bool{}
	put := func(t, v string) {
		if !seen[t+v] {
			seen[t+v] = true
			out = append(out, anchorHit{t, v})
		}
	}
	if toolName != "" {
		put("tool", toolName)
	}
	if kind == "thinking" || kind == "injected" || kind == "summary" {
		return out // anchors come from what was said or done, not from reasoning or re-sent text
	}
	for _, m := range shaRe.FindAllString(text, 200) {
		if strings.ContainsAny(m, "0123456789") && strings.ContainsAny(m, "abcdef") {
			put("sha?", m)
		}
	}
	// The repo prefix can hold a space, so strip it before matching paths.
	slashed := strings.ReplaceAll(text, `\`, "/")
	if repoRe != nil {
		slashed = repoRe.ReplaceAllString(slashed, "")
	}
	for _, m := range pathRe.FindAllString(slashed, 200) {
		put("path", m)
	}
	for _, a := range extraAnchors {
		for _, m := range a.re.FindAllString(text, 50) {
			put(a.name, m)
		}
	}
	for _, m := range urlRe.FindAllString(text, 50) {
		put("url", strings.TrimRight(m, ".,;:"))
	}
	if (kind == "prompt" || kind == "steering") && pinRe.MatchString(text) {
		put("pin", "user")
	}
	return out
}
