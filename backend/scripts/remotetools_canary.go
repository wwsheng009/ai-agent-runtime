//go:build ignore

// Command remotetools_canary drives a live aicli-remote-tools server over the
// real MCP Streamable HTTP transport. It is the Linux/WSL-native counterpart of
// scripts/canary-remote-tools.ps1: the PowerShell canary cannot run inside WSL
// because WSL has no pwsh, while the Go SDK is already a backend dependency.
//
// The canary only uses read tools and one git-log command that is expected to
// be allowlisted on the target. It never writes to the remote workspace.
//
// Usage:
//
//	go run ./scripts/remotetools_canary.go \
//	  -url http://172.18.3.238:18765/mcp \
//	  -env-file /mnt/e/projects/itsm/itsm/.env \
//	  -scenario smoke
//
// Token handling: the token is read from the environment or from -env-file and
// is never printed. The -url value must not contain a token.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	maxToolDescriptionBytes = 4 * 1024
	maxToolSchemaBytes      = 64 * 1024
	maxToolCount            = 256
)

var artifactIDPattern = regexp.MustCompile(`art_[0-9a-fA-F]{8,}`)

type toolSummary struct {
	Name             string `json:"name"`
	DescriptionBytes int    `json:"description_bytes"`
	SchemaBytes      int    `json:"schema_bytes"`
	ReadOnly         bool   `json:"read_only"`
}

type callRecord struct {
	Tool       string `json:"tool"`
	OK         bool   `json:"ok"`
	Expected   bool   `json:"expected,omitempty"`
	IsError    bool   `json:"is_error"`
	Bytes      int    `json:"bytes"`
	DurationMS int64  `json:"duration_ms"`
	ArtifactID string `json:"artifact_id,omitempty"`
	Preview    string `json:"preview,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

type smokeReport struct {
	Scenario string        `json:"scenario"`
	URL      string        `json:"url"`
	Prefix   string        `json:"prefix"`
	Tools    []toolSummary `json:"tools"`
	Results  []callRecord  `json:"results"`
}

type soakReport struct {
	Scenario  string        `json:"scenario"`
	URL       string        `json:"url"`
	Prefix    string        `json:"prefix"`
	Count     int           `json:"count"`
	OK        int           `json:"ok"`
	Failed    int           `json:"failed"`
	ErrorRate float64       `json:"error_rate"`
	P50MS     int64         `json:"p50_ms"`
	P95MS     int64         `json:"p95_ms"`
	Artifact  string        `json:"artifact_id,omitempty"`
	Tools     []toolSummary `json:"tools"`
	Failures  []callRecord  `json:"failures,omitempty"`
}

type canary struct {
	ctx     context.Context
	session *mcp.ClientSession
	prefix  string
	tools   []toolSummary
	records []callRecord
}

func main() {
	urlFlag := flag.String("url", "http://172.18.3.238:18765/mcp", "MCP endpoint")
	envFile := flag.String("env-file", "", "file containing the bearer token (KEY=VALUE)")
	tokenEnv := flag.String("token-env", "AICLI_REMOTE_TOKEN", "environment variable holding the bearer token")
	prefixFlag := flag.String("prefix", "remote_238", "expected tool-name prefix")
	scenario := flag.String("scenario", "smoke", "smoke or soak")
	count := flag.Int("count", 100, "number of calls for the soak scenario")
	listTools := flag.Bool("list-tools", false, "print the tool surface and exit")
	timeout := flag.Duration("timeout", 180*time.Second, "overall timeout")
	flag.Parse()

	if strings.TrimSpace(*prefixFlag) == "" {
		fatalf("-prefix must not be empty")
	}
	if *count <= 0 {
		fatalf("-count must be positive")
	}

	token, err := resolveToken(*envFile, *tokenEnv)
	if err != nil {
		fatalf("%v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	client := mcp.NewClient(&mcp.Implementation{Name: "toolcore-238-wsl-canary", Version: "1.0.0"}, nil)
	transport := &mcp.StreamableClientTransport{Endpoint: *urlFlag}
	transport.HTTPClient = bearerClient(token)

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		fatalf("connect: %v", err)
	}
	defer session.Close()

	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		fatalf("tools/list: %v", err)
	}

	summaries, err := validateTools(listed.Tools, *prefixFlag)
	if err != nil {
		fatalf("%v", err)
	}
	for _, summary := range summaries {
		fmt.Printf("TOOL %s description=%dB schema=%dB readOnly=%t\n",
			summary.Name, summary.DescriptionBytes, summary.SchemaBytes, summary.ReadOnly)
	}

	if *listTools {
		writeJSON(summaries)
		return
	}

	c := &canary{ctx: ctx, session: session, prefix: *prefixFlag, tools: summaries}
	switch strings.ToLower(strings.TrimSpace(*scenario)) {
	case "smoke":
		c.smoke(*urlFlag)
	case "soak":
		c.soak(*urlFlag, *count)
	default:
		fatalf("unknown scenario %q (want smoke or soak)", *scenario)
	}
}

func resolveToken(envFile, envName string) (string, error) {
	if value := strings.TrimSpace(os.Getenv(envName)); value != "" {
		return normalizeToken(value), nil
	}
	if strings.TrimSpace(envFile) == "" {
		return "", fmt.Errorf("%s is unset and -env-file is empty", envName)
	}
	data, err := os.ReadFile(envFile)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", envFile, err)
	}
	for _, raw := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(strings.TrimPrefix(raw, "\ufeff"))
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		if !strings.HasPrefix(line, envName+"=") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(line, envName+"="))
		value = strings.Trim(value, `"'`)
		value = normalizeToken(value)
		if value == "" {
			return "", fmt.Errorf("%s is empty in %s", envName, envFile)
		}
		return value, nil
	}
	return "", fmt.Errorf("%s not found in %s", envName, envFile)
}

