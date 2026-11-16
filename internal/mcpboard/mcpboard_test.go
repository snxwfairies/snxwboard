package mcpboard

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/snxwfairies/snxwboard/internal/features"
)

// fakeBackend serves fixed state and records what the tools wrote.
type fakeBackend struct {
	sessions []Session
	events   []Event
	ingested []IngestEvent
	err      error
}

func (f *fakeBackend) Sessions(context.Context) ([]Session, error) {
	return f.sessions, f.err
}

func (f *fakeBackend) History(context.Context, string, int) ([]Event, error) {
	return f.events, f.err
}

func (f *fakeBackend) Ingest(_ context.Context, ev IngestEvent) error {
	if f.err != nil {
		return f.err
	}
	f.ingested = append(f.ingested, ev)
	return nil
}

func seeded() (*fakeBackend, []Session) {
	sessions := []Session{
		{ID: "calm", Status: "running", Repo: "snxwboard", UpdatedAt: 100},
		{ID: "blocked", Status: "waiting_for_you", Repo: "cctverp.com", UpdatedAt: 50},
		{ID: "broke", Status: "failed", Repo: "snxwboard", UpdatedAt: 80},
		{ID: "fresh", Status: "running", Repo: "verba", UpdatedAt: 200},
	}
	return &fakeBackend{sessions: sessions}, sessions
}

// serve runs one conversation through the server and returns the decoded
// responses, keyed by the order they were written.
// on builds a feature set, failing the test on an invalid name.
func on(t *testing.T, names ...string) features.Set {
	t.Helper()
	set, err := features.Resolve(names)
	if err != nil {
		t.Fatalf("features %v: %v", names, err)
	}
	return set
}

func serve(t *testing.T, b Backend, lines ...string) []map[string]any {
	t.Helper()
	return serveWith(t, b, features.Set{}, lines...)
}

func serveWith(t *testing.T, b Backend, set features.Set, lines ...string) []map[string]any {
	t.Helper()
	var out bytes.Buffer
	if err := New(b, set).Serve(context.Background(), strings.NewReader(strings.Join(lines, "\n")+"\n"), &out); err != nil {
		t.Fatalf("serve: %v", err)
	}
	var resps []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("response is not JSON: %q: %v", line, err)
		}
		resps = append(resps, m)
	}
	return resps
}

func call(id, name, args string) string {
	return `{"jsonrpc":"2.0","id":` + id + `,"method":"tools/call","params":{"name":"` + name + `","arguments":` + args + `}}`
}

func toolJSON(t *testing.T, resp map[string]any) map[string]any {
	t.Helper()
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %v", resp)
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("no content: %v", result)
	}
	first := content[0].(map[string]any)
	var decoded map[string]any
	if err := json.Unmarshal([]byte(first["text"].(string)), &decoded); err != nil {
		t.Fatalf("tool text is not JSON: %v", first["text"])
	}
	return decoded
}

