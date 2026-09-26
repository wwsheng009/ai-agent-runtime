// Real end-to-end verification of M4（跨 agent 导入 / 单 server 导出 / JSON 直填），
// 见 docs/analysis/commandcode-mcp-design-borrowing-20260925.md §4.6、§4.7。
//
// 断言：
//
//  1. `mcp import --dry-run` 只预览：输出计数正确、目标文件不被创建；
//  2. `mcp import --from claude --scope local` 写入项目私有层，`mcp list` 显示 来源: local；
//  3. `mcp import --from codex --scope project` 把明文凭证改写成 ${VAR}，并打印需设置的变量名，
//     项目文件里不出现明文；
//  4. `mcp get <name> --json` 的输出可直接喂回 `mcp add-json`（跨机复制路径）；
//  5. `mcp add-json` 缺 url/command 时给出可行动报错，不写文件。
//
// Run (from backend/):
//
//	go run ./scripts/mcp_import_e2e.go [aicli.exe]
//
//go:build ignore

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("\n✅ MCP 导入/导出/JSON 直填 e2e 全部通过")
}

func run() error {
	binary, err := resolveAICLI()
	if err != nil {
		return err
	}
	root, err := os.MkdirTemp("", "aicli-mcp-import-e2e-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(root) }()

	home := filepath.Join(root, "home")
	project := filepath.Join(root, "project")
	for _, dir := range []string{filepath.Join(home, ".aicli"), filepath.Join(home, ".codex"), filepath.Join(project, ".aicli")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	// Claude 来源：一个带明文凭证的远程 server + 一个 stdio server。
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"mcpServers": {
  "context7": {"type": "http", "url": "https://mcp.context7.com/mcp",
               "headers": {"Authorization": "Bearer sk-live-e2e"}},
  "files": {"type": "stdio", "command": "npx", "args": ["-y", "server-filesystem", "/data"],
            "env": {"FS_ROOT": "/data"}}
}}`), 0o644); err != nil {
		return err
	}
	// Codex 来源：TOML 里的远程 server（明文 header）。
	if err := os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte(`[mcp_servers.codex-remote]
url = "https://codex.example.com/mcp"
http_headers = { "X-Api-Key" = "plain-codex-key" }
`), 0o644); err != nil {
		return err
	}

	env := hermeticEnv(home, root)
	localFile := filepath.Join(home, ".aicli", "projects", projectSlugForScript(project), "mcp.yaml")
	projectFile := filepath.Join(project, ".aicli", "mcp.yaml")
	userFile := filepath.Join(home, ".aicli", "mcp.yaml")

	// 1) --dry-run 不落盘。
	out, err := runCLI(env, project, binary, "mcp", "import", "--from", "claude", "--dry-run",
		"--scope", "local", "--output", "json", "--envelope")
	if err != nil {
		return fmt.Errorf("import --dry-run: %v\n%s", err, out)
	}
	var preview struct {
		Data struct {
			DryRun   bool `json:"dryRun"`
			Imported int  `json:"imported"`
			Failed   int  `json:"failed"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(jsonPayload(out)), &preview); err != nil {
		return fmt.Errorf("解析 dry-run json: %v\n%s", err, out)
	}
	if !preview.Data.DryRun || preview.Data.Imported != 2 || preview.Data.Failed != 0 {
		return fmt.Errorf("dry-run 摘要错误: %#v", preview.Data)
	}
	if _, statErr := os.Stat(localFile); statErr == nil {
		return fmt.Errorf("--dry-run 不应创建文件: %s", localFile)
	}

	// 2) 真正导入到 local 层，并在 list 里看到来源。
	if out, err = runCLI(env, project, binary, "mcp", "import", "--from", "claude", "--scope", "local"); err != nil {
		return fmt.Errorf("import --scope local: %v\n%s", err, out)
	}
	localText := readFile(localFile)
	if !strings.Contains(localText, "context7") || !strings.Contains(localText, "server-filesystem") {
		return fmt.Errorf("local 层内容不完整:\n%s", localText)
	}
	if !strings.Contains(localText, "sk-live-e2e") {
		return fmt.Errorf("local 是个人层，不应改写凭证:\n%s", localText)
	}
	statuses, err := listStatuses(env, project, binary)
	if err != nil {
		return err
	}
	if item, ok := statuses["context7"]; !ok || item.ConfigSource != "local" {
		return fmt.Errorf("导入后应显示 local 来源: %#v", item)
	}

	// 3) 项目级导入：明文凭证被 ${VAR} 化，且项目文件不含明文。
	out, err = runCLI(env, project, binary, "mcp", "import", "--from", "codex", "--scope", "project")
	if err != nil {
		return fmt.Errorf("import --scope project: %v\n%s", err, out)
	}
	projectText := readFile(projectFile)
	if strings.Contains(projectText, "plain-codex-key") {
		return fmt.Errorf("项目级不应落明文凭证:\n%s", projectText)
	}
	if !strings.Contains(projectText, "${CODEX_REMOTE_X_API_KEY}") {
		return fmt.Errorf("项目级应改写为 ${VAR}:\n%s", projectText)
	}
	if !strings.Contains(out, "CODEX_REMOTE_X_API_KEY") {
		return fmt.Errorf("应提示需要设置的环境变量:\n%s", out)
	}

	// 4) get --json → add-json 往返。
	out, err = runCLI(env, project, binary, "mcp", "get", "codex-remote", "--json", "--envelope")
	if err != nil {
		return fmt.Errorf("mcp get: %v\n%s", err, out)
	}
	var getPayload struct {
		Data struct {
			Name         string          `json:"name"`
			ConfigSource string          `json:"configSource"`
			Config       json.RawMessage `json:"config"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(jsonPayload(out)), &getPayload); err != nil {
		return fmt.Errorf("解析 get json: %v\n%s", err, out)
	}
	if getPayload.Data.Name != "codex-remote" || getPayload.Data.ConfigSource != "project" {
		return fmt.Errorf("get 结果错误: %#v", getPayload.Data)
	}
	if out, err = runCLI(env, project, binary, "mcp", "add-json", "codex-remote-copy",
		string(getPayload.Data.Config), "--scope", "user"); err != nil {
		return fmt.Errorf("add-json 往返: %v\n%s", err, out)
	}
	userText := readFile(userFile)
	if !strings.Contains(userText, "codex-remote-copy") || !strings.Contains(userText, "codex.example.com") {
		return fmt.Errorf("add-json 应写入用户级配置:\n%s", userText)
	}

	// 5) 非法输入：报错且不写文件。
	before := readFile(projectFile)
	out, err = runCLI(env, project, binary, "mcp", "add-json", "broken", `{"type":"http"}`, "--scope", "project")
	if err == nil {
		return fmt.Errorf("缺少 url/command 应失败:\n%s", out)
	}
	if !strings.Contains(out, "command") || !strings.Contains(out, "url") {
		return fmt.Errorf("报错应给出可行动提示:\n%s", out)
	}
	if readFile(projectFile) != before {
		return fmt.Errorf("失败后不应改动项目文件")
	}
	return nil
}

func hermeticEnv(home, root string) []string {
	env := make([]string, 0, len(os.Environ())+4)
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		switch strings.ToUpper(key) {
		case "HOME", "USERPROFILE", "HOMEDRIVE", "HOMEPATH", "MCP_CONFIG_FILE", "AICLI_MCP_TOKENS_FILE":
			continue
		}
		env = append(env, item)
	}
	return append(env,
		"HOME="+home,
		"USERPROFILE="+home,
		"HOMEDRIVE=",
		"HOMEPATH=",
		"AICLI_MCP_TOKENS_FILE="+filepath.Join(root, "mcp-tokens.json"),
	)
}

func jsonPayload(out string) string {
	trimmed := strings.TrimSpace(out)
	if start := strings.Index(trimmed, "{"); start >= 0 {
		if end := strings.LastIndex(trimmed, "}"); end > start {
			return trimmed[start : end+1]
		}
	}
	return trimmed
}

func readFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

// projectSlugForScript 复刻 internal/aiclipaths.ProjectScopeSlug 的规则。
func projectSlugForScript(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	path = strings.ReplaceAll(path, "\\", "/")
	var builder strings.Builder
	pendingDash := false
	for _, r := range strings.ToLower(path) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			builder.WriteRune(r)
			pendingDash = false
			continue
		}
		if builder.Len() > 0 && !pendingDash {
			builder.WriteByte('-')
			pendingDash = true
		}
	}
	return strings.Trim(builder.String(), "-")
}

type statusItem struct {
	Name         string `json:"name"`
	Enabled      bool   `json:"enabled"`
	ConfigSource string `json:"configSource"`
	ConfigPath   string `json:"configPath"`
}

func listStatuses(env []string, project, binary string) (map[string]statusItem, error) {
	out, err := runCLI(env, project, binary, "mcp", "list", "--output", "json", "--envelope")
	if err != nil {
		return nil, fmt.Errorf("mcp list --output json: %v\n%s", err, out)
	}
	var payload struct {
		Data []statusItem `json:"data"`
	}
	if err := json.Unmarshal([]byte(jsonPayload(out)), &payload); err != nil {
		return nil, fmt.Errorf("解析 list json: %v\n%s", err, out)
	}
	byName := make(map[string]statusItem, len(payload.Data))
	for _, item := range payload.Data {
		byName[item.Name] = item
	}
	return byName, nil
}

func runCLI(env []string, dir, binary string, args ...string) (string, error) {
	cmd := exec.Command(binary, args...)
	cmd.Env = env
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

func resolveAICLI() (string, error) {
	if len(os.Args) > 1 {
		return os.Args[1], nil
	}
	name := "aicli"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	target := filepath.Join(os.TempDir(), "aicli-mcp-import-e2e-"+name)
	cmd := exec.Command("go", "build", "-o", target, "./cmd/aicli")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("go build ./cmd/aicli: %v\n%s", err, out)
	}
	return target, nil
}