func normalizeToken(value string) string {
	return strings.TrimSpace(strings.Trim(value, `"'`))
}

func bearerClient(token string) *http.Client {
	return &http.Client{Transport: &bearerRoundTripper{token: token, base: http.DefaultTransport}}
}

type bearerRoundTripper struct {
	token string
	base  http.RoundTripper
}

func (rt *bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	cloned.Header.Set("Authorization", "Bearer "+rt.token)
	base := rt.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(cloned)
}

func validateTools(tools []*mcp.Tool, prefix string) ([]toolSummary, error) {
	if len(tools) == 0 {
		return nil, fmt.Errorf("tools/list returned no tools")
	}
	if len(tools) > maxToolCount {
		return nil, fmt.Errorf("tools/list returned %d tools, limit is %d", len(tools), maxToolCount)
	}

	seen := make(map[string]bool, len(tools))
	summaries := make([]toolSummary, 0, len(tools))
	for _, tool := range tools {
		if tool == nil || strings.TrimSpace(tool.Name) == "" {
			return nil, fmt.Errorf("tools/list returned an unnamed tool")
		}
		if !strings.HasPrefix(tool.Name, prefix+"__") {
			return nil, fmt.Errorf("tool %q does not start with %q", tool.Name, prefix+"__")
		}
		if seen[tool.Name] {
			return nil, fmt.Errorf("duplicate tool name %q", tool.Name)
		}
		seen[tool.Name] = true

		if len([]byte(tool.Description)) > maxToolDescriptionBytes {
			return nil, fmt.Errorf("tool %q description is %d bytes, limit is %d",
				tool.Name, len([]byte(tool.Description)), maxToolDescriptionBytes)
		}
		schema, err := json.Marshal(tool.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("marshal schema for %q: %w", tool.Name, err)
		}
		if len(schema) > maxToolSchemaBytes {
			return nil, fmt.Errorf("tool %q schema is %d bytes, limit is %d",
				tool.Name, len(schema), maxToolSchemaBytes)
		}
		summaries = append(summaries, toolSummary{
			Name:             tool.Name,
			DescriptionBytes: len([]byte(tool.Description)),
			SchemaBytes:      len(schema),
			ReadOnly:         tool.Annotations != nil && tool.Annotations.ReadOnlyHint,
		})
	}
	sort.Slice(summaries, func(i, j int) bool { return summaries[i].Name < summaries[j].Name })

	for _, suffix := range []string{"view", "list_dir", "glob", "grep", "shell", "artifact_read"} {
		name := prefix + "__" + suffix
		if !seen[name] {
			return nil, fmt.Errorf("missing required tool %q", name)
		}
	}
	return summaries, nil
}

