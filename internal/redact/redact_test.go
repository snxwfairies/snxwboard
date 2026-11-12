package redact

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"password key", `{"password":"hunter2"}`, `{"password":"[redacted]"}`},
		{"upper case key", `{"PASSWORD":"x"}`, `{"PASSWORD":"[redacted]"}`},
		{"api key variants", `{"api_key":"a","X-Api-Key":"b","apikey":"c"}`, `{"X-Api-Key":"[redacted]","api_key":"[redacted]","apikey":"[redacted]"}`},
		{"nested object", `{"a":{"token":"t","keep":1}}`, `{"a":{"keep":1,"token":"[redacted]"}}`},
		{"nested array", `{"list":[{"secret":"s"},{"ok":"v"}]}`, `{"list":[{"secret":"[redacted]"},{"ok":"v"}]}`},
		{"key holding object", `{"credentials":{"user":"u"}}`, `{"credentials":"[redacted]"}`},
		{"aws key in string", `{"log":"use AKIAIOSFODNN7EXAMPLE now"}`, `{"log":"use [redacted] now"}`},
		{"github token", `{"out":"ghp_` + strings.Repeat("a", 36) + `"}`, `{"out":"[redacted]"}`},
		{"github pat", `{"out":"github_pat_` + strings.Repeat("b", 50) + `"}`, `{"out":"[redacted]"}`},
		{"openai key", `{"out":"sk-` + strings.Repeat("c", 25) + `"}`, `{"out":"[redacted]"}`},
		{"slack token", `{"out":"xoxb-1234567890-abc"}`, `{"out":"[redacted]"}`},
		{"jwt", `{"out":"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dGhpcy1pcy1hLXNpZ25hdHVyZQ"}`, `{"out":"[redacted]"}`},
		{"pem header", `{"pem":"-----BEGIN RSA PRIVATE KEY-----\nabc"}`, `{"pem":"[redacted]"}`},
		{"bearer", `{"out":"Authorization: Bearer abc123def456"}`, `{"out":"Authorization: [redacted]"}`},
		{"env text", `{"env":"DATABASE_PASSWORD=hunter2\nPORT=80"}`, `{"env":"DATABASE_PASSWORD=[redacted]\nPORT=80"}`},
		{"env text with spaces", `{"env":"  API_KEY = abc\nX=t"}`, `{"env":"  API_KEY = [redacted]\nX=t"}`},
		{"non object body", `"token AKIAIOSFODNN7EXAMPLE"`, `"token [redacted]"`},
		{"array body", `["ghp_` + strings.Repeat("d", 36) + `"]`, `["[redacted]"]`},
		{"number untouched", `{"n":12345678901234567890,"ok":true}`, `{"n":12345678901234567890,"ok":true}`},
		{"null body", `null`, `null`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(Redact(json.RawMessage(tc.in)))
			if got != tc.want {
				t.Fatalf("Redact(%s) = %s, want %s", tc.in, got, tc.want)
			}
			if !json.Valid([]byte(got)) {
				t.Fatalf("output is not valid JSON: %s", got)
			}
		})
	}
}

func TestRedactInvalidJSONUntouched(t *testing.T) {
	in := json.RawMessage("not json")
	if got := Redact(in); string(got) != string(in) {
		t.Fatalf("Redact(%s) = %s, want identical bytes", in, got)
	}
}

func TestRedactKeepsUnchangedBytes(t *testing.T) {
	in := json.RawMessage(`{ "a" : 1 }`)
	if got := Redact(in); string(got) != string(in) {
		t.Fatalf("Redact(%s) = %s, want identical bytes", in, got)
	}
}

func TestRedactLongerString(t *testing.T) {
	in := `{"cmd":"curl -H 'Authorization: Bearer sk-abcdefghijklmnopqrstuvwxyz' https://x"}`
	got := string(Redact(json.RawMessage(in)))
	if strings.Contains(got, "sk-abcdefghijklmnopqrstuvwxyz") {
		t.Fatalf("secret survived: %s", got)
	}
	if !strings.Contains(got, "curl -H 'Authorization: [redacted]' https://x") {
		t.Fatalf("lost surrounding text: %s", got)
	}
}

