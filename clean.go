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

// redacted is s with its secrets replaced, for text stored outside events (SPEC P1).
func redacted(s string) string { r, _ := redact(s); return r }

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

// kindsIn is the secret kinds named in a comma-separated list, each once, in the order redact checks them.
func kindsIn(list string) string {
	named := map[string]bool{}
	for _, k := range strings.Split(list, ",") {
		named[k] = true
	}
	var out []string
	for _, p := range secretPatterns {
		if named[p.name] {
			out = append(out, p.name)
		}
	}
	return strings.Join(out, ",")
}

type anchorHit struct{ typ, value string }

var (
	pathRe = regexp.MustCompile(`(?:[A-Za-z]:[\\/])?(?:[\w.-]+[\\/])+[\w.-]+\.(?:md|go|mjs|js|ts|json|jsonl|py|sh|txt|yaml|yml|toml|db|ps1|html|csv)\b`)
	urlRe  = regexp.MustCompile(`https?://[^\s<>"'\x60)\]]+`)
)

// anchors extracts code-made tags: tools, paths, urls and the configured extra ones. Commit ids are text, not tags
// (SPEC G5).
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
	switch kind {
	case "thinking", "injected", "summary":
		return out // anchors come from what was said or done, not from reasoning or re-sent text
	}
	textAnchors(text, put)
	if (kind == "prompt" || kind == "steering") && pinRe.MatchString(text) {
		put("pin", "user")
	}
	return out
}

// textAnchors puts the paths, the configured extra anchors and the urls a text names.
func textAnchors(text string, put func(typ, value string)) {
	slashed := strings.ReplaceAll(text, `\`, "/")
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
}