func (c *canary) tool(suffix string) string {
	return c.prefix + "__" + suffix
}

func (c *canary) smoke(urlFlag string) {
	type readCase struct {
		label string
		tool  string
		args  map[string]any
	}
	cases := []readCase{
		{"view README.md", c.tool("view"), map[string]any{"file_path": "README.md", "limit": 20}},
		{"list_dir .", c.tool("list_dir"), map[string]any{"path": "."}},
		{"glob **/*.md", c.tool("glob"), map[string]any{"pattern": "**/*.md", "path": "."}},
		{"grep README", c.tool("grep"), map[string]any{"pattern": "README", "path": ".", "glob": "*.md"}},
		{"shell git status", c.tool("shell"), map[string]any{"command": "git status"}},
	}
	for _, test := range cases {
		body, isError, err := c.call(test.tool, test.args)
		if err != nil {
			fatalf("%s: transport error: %v", test.label, err)
		}
		if isError {
			fatalf("%s: tool error: %s", test.label, preview(body))
		}
		if strings.TrimSpace(body) == "" {
			fatalf("%s: empty result", test.label)
		}
		fmt.Printf("PASS %s (%dB)\n", test.label, len([]byte(body)))
	}

	// git log --stat is large enough to force the remote output folder and
	// produce an artifact_id that artifact_read can page.
	body, isError, err := c.call(c.tool("shell"), map[string]any{
		"command": "git log --stat -n 100",
	})
	if err != nil {
		fatalf("shell git log: transport error: %v", err)
	}
	if isError {
		fatalf("shell git log: tool error: %s", preview(body))
	}
	artifact := extractArtifactID(body)
	if artifact == "" {
		fatalf("shell git log did not fold: no artifact_id in %d bytes", len([]byte(body)))
	}
	fmt.Printf("PASS shell git log folded to artifact %s\n", artifact)

	page, isError, err := c.call(c.tool("artifact_read"), map[string]any{
		"artifact_id": artifact,
		"offset":      0,
		"limit":       4096,
	})
	if err != nil {
		fatalf("artifact_read: transport error: %v", err)
	}
	if isError {
		fatalf("artifact_read: tool error: %s", preview(page))
	}
	if !strings.Contains(page, "commit ") {
		fatalf("artifact_read: expected git log content, got %s", preview(page))
	}
	fmt.Printf("PASS artifact_read (%dB)\n", len([]byte(page)))

	c.expectFailure("artifact cross-target denial", c.tool("artifact_read"), map[string]any{
		"artifact_ref": "remote:999:" + artifact,
		"offset":       0,
		"limit":        64,
	}, []string{"ARTIFACT_SCOPE_DENIED", "artifact scope denied"})

	c.expectFailure("path traversal denial", c.tool("view"), map[string]any{
		"file_path": "../../etc/passwd",
	}, []string{"PATH_DENIED"})

	c.expectFailure("shell allowlist denial", c.tool("shell"), map[string]any{
		"command": "whoami",
	}, []string{"COMMAND_DENIED"})

	c.expectFailure("destructive command denial", c.tool("shell"), map[string]any{
		"command": "rm -rf /",
	}, []string{"COMMAND_DENIED"})

	report := smokeReport{
		Scenario: "smoke",
		URL:      redactURL(urlFlag),
		Prefix:   c.prefix,
		Tools:    c.tools,
		Results:  c.records,
	}
	writeJSON(report)
	fmt.Println("ALL PASS")
}