func TestRedactReviewCases(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"usage counts kept", `{"input_tokens":120,"output_tokens":45}`, `{"input_tokens":120,"output_tokens":45}`},
		{"object under secret key replaced whole", `{"credentials":{"user":"bob","password":"hunter2"},"note":"ok"}`, `{"credentials":"[redacted]","note":"ok"}`},
		{"whole pem block", `{"f":"-----BEGIN RSA PRIVATE KEY-----\nMIIEow\nabc\n-----END RSA PRIVATE KEY-----\nrest"}`, `{"f":"[redacted]\nrest"}`},
		{"inline env assignment", `{"command":"API_KEY=abc123 npm run deploy"}`, `{"command":"API_KEY=[redacted] npm run deploy"}`},
		{"export env assignment", `{"command":"export GH_TOKEN=xyz && gh pr list"}`, `{"command":"export GH_TOKEN=[redacted] && gh pr list"}`},
		{"non-secret env kept", `{"command":"NODE_ENV=production npm start"}`, `{"command":"NODE_ENV=production npm start"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := string(Redact(json.RawMessage(c.in))); got != c.want {
				t.Fatalf("got %s\nwant %s", got, c.want)
			}
		})
	}
}

// Cases from the pre-merge review: real secrets that slipped through, and
// ordinary text that was wrongly redacted.
func TestRedactReviewProbes(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"path with -sk- inside", `{"cwd":"/home/u/projects/task-management-service-api"}`, ``},
		{"branch with -sk- inside", `{"arg":"git checkout disk-usage-monitoring-dashboard"}`, ``},
		{"real sk key after space", `{"arg":"OPENAI=x; echo sk-proj-abcdefghijklmnopqrstuv"}`, `{"arg":"OPENAI=x; echo [redacted]"}`},
		{"flag with =", `{"arg":"mysql --password=hunter2 -u root"}`, `{"arg":"mysql --password=[redacted] -u root"}`},
		{"flag with space", `{"arg":"tool --api-key abc123 run"}`, `{"arg":"tool --api-key [redacted] run"}`},
		{"url password", `{"arg":"psql postgres://admin:S3cretPass@db:5432/app"}`, `{"arg":"psql postgres://admin:[redacted]@db:5432/app"}`},
		{"url token", `{"arg":"git clone https://user:tok123@github.com/o/r"}`, `{"arg":"git clone https://user:[redacted]@github.com/o/r"}`},
		{"stripe", `{"out":"sk_live_` + strings.Repeat("a", 24) + `"}`, `{"out":"[redacted]"}`},
		{"google", `{"out":"AIza` + strings.Repeat("b", 35) + `"}`, `{"out":"[redacted]"}`},
		{"yaml line", `{"out":"db:\n  password: hunter2\n  host: x"}`, `{"out":"db:\n  password: [redacted]\n  host: x"}`},
		{"json in a command", `{"arg":"curl -d '{\"password\":\"hunter2\",\"user\":\"bob\"}' x"}`, `{"arg":"curl -d '{\"password\":\"[redacted]\",\"user\":\"bob\"}' x"}`},
		{"max_tokens kept", `{"max_tokens":"1000","tokenizer":"bpe"}`, ``},
		{"camelCase secret", `{"accessToken":"abc","clientSecret":"def"}`, `{"accessToken":"[redacted]","clientSecret":"[redacted]"}`},
		{"plain colon prose kept", `{"last":"Note: the build passed"}`, ``},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			want := c.want
			if want == "" {
				want = c.in // unchanged
			}
			if got := string(Redact(json.RawMessage(c.in))); got != want {
				t.Fatalf("\n got  %s\n want %s", got, want)
			}
		})
	}
}
