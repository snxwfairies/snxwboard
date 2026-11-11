// Package redact strips secrets out of event bodies before they are stored.
package redact

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

const marker = "[redacted]"

// keyRe matches object keys whose values are secret no matter what they hold.
var keyRe = regexp.MustCompile(`(?i)password|passwd|secret|token|api[_-]?key|authorization|cookie|private[_-]?key|credential`)

// secretRes match well-known token formats wherever they appear in a string.
// Bearer goes first so "Bearer <token>" collapses to one marker.
var secretRes = []*regexp.Regexp{
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/-]{8,}={0,2}`),
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
	regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{36,}`),
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{50,}`),
	regexp.MustCompile(`sk-[A-Za-z0-9_-]{20,}`),
	regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{10,}`),
	regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`),
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?(?:-----END [A-Z ]*PRIVATE KEY-----|$)`),
}

// envRe matches KEY=value assignments anywhere: line starts, `export KEY=v`, and
// inline `KEY=v cmd`. Group 1 is the key, group 2 the value (quoted or bare).
var envRe = regexp.MustCompile(`(?:^|[\s;&|(])(?:export[ \t]+)?([A-Za-z_][A-Za-z0-9_]*)[ \t]*=[ \t]*("[^"\n]*"|'[^'\n]*'|[^\s;&|)]+)`)

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
			if keyRe.MatchString(k) && !isScalarNonString(val) {
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
	for _, re := range secretRes {
		out = re.ReplaceAllString(out, marker)
	}
	out = redactEnvLines(out)
	if out == s {
		return s, false
	}
	return out, true
}

// redactEnvLines rewrites only the value of assignments whose key looks secret,
// leaving the key readable.
func redactEnvLines(s string) string {
	matches := envRe.FindAllStringSubmatchIndex(s, -1)
	var b strings.Builder
	last := 0
	for _, m := range matches {
		if !keyRe.MatchString(s[m[2]:m[3]]) || s[m[4]:m[5]] == marker {
			continue
		}
		b.WriteString(s[last:m[4]])
		b.WriteString(marker)
		last = m[5]
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}