func TestInitializeAdvertisesTools(t *testing.T) {
	b, _ := seeded()
	resps := serve(t, b, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if len(resps) != 1 {
		t.Fatalf("want 1 response, got %d", len(resps))
	}
	result := resps[0]["result"].(map[string]any)
	if result["protocolVersion"] != ProtocolVersion {
		t.Errorf("protocolVersion = %v", result["protocolVersion"])
	}
	caps := result["capabilities"].(map[string]any)
	if _, ok := caps["tools"]; !ok {
		t.Errorf("tools capability missing: %v", caps)
	}
	info := result["serverInfo"].(map[string]any)
	if info["name"] != "snxwboard" {
		t.Errorf("serverInfo name = %v", info["name"])
	}
}

func TestNotificationsGetNoReply(t *testing.T) {
	b, _ := seeded()
	resps := serve(t, b,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
	)
	if len(resps) != 1 {
		t.Fatalf("a notification must not be answered; got %d responses", len(resps))
	}
}

func TestToolsListCoversEveryTool(t *testing.T) {
	b, _ := seeded()
	resps := serveWith(t, b, on(t, "session_search"), `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	tools := resps[0]["result"].(map[string]any)["tools"].([]any)
	want := []string{"whats_running", "list_sessions", "get_session", "search_feed", "notify_me", "snxw_ingest"}
	seen := map[string]bool{}
	for _, raw := range tools {
		tool := raw.(map[string]any)
		seen[tool["name"].(string)] = true
		schema, ok := tool["inputSchema"].(map[string]any)
		if !ok || schema["type"] != "object" {
			t.Errorf("%v has no object inputSchema", tool["name"])
		}
		if _, ok := schema["properties"]; !ok {
			t.Errorf("%v has no properties", tool["name"])
		}
	}
	for _, name := range want {
		if !seen[name] {
			t.Errorf("tool %q missing", name)
		}
	}
}

func TestWhatsRunningPutsTheHumanFirst(t *testing.T) {
	b, _ := seeded()
	got := toolJSON(t, serve(t, b, call("3", "whats_running", `{}`))[0])

	if got["total"].(float64) != 4 {
		t.Errorf("total = %v", got["total"])
	}
	byStatus := got["byStatus"].(map[string]any)
	if byStatus["running"].(float64) != 2 || byStatus["waiting_for_you"].(float64) != 1 {
		t.Errorf("byStatus = %v", byStatus)
	}
	waiting := got["waitingOnHuman"].([]any)
	if len(waiting) != 1 || waiting[0].(map[string]any)["id"] != "blocked" {
		t.Errorf("waitingOnHuman = %v", waiting)
	}
	// running is newest-first, so the agent reads the freshest work first
	running := got["running"].([]any)
	if len(running) != 2 || running[0].(map[string]any)["id"] != "fresh" {
		t.Errorf("running = %v", running)
	}
}

func TestListSessionsFiltersAndOrders(t *testing.T) {
	b, _ := seeded()
	all := toolJSON(t, serve(t, b, call("4", "list_sessions", `{}`))[0])
	ids := []string{}
	for _, raw := range all["sessions"].([]any) {
		ids = append(ids, raw.(map[string]any)["id"].(string))
	}
	// waiting first, then failed, then running newest-first
	want := []string{"blocked", "broke", "fresh", "calm"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", ids, want)
	}

	filtered := toolJSON(t, serve(t, b, call("5", "list_sessions", `{"status":"running"}`))[0])
	if filtered["count"].(float64) != 2 {
		t.Errorf("status filter count = %v", filtered["count"])
	}

	byRepo := toolJSON(t, serve(t, b, call("6", "list_sessions", `{"repo":"SNXW"}`))[0])
	if byRepo["count"].(float64) != 2 {
		t.Errorf("repo filter is case-insensitive and should keep 2, got %v", byRepo["count"])
	}
}

func TestGetSession(t *testing.T) {
	b, _ := seeded()
	got := toolJSON(t, serve(t, b, call("7", "get_session", `{"id":"broke"}`))[0])
	if got["status"] != "failed" {
		t.Errorf("status = %v", got["status"])
	}

	resp := serve(t, b, call("8", "get_session", `{"id":"nope"}`))[0]
	result := resp["result"].(map[string]any)
	if result["isError"] != true {
		t.Fatalf("an unknown session must be an error result, got %v", resp)
	}
}

func TestSearchFeedMatchesKindOrBody(t *testing.T) {
	b, _ := seeded()
	b.events = []Event{
		{Seq: 1, Kind: "tool.start", Session: "a", Body: json.RawMessage(`{"tool":"Bash","command":"npm test"}`)},
		{Seq: 2, Kind: "attention", Session: "b", Body: json.RawMessage(`{"reason":"permission prompt"}`)},
		{Seq: 3, Kind: "tool.end", Session: "a", Body: json.RawMessage(`{"tool":"Bash","ok":true}`)},
	}

	searchOn := on(t, "session_search")
	got := toolJSON(t, serveWith(t, b, searchOn, call("9", "search_feed", `{"query":"bash"}`))[0])
	if got["count"].(float64) != 2 || got["scanned"].(float64) != 3 {
		t.Errorf("query bash: count=%v scanned=%v", got["count"], got["scanned"])
	}

	att := toolJSON(t, serveWith(t, b, searchOn, call("10", "search_feed", `{"query":"permission"}`))[0])
	events := att["events"].([]any)
	if len(events) != 1 || events[0].(map[string]any)["seq"].(float64) != 2 {
		t.Errorf("query permission: %v", events)
	}

	empty := serveWith(t, b, searchOn, call("11", "search_feed", `{"query":"  "}`))[0]
	if empty["result"].(map[string]any)["isError"] != true {
		t.Errorf("a blank query must be an error result")
	}
}

func TestNotifyMeRecordsAttention(t *testing.T) {
	b, _ := seeded()
	got := toolJSON(t, serve(t, b, call("12", "notify_me", `{"message":"need a decision on schema","session":"calm"}`))[0])
	if got["notified"] != true {
		t.Errorf("notified = %v", got["notified"])
	}
	if len(b.ingested) != 1 {
		t.Fatalf("want one ingested event, got %d", len(b.ingested))
	}
	ev := b.ingested[0]
	if ev.Kind != "attention" || ev.Session != "calm" || ev.Source != "mcp" {
		t.Errorf("ingested = %+v", ev)
	}
	if !strings.Contains(string(ev.Body), "need a decision on schema") {
		t.Errorf("body lost the message: %s", ev.Body)
	}

	missing := serve(t, b, call("13", "notify_me", `{}`))[0]
	if missing["result"].(map[string]any)["isError"] != true {
		t.Errorf("a missing message must be an error result")
	}
}

func TestSnxwIngestValidatesKind(t *testing.T) {
	b, _ := seeded()
	ok := toolJSON(t, serve(t, b, call("14", "snxw_ingest", `{"kind":"tool.start","session":"calm"}`))[0])
	if ok["accepted"] != true {
		t.Errorf("accepted = %v", ok["accepted"])
	}
	if len(b.ingested) != 1 || b.ingested[0].Kind != "tool.start" {
		t.Errorf("ingested = %+v", b.ingested)
	}

	bad := serve(t, b, call("15", "snxw_ingest", `{"kind":"anything"}`))[0]
	if bad["result"].(map[string]any)["isError"] != true {
		t.Errorf("an unknown kind must be an error result")
	}
}

func TestGatedToolIsHiddenAndRefusedWhenItsFeatureIsOff(t *testing.T) {
	b, _ := seeded()
	// off: the tool is not in the catalogue a client sees
	resps := serve(t, b, `{"jsonrpc":"2.0","id":20,"method":"tools/list"}`)
	for _, raw := range resps[0]["result"].(map[string]any)["tools"].([]any) {
		if raw.(map[string]any)["name"] == "search_feed" {
			t.Fatal("search_feed is listed while session_search is off")
		}
	}
	// off: and it cannot be called anyway, even by a client that remembers it
	resp := serve(t, b, call("21", "search_feed", `{"query":"x"}`))[0]
	result := resp["result"].(map[string]any)
	if result["isError"] != true {
		t.Fatalf("a gated tool must refuse, got %v", resp)
	}
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "snxw features enable session_search") {
		t.Errorf("the refusal must say how to enable it, got %q", text)
	}
}

func TestGatedToolIsListedWhenItsFeatureIsOn(t *testing.T) {
	b, _ := seeded()
	resps := serveWith(t, b, on(t, "session_search"), `{"jsonrpc":"2.0","id":22,"method":"tools/list"}`)
	found := false
	for _, raw := range resps[0]["result"].(map[string]any)["tools"].([]any) {
		if raw.(map[string]any)["name"] == "search_feed" {
			found = true
		}
	}
	if !found {
		t.Fatal("search_feed is not listed with session_search on")
	}
}

func TestCoreToolsAreNeverGated(t *testing.T) {
	b, _ := seeded()
	resps := serve(t, b, `{"jsonrpc":"2.0","id":23,"method":"tools/list"}`)
	want := map[string]bool{"whats_running": false, "list_sessions": false, "get_session": false, "notify_me": false, "snxw_ingest": false}
	for _, raw := range resps[0]["result"].(map[string]any)["tools"].([]any) {
		name := raw.(map[string]any)["name"].(string)
		if _, ok := want[name]; ok {
			want[name] = true
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("%q is missing with every feature off: observing and asking the human are not autonomy", name)
		}
	}
}

func TestUnknownToolIsInvalidParams(t *testing.T) {
	b, _ := seeded()
	resp := serve(t, b, call("16", "no_such_tool", `{}`))[0]
	rpcErr, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("want a JSON-RPC error, got %v", resp)
	}
	if rpcErr["code"].(float64) != codeParams {
		t.Errorf("code = %v, want %v", rpcErr["code"], codeParams)
	}
}

func TestUnknownMethod(t *testing.T) {
	b, _ := seeded()
	resp := serve(t, b, `{"jsonrpc":"2.0","id":17,"method":"resources/list"}`)[0]
	if resp["error"].(map[string]any)["code"].(float64) != codeMethod {
		t.Errorf("want -32601, got %v", resp["error"])
	}
}

func TestBadJSONGetsParseErrorWithNullID(t *testing.T) {
	b, _ := seeded()
	resp := serve(t, b, `{not json`)[0]
	rpcErr := resp["error"].(map[string]any)
	if rpcErr["code"].(float64) != codeParse {
		t.Errorf("code = %v", rpcErr["code"])
	}
	if id, present := resp["id"]; !present || id != nil {
		t.Errorf("id must be null, got %v (present %v)", id, present)
	}
}

func TestBackendFailureIsAnErrorResult(t *testing.T) {
	b := &fakeBackend{err: context.DeadlineExceeded}
	resp := serve(t, b, call("18", "whats_running", `{}`))[0]
	if resp["result"].(map[string]any)["isError"] != true {
		t.Errorf("a backend failure must be an error result the model can read, got %v", resp)
	}
}
