// Package redact strips secrets out of event bodies before they are stored.
package redact

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

const marker = "[redacted]"

// Secret keys are matched on whole words, so `GH_TOKEN`, `x-api-key` and
// `accessToken` are secret while `max_tokens` and `tokenizer` are not.
var (
	secretWords = map[string]bool{
		"password": true, "passwd": true, "pwd": true, "passphrase": true,
		"secret": true, "secrets": true, "token": true, "cookie": true, "cookies": true,
		"authorization": true, "credential": true, "credentials": true, "apikey": true,
	}
	// secretPairs are two adjacent words that together name a secret.
	secretPairs = map[[2]string]bool{
		{"api", "key"}: true, {"private", "key"}: true, {"access", "key"}: true,
		{"secret", "key"}: true, {"signing", "key"}: true, {"encryption", "key"}: true,
	}
	camelRe = regexp.MustCompile(`([a-z0-9])([A-Z])`)
	splitRe = regexp.MustCompile(`[^a-z0-9]+`)
)

func isSecretKey(k string) bool {
	words := splitRe.Split(strings.ToLower(camelRe.ReplaceAllString(k, "${1}_${2}")), -1)
	for i, w := range words {
		if secretWords[w] {
			return true
		}
		if i > 0 && secretPairs[[2]string{words[i-1], w}] {
			return true
		}
	}
	return false
}

// tokenRule rewrites a well-known token format wherever it appears. Rules with
// a leading boundary group keep it via ${1}.
type tokenRule struct {
	re   *regexp.Regexp
	repl string
}

// b is a left boundary, so "task-management-service" is not an "sk-" key.
const b = `(^|[^A-Za-z0-9_-])`

// tokenRules go first-to-last; Bearer first so "Bearer <token>" collapses to
// one marker.
var tokenRules = []tokenRule{
	{regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/-]{8,}={0,2}`), marker},
	{regexp.MustCompile(`AKIA[0-9A-Z]{16}`), marker},
	{regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{36,}`), marker},
	{regexp.MustCompile(`github_pat_[A-Za-z0-9_]{50,}`), marker},
	{regexp.MustCompile(b + `sk-[A-Za-z0-9_-]{20,}`), "${1}" + marker},
	{regexp.MustCompile(b + `[sr]k_(?:live|test)_[A-Za-z0-9]{10,}`), "${1}" + marker},
	{regexp.MustCompile(`AIza[0-9A-Za-z_-]{35}`), marker},
	{regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{10,}`), marker},
	{regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`), marker},
	{regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?(?:-----END [A-Z ]*PRIVATE KEY-----|$)`), marker},
	// user:password@ in any URL: keep the user, drop the password.
	{regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://[^/\s:@]+:)[^@\s/]+@`), "${1}" + marker + "@"},
}

// assignRules match "key <sep> value" forms. Group 1 is the key, group 2 the
// value; only the value of a secret-looking key is replaced.
var assignRules = []*regexp.Regexp{
	// KEY=value: line starts, `export KEY=v`, inline `KEY=v cmd`.
	regexp.MustCompile(`(?:^|[\s;&|(])(?:export[ \t]+)?([A-Za-z_][A-Za-z0-9_]*)[ \t]*=[ \t]*("[^"\n]*"|'[^'\n]*'|[^\s;&|)]+)`),
	// --password=x, --api-key x
	regexp.MustCompile(`(?:^|\s)--?([A-Za-z][A-Za-z0-9_-]*)(?:=|[ \t]+)("[^"\n]*"|'[^'\n]*'|[^\s;&|)]+)`),
	// key: value lines (YAML, HTTP headers).
	regexp.MustCompile(`(?m)^[ \t-]*([A-Za-z_][A-Za-z0-9_.-]*)[ \t]*:[ \t]+([^\s#][^\n]*?)[ \t]*$`),
	// "key": "value" inside a string, e.g. JSON in a curl command.
	regexp.MustCompile(`"([A-Za-z_][A-Za-z0-9_.-]*)"[ \t]*:[ \t]*("[^"\n]*")`),
}

// Redact returns raw with secret values replaced by "[redacted]". It keeps the
// original bytes when nothing changed, and returns raw untouched when it is not
// valid JSON.
func Redact(raw json.RawMessage) json.RawMessage {
	data := bytes.TrimSpace(raw)
	if len(data) == 0 || !json.Valid(data) {
		return raw
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return raw
	}
	out, changed := walk(v)
	if !changed {
		return raw
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // keep `&&`, `<`, `>` in shell commands readable
	if err := enc.Encode(out); err != nil {
		return raw
	}
	return bytes.TrimRight(buf.Bytes(), "\n")
}

func isScalarNonString(v any) bool {
	switch v.(type) {
	case json.Number, bool, nil:
		return true
	}
	return false
}

func walk(v any) (any, bool) {
	switch t := v.(type) {
	case map[string]any:
		changed := false
		for k, val := range t {
			// Numbers and booleans are never secrets, so counts such as
			// input_tokens stay; any string, object or array under a
			// secret-looking key is replaced whole.
			if isSecretKey(k) && !isScalarNonString(val) {
				if val != marker {
					t[k] = marker
					changed = true
				}
				continue
			}
			if nv, ok := walk(val); ok {
				t[k] = nv
				changed = true
			}
		}
		return t, changed
	case []any:
		changed := false
		for i := range t {
			if nv, ok := walk(t[i]); ok {
				t[i] = nv
				changed = true
			}
		}
		return t, changed
	case string:
		return redactString(t)
	default:
		return v, false
	}
}

func redactString(s string) (any, bool) {
	out := s
	for _, r := range tokenRules {
		out = r.re.ReplaceAllString(out, r.repl)
	}
	for _, re := range assignRules {
		out = redactAssignments(re, out)
	}
	if out == s {
		return s, false
	}
	return out, true
}

// redactAssignments rewrites only the value of matches whose key looks
// secret, leaving the key readable. A quoted value keeps its quotes.
func redactAssignments(re *regexp.Regexp, s string) string {
	matches := re.FindAllStringSubmatchIndex(s, -1)
	var sb strings.Builder
	last := 0
	for _, m := range matches {
		key, val := s[m[2]:m[3]], s[m[4]:m[5]]
		if !isSecretKey(key) || strings.Trim(val, `"'`) == marker {
			continue
		}
		start, end := m[4], m[5]
		if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') && val[len(val)-1] == val[0] {
			start, end = start+1, end-1
		}
		sb.WriteString(s[last:start])
		sb.WriteString(marker)
		last = end
	}
	if last == 0 {
		return s
	}
	sb.WriteString(s[last:])
	return sb.String()
}
