package acp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeMCPServersUnionAndTolerance(t *testing.T) {
	raw := json.RawMessage(`[
		{"name":"fs","command":"npx","args":["-y","server-filesystem","/tmp"],"env":[{"name":"TOKEN","value":"s3cret"}]},
		{"name":"remote","type":"http","url":"https://example.com/mcp","headers":[{"name":"Authorization","value":"Bearer top-secret"}]},
		{"name":"events","type":"sse","url":"https://example.com/sse"},
		{"name":"explicit","type":"stdio","command":"uvx","args":["mcp-server-git"]},
		{"name":"broken"},
		{"type":"http","url":"https://example.com/no-name"},
		{"name":"weird","type":"carrier-pigeon","headers":[{"name":"X-Api-Key","value":"leak-me"}]},
		"not-an-object"
	]`)

	servers, issues := DecodeMCPServers(raw)
	if len(servers) != 4 {
		t.Fatalf("expected 4 usable servers, got %d: %+v", len(servers), servers)
	}
	if len(issues) != 4 {
		t.Fatalf("expected 4 skipped entries, got %d: %+v", len(issues), issues)
	}

	stdio := servers[0]
	if stdio.TransportKind() != MCPTransportStdio || stdio.Name != "fs" || stdio.Command != "npx" {
		t.Fatalf("stdio server = %+v", stdio)
	}
	if len(stdio.Args) != 3 || stdio.Args[0] != "-y" {
		t.Fatalf("stdio args = %+v", stdio.Args)
	}
	if len(stdio.Env) != 1 || stdio.Env[0].Name != "TOKEN" || stdio.Env[0].Value != "s3cret" {
		t.Fatalf("stdio env = %+v", stdio.Env)
	}

	http := servers[1]
	if http.TransportKind() != MCPTransportHTTP || http.URL != "https://example.com/mcp" {
		t.Fatalf("http server = %+v", http)
	}
	if len(http.Headers) != 1 || http.Headers[0].Name != "Authorization" {
		t.Fatalf("http headers = %+v", http.Headers)
	}

	sse := servers[2]
	if sse.TransportKind() != MCPTransportSSE || sse.URL != "https://example.com/sse" {
		t.Fatalf("sse server = %+v", sse)
	}

	// 显式 "stdio" 与缺省 type 等价。
	explicit := servers[3]
	if explicit.TransportKind() != MCPTransportStdio || explicit.Command != "uvx" {
		t.Fatalf("explicit stdio server = %+v", explicit)
	}

	// 逐条容错：下标必须指向原始数组位置，原因必须可诊断。
	wantIssues := []struct {
		index  int
		name   string
		reason string
	}{
		{index: 4, name: "broken", reason: "command"},
		{index: 5, name: "", reason: "name"},
		{index: 6, name: "weird", reason: "carrier-pigeon"},
		{index: 7, name: "", reason: "JSON object"},
	}
	for i, want := range wantIssues {
		got := issues[i]
		if got.Index != want.index || got.Name != want.name || !strings.Contains(got.Reason, want.reason) {
			t.Fatalf("issue[%d] = %+v, want index=%d name=%q reason~%q", i, got, want.index, want.name, want.reason)
		}
	}
}

func TestDecodeMCPServersNeverLeaksCredentialValues(t *testing.T) {
	raw := json.RawMessage(`[{"name":"weird","type":"carrier-pigeon","env":[{"name":"API_KEY","value":"leak-me"}]}]`)
	_, issues := DecodeMCPServers(raw)
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %+v", issues)
	}
	if text := issues[0].String(); strings.Contains(text, "leak-me") {
		t.Fatalf("issue leaked a credential value: %s", text)
	}
}

func TestDecodeMCPServersFieldLevelErrors(t *testing.T) {
	if servers, issues := DecodeMCPServers(nil); servers != nil || issues != nil {
		t.Fatalf("nil payload must be a silent no-op, got %+v / %+v", servers, issues)
	}
	if servers, issues := DecodeMCPServers(json.RawMessage("null")); servers != nil || issues != nil {
		t.Fatalf("null payload must be a silent no-op, got %+v / %+v", servers, issues)
	}

	_, issues := DecodeMCPServers(json.RawMessage(`{"name":"fs","command":"npx"}`))
	if len(issues) != 1 || issues[0].Index != -1 || !strings.Contains(issues[0].Reason, "array") {
		t.Fatalf("object payload issues = %+v", issues)
	}

	_, issues = DecodeMCPServers(json.RawMessage(`[{"name":"fs",`))
	if len(issues) != 1 || issues[0].Index != -1 || !strings.Contains(issues[0].Reason, "invalid JSON") {
		t.Fatalf("broken payload issues = %+v", issues)
	}
}

func TestDecodeMCPServersRejectsObjectFormKeyValues(t *testing.T) {
	// 规范要求 env/headers 是 [{name,value}] 数组；map 形态属非法条目，
	// 必须逐条跳过并留下可见诊断（§2.2、§4.3），且不得回显任何值。
	raw := json.RawMessage(`[{"name":"fs","command":"npx","env":{"TOKEN":"super-secret"}}]`)
	servers, issues := DecodeMCPServers(raw)
	if len(servers) != 0 {
		t.Fatalf("servers = %+v, want the illegal entry skipped", servers)
	}
	if len(issues) != 1 || issues[0].Index != 0 || issues[0].Name != "fs" {
		t.Fatalf("issues = %+v", issues)
	}
	if strings.Contains(issues[0].String(), "super-secret") {
		t.Fatalf("issue leaked a credential value: %q", issues[0].String())
	}
}

func TestMCPDecodeIssueString(t *testing.T) {
	if got := (MCPDecodeIssue{Index: 2, Name: "fs", Reason: "boom"}).String(); !strings.Contains(got, "mcpServers[2]") || !strings.Contains(got, `name="fs"`) {
		t.Fatalf("issue string = %q", got)
	}
	if got := (MCPDecodeIssue{Index: -1, Reason: "boom"}).String(); !strings.HasPrefix(got, "mcpServers: ") {
		t.Fatalf("field-level issue string = %q", got)
	}
}
