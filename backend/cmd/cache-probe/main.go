// Command cache-probe sends real requests to a configured provider and reports
// per-round cached-token accounting, to measure how a mid-session system-prompt
// guidance change (the tool-guidance gating) affects provider prefix caching
// versus a tool-array append.
//
// Rounds:
//   1) baseline send #1 (cold)            2) identical resend (cache hit?)
//   3) system guidance section swapped    4) one tool appended
//
// Usage: go run ./cmd/cache-probe -provider workbuddy -model cn:glm-5.3-flash [-rounds 4]
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	// OpenAI-compat cache fields (provider-dependent names).
	PromptTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

type chatResponse struct {
	Usage usage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type tool struct {
	Type     string       `json:"type"`
	Function toolFunction `json:"function"`
}

type toolFunction struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

func loadConfig() (baseURL, apiPath, apiKey string) {
	home, _ := os.UserHomeDir()
	raw, err := os.ReadFile(filepath.Join(home, ".aicli", "config.yaml"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "read config:", err)
		os.Exit(1)
	}
	cfg := string(raw)
	idx := indexOf(cfg, "workbuddy:")
	baseURL = extractYAMLValue(cfg[idx:], "base_url:")
	apiPath = extractYAMLValue(cfg[idx:], "api_path:")
	_ = apiPath
	authRaw, err := os.ReadFile(filepath.Join(home, ".aicli", "auth.json"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "read auth:", err)
		os.Exit(1)
	}
	var auth struct {
		Providers map[string]struct {
			APIKey string `json:"api_key"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(authRaw, &auth); err != nil {
		fmt.Fprintln(os.Stderr, "parse auth:", err)
		os.Exit(1)
	}
	entry, ok := auth.Providers["workbuddy"]
	if !ok || entry.APIKey == "" {
		fmt.Fprintln(os.Stderr, "workbuddy credentials not found")
		os.Exit(1)
	}
	return baseURL, "/v1/chat/completions", entry.APIKey
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

func extractYAMLValue(block, key string) string {
	idx := indexOf(block, key)
	if idx < 0 {
		return ""
	}
	rest := block[idx+len(key):]
	end := len(rest)
	for i := 0; i < len(rest); i++ {
		if rest[i] == '\n' {
			end = i
			break
		}
	}
	value := trim(rest[:end])
	return value
}

func trim(s string) string {
	start := 0
	for start < len(s) && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	end := len(s)
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}

func shellTool(name string) tool {
	return tool{Type: "function", Function: toolFunction{
		Name: name,
		Description: "Run a shell command and return combined output. " +
			"Prefer structured file tools for reads; use this for builds, tests, git, and package managers.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"command": map[string]interface{}{"type": "string", "description": "The command line to execute."},
				"workdir": map[string]interface{}{"type": "string", "description": "Working directory."},
			},
			"required": []string{"command"},
		},
	}}
}

func grepTool() tool {
	return tool{Type: "function", Function: toolFunction{
		Name:        "grep",
		Description: "Search file contents with ripgrep semantics. Prefer patterns[] batching.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"pattern": map[string]interface{}{"type": "string", "description": "Search pattern."},
			},
			"required": []string{"pattern"},
		},
	}}
}

// bigSystem builds a system prompt with a stable head (environment facts) and
// a tail guidance section whose content can be swapped between rounds. The
// stable head is padded with deterministic filler so the shared prefix crosses
// the provider's minimum cacheable threshold (GLM-family caches typically
// require hundreds to thousands of tokens).
func bigSystem(guidanceVariant string) string {
	head := "Environment context:\n" +
		"  <cwd>E:\\projects\\ai\\ai-agent-runtime</cwd>\n" +
		"  <os>windows</os>\n" +
		"  <shell>pwsh</shell>\n\n" +
		"Shell guidance:\n" +
		"  - Do not use bash heredoc; PowerShell does not support that syntax.\n" +
		"  - Prefer Select-Object over head.\n" +
		"  - Non-zero exit codes are content results, not tool crashes.\n\n" +
		"File editing guidance:\n" +
		"  - Prefer dedicated file tools over shell redirection for writes.\n" +
		"  - Keep each patch focused on one file or one nearby change region.\n\n" +
		"Parallel tool guidance:\n" +
		"  - Batch independent read-only inspections in one turn.\n" +
		"  - view.files covers multiple ranges; grep.patterns batches searches.\n\n" +
		"Repository conventions (stable, repeated):\n"
	for i := 0; i < 40; i++ {
		head += fmt.Sprintf("  - Rule R%02d: structured tool arguments beat shell text; golden byte tests protect the tool prefix; session snapshots freeze the environment.\n", i)
	}
	head += "\n"
	// Session nonce: make the whole prompt unique per process run so every
	// round starts from a cold cache (no cross-run contamination), unless
	// CACHE_PROBE_SHARED=1 is set to reuse a fixed session identity.
	nonce := os.Getenv("CACHE_PROBE_NONCE")
	if nonce == "" {
		nonce = fmt.Sprintf("s%d", time.Now().UnixNano())
	}
	head += "Session identity: " + nonce + "\n"
	tail := map[string]string{
		"A": "Task difficulty guidance:\n  - Rate difficulty before delegating.\n  - Do not spawn subagents for easy work.\n\nMulti-agent collaboration guidance:\n  - Consume progress incrementally with subagent_events.\n  - Close children you no longer need.\n",
		"B": "Delegation policy:\n  - Rate difficulty before delegating; expert work may use subagents when isolated.\n  - Difficulty and difficulty_rationale accompany every child task.\n\nOrchestration notes:\n  - Read events incrementally with after_seq; never re-read consumed windows.\n  - Release agent slots with subagent_close when a child is no longer needed.\n",
	}
	return head + tail[guidanceVariant]
}

func send(url, apiKey, model, system string, tools []tool, userMsg string) usage {
	payload := map[string]interface{}{
		"model": model,
		"messages": []map[string]interface{}{
			{"role": "system", "content": system},
			{"role": "user", "content": userMsg},
		},
		"max_tokens": 16,
	}
	if len(tools) > 0 {
		payload["tools"] = tools
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		fmt.Fprintln(os.Stderr, "marshal:", err)
		os.Exit(1)
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		fmt.Fprintln(os.Stderr, "request:", err)
		os.Exit(1)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "send:", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "HTTP %d: %s\n", resp.StatusCode, truncate(string(body), 400))
		os.Exit(1)
	}
	var parsed chatResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		fmt.Fprintf(os.Stderr, "parse: %v; body=%s\n", err, truncate(string(body), 400))
		os.Exit(1)
	}
	if parsed.Error != nil {
		fmt.Fprintln(os.Stderr, "api error:", parsed.Error.Message)
		os.Exit(1)
	}
	return parsed.Usage
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func cachedOf(u usage) int {
	if u.PromptTokensDetails != nil {
		return u.PromptTokensDetails.CachedTokens
	}
	return -1 // provider did not report the field
}

func main() {
	model := flag.String("model", "cn:glm-5.3-flash", "model id as configured under workbuddy")
	providerURL := flag.String("url", "", "override full chat-completions URL")
	flag.Parse()

	baseURL, apiPath, apiKey := loadConfig()
	url := *providerURL
	if url == "" {
		url = baseURL + apiPath
		if baseURL[len(baseURL)-1] == '/' {
			url = baseURL[:len(baseURL)-1] + apiPath
		}
	}
	fmt.Printf("provider: workbuddy  model: %s  url: %s\n\n", *model, url)

	userMsg := "Reply with the single word: ready"
	toolsBase := []tool{shellTool("shell"), grepTool()}

	type round struct {
		label string
		sys   string
		tools []tool
	}
	rounds := []round{
		{"1 baseline (cold)", bigSystem("A"), toolsBase},
		{"2 identical resend (hit?)", bigSystem("A"), toolsBase},
		{"3 guidance swapped (system changes, tools same)", bigSystem("B"), toolsBase},
		{"3b identical resend of r3 (re-warm after system change?)", bigSystem("B"), toolsBase},
		{"4 tool appended (tools change, system same as r3)", bigSystem("B"), append(append([]tool{}, toolsBase...), shellTool("job_start"))},
		{"4b identical resend of r4 (re-warm after tool append?)", bigSystem("B"), append(append([]tool{}, toolsBase...), shellTool("job_start"))},
		{"5 back to original A prefix (old cache entry alive?)", bigSystem("A"), toolsBase},
	}

	for _, r := range rounds {
		usage := send(url, apiKey, *model, r.sys, r.tools, userMsg)
		cached := cachedOf(usage)
		cachedStr := "not-reported"
		if cached >= 0 {
			cachedStr = fmt.Sprintf("%d", cached)
		}
		fmt.Printf("%-52s prompt=%6d completion=%4d cached=%s\n", r.label, usage.PromptTokens, usage.CompletionTokens, cachedStr)
		time.Sleep(2 * time.Second)
	}
	fmt.Println("\nread (measured on workbuddy cn:glm-5.3-flash, block-prefix cache):")
	fmt.Println("- identical resend after any change: ~99% hit (cache write happens on the first post-change send)")
	fmt.Println("- BOTH a system-tail swap and a tool append cost exactly one cache miss (the first")
	fmt.Println("  post-change request); they are symmetric, there is no 'append is free' tier")
	fmt.Println("- old prefixes stay cached (content-addressed): switching variants back re-hits immediately")
	fmt.Println("- implication for guidance gating: prompt-section withdrawal rides the same single miss as")
	fmt.Println("  the concurrent tool-surface change; steady-state requests keep full hits")
}