func (c *canary) soak(urlFlag string, count int) {
	body, isError, err := c.call(c.tool("shell"), map[string]any{
		"command": "git log --stat -n 100",
	})
	if err != nil {
		fatalf("shell git log: transport error: %v", err)
	}
	if isError {
		fatalf("shell git log: tool error: %s", preview(body))
	}
	artifact := extractArtifactID(body)
	if artifact == "" {
		fatalf("shell git log did not fold: no artifact_id in %d bytes", len([]byte(body)))
	}

	calls := []struct {
		name string
		args map[string]any
	}{
		{c.tool("view"), map[string]any{"file_path": "README.md", "limit": 20}},
		{c.tool("list_dir"), map[string]any{"path": "."}},
		{c.tool("glob"), map[string]any{"pattern": "**/*.md", "path": "."}},
		{c.tool("grep"), map[string]any{"pattern": "README", "path": ".", "glob": "*.md"}},
		{c.tool("shell"), map[string]any{"command": "git status"}},
		{c.tool("artifact_read"), map[string]any{
			"artifact_id": artifact,
			"offset":      0,
			"limit":       4096,
		}},
	}

	startIndex := len(c.records)
	for i := 0; i < count; i++ {
		call := calls[i%len(calls)]
		_, _, _ = c.call(call.name, call.args)
	}
	window := c.records[startIndex:]

	failed := 0
	durations := make([]int64, 0, len(window))
	failures := make([]callRecord, 0)
	for _, record := range window {
		durations = append(durations, record.DurationMS)
		if !record.OK {
			failed++
			failures = append(failures, record)
		}
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	report := soakReport{
		Scenario:  "soak",
		URL:       redactURL(urlFlag),
		Prefix:    c.prefix,
		Count:     len(window),
		OK:        len(window) - failed,
		Failed:    failed,
		ErrorRate: float64(failed) / float64(len(window)),
		P50MS:     percentile(durations, 0.50),
		P95MS:     percentile(durations, 0.95),
		Artifact:  artifact,
		Tools:     c.tools,
		Failures:  failures,
	}
	writeJSON(report)
	if failed > 0 {
		os.Exit(1)
	}
}

func (c *canary) call(name string, args map[string]any) (string, bool, error) {
	start := time.Now()
	result, err := c.session.CallTool(c.ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	elapsed := time.Since(start).Milliseconds()

	record := callRecord{
		Tool:       name,
		OK:         err == nil,
		IsError:    err != nil,
		DurationMS: elapsed,
	}
	if err != nil {
		record.Detail = err.Error()
		c.records = append(c.records, record)
		return "", true, err
	}

	body := resultText(result)
	record.OK = !result.IsError
	record.IsError = result.IsError
	record.Bytes = len([]byte(body))
	record.ArtifactID = extractArtifactID(body)
	record.Preview = preview(body)
	if result.IsError {
		record.Detail = preview(body)
	}
	c.records = append(c.records, record)
	return body, result.IsError, nil
}

func (c *canary) expectFailure(label, tool string, args map[string]any, wants []string) {
	body, isError, err := c.call(tool, args)
	if err == nil && !isError {
		fatalf("%s: expected failure, got success: %s", label, preview(body))
	}
	combined := body
	if err != nil {
		combined = err.Error()
	}
	for _, want := range wants {
		if strings.Contains(combined, want) {
			markLastExpected(c.records, want)
			fmt.Printf("PASS %s (%s)\n", label, want)
			return
		}
	}
	fatalf("%s: expected one of %v, got %s", label, wants, preview(combined))
}

func markLastExpected(records []callRecord, detail string) {
	if len(records) == 0 {
		return
	}
	last := &records[len(records)-1]
	last.OK = true
	last.Expected = true
	last.Detail = detail
}

func resultText(result *mcp.CallToolResult) string {
	var builder strings.Builder
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			builder.WriteString(text.Text)
		}
	}
	return builder.String()
}

func extractArtifactID(body string) string {
	return artifactIDPattern.FindString(body)
}

func preview(body string) string {
	flat := strings.Join(strings.Fields(body), " ")
	runes := []rune(flat)
	if len(runes) > 180 {
		return string(runes[:180]) + "..."
	}
	return flat
}

func percentile(sorted []int64, fraction float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	index := int(float64(len(sorted)-1) * fraction)
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}

func redactURL(raw string) string {
	// The canary never accepts credentials in the URL, but strip a query and
	// fragment defensively so a stray token cannot be persisted in evidence.
	if index := strings.IndexAny(raw, "?#"); index >= 0 {
		return raw[:index]
	}
	return raw
}

func writeJSON(value any) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		fatalf("encode report: %v", err)
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "FAIL: "+format+"\n", args...)
	os.Exit(1)
}
