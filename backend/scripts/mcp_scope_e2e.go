// Real end-to-end verification of MCP layered config scopes（M3，见
// docs/analysis/commandcode-mcp-design-borrowing-20260925.md §4.5）。
//
// 脚本完全离线：隔离 HOME + 临时项目目录，用真实 aicli 二进制验证
// 「用户级基础 + 项目级同名覆盖」的合并语义、来源展示、--scope 写入目标
// 与项目级明文凭证拦截。
//
// 断言：
//
//  1. 用户级 mcp.yaml 提供基础 server，项目级同名 server 整体覆盖；
//     `mcp list` 显示「来源」与「覆盖」两行；
//  2. `mcp list --output json` 带 configSource/configPath/shadowedSources；
//  3. `--scope project` 拒绝明文凭证（并提示改用 ${VAR} 或 --scope user），
//     `${VAR}` 引用式凭证可写入项目文件且保留字面量；
//  4. `mcp remove <项目级 server>` 只删项目级定义，用户级定义随即重新生效；
//  5. `mcp disable <用户级 server>` 写回用户级文件（不会误写项目级）。
//
// Run (from backend/):
//
//	go run ./scripts/mcp_scope_e2e.go [aicli.exe]
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
	fmt.Println("\n✅ MCP 分层配置（scope）e2e 全部通过")
}

func run() error {
	binary, err := resolveAICLI()
	if err != nil {
		return err
	}

	root, err := os.MkdirTemp("", "aicli-mcp-scope-e2e-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(root) }()

	home := filepath.Join(root, "home")
	project := filepath.Join(root, "project")
	for _, dir := range []string{filepath.Join(home, ".aicli"), filepath.Join(project, ".aicli")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	userConfig := filepath.Join(home, ".aicli", "mcp.yaml")
	projectConfig := filepath.Join(project, ".aicli", "mcp.yaml")

	if err := os.WriteFile(userConfig, []byte(`mcpServers:
  base:
    name: base
    type: streamable
    url: http://127.0.0.1:9/mcp
    timeout: 2s
  shared:
    name: shared
    type: streamable
    url: http://127.0.0.1:9/from-user
    timeout: 2s
global:
  connectTimeout: 2s
`), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(projectConfig, []byte(`mcpServers:
  shared:
    name: shared
    type: streamable
    url: http://127.0.0.1:9/from-project
    timeout: 2s
`), 0o644); err != nil {
		return err
	}

	env := hermeticEnv(home, root)

	// 1) 分层合并 + 来源展示。
	out, err := runCLI(env, project, binary, "mcp", "list")
	if err != nil {
		return fmt.Errorf("mcp list: %v\n%s", err, out)
	}
	for _, want := range []string{
		"来源: user (" + userConfig + ")",
		"来源: project (" + projectConfig + ")",
		"覆盖: user (" + userConfig + ")",
	} {
		if !strings.Contains(normalize(out), normalize(want)) {
			return fmt.Errorf("list 缺少 %q:\n%s", want, out)
		}
	}

	// 2) JSON 输出带来源与 shadow 关系。
	statuses, err := listStatuses(env, project, binary)
	if err != nil {
		return err
	}
	shared, ok := statuses["shared"]
	if !ok {
		return fmt.Errorf("json 缺少 shared: %#v", statuses)
	}
	if shared.ConfigSource != "project" || filepath.Clean(shared.ConfigPath) != filepath.Clean(projectConfig) {
		return fmt.Errorf("shared 来源错误: %#v", shared)
	}
	if len(shared.ShadowedSources) != 1 || shared.ShadowedSources[0].Source != "user" {
		return fmt.Errorf("shared 缺少 shadow 关系: %#v", shared.ShadowedSources)
	}

	// 3) 项目级明文凭证被拒；引用式允许。
	out, err = runCLI(env, project, binary, "mcp", "add", "team", "http://127.0.0.1:9/mcp",
		"--scope", "project", "--header", "Authorization: Bearer sk-live-123")
	if err == nil {
		return fmt.Errorf("项目级明文凭证应被拒绝:\n%s", out)
	}
	if !strings.Contains(out, "明文凭证") || !strings.Contains(out, "--scope user") {
		return fmt.Errorf("拒绝信息缺少可行动提示:\n%s", out)
	}
	if strings.Contains(readFile(projectConfig), "sk-live-123") {
		return fmt.Errorf("被拒绝的凭证不应落盘:\n%s", readFile(projectConfig))
	}

	out, err = runCLI(env, project, binary, "mcp", "add", "team", "http://127.0.0.1:9/mcp",
		"--scope", "project", "--header", "Authorization: Bearer ${TEAM_TOKEN}")
	if err != nil {
		return fmt.Errorf("引用式凭证应可写入: %v\n%s", err, out)
	}
	projectText := readFile(projectConfig)
	if !strings.Contains(projectText, "${TEAM_TOKEN}") {
		return fmt.Errorf("项目文件应保留 ${VAR} 字面量:\n%s", projectText)
	}
	if !strings.Contains(projectText, "team") {
		return fmt.Errorf("项目文件应包含新增 server:\n%s", projectText)
	}

	// 4) 删除项目级定义后用户级定义重新生效。
	out, err = runCLI(env, project, binary, "mcp", "remove", "shared")
	if err != nil {
		return fmt.Errorf("mcp remove: %v\n%s", err, out)
	}
	statuses, err = listStatuses(env, project, binary)
	if err != nil {
		return err
	}
	shared, ok = statuses["shared"]
	if !ok {
		return fmt.Errorf("删除后 shared 不应消失（用户级仍有定义）: %#v", statuses)
	}
	if shared.ConfigSource != "user" || filepath.Clean(shared.ConfigPath) != filepath.Clean(userConfig) {
		return fmt.Errorf("用户级定义应重新生效: %#v", shared)
	}
	if strings.Contains(readFile(projectConfig), "shared") {
		return fmt.Errorf("项目级 shared 应已删除:\n%s", readFile(projectConfig))
	}

	// 5) disable 写在定义该 server 的文件（用户级），不动项目级。
	beforeProject := readFile(projectConfig)
	if out, err = runCLI(env, project, binary, "mcp", "disable", "base"); err != nil {
		return fmt.Errorf("mcp disable: %v\n%s", err, out)
	}
	if !strings.Contains(readFile(userConfig), "disabled: true") {
		return fmt.Errorf("base 应写入用户级文件:\n%s", readFile(userConfig))
	}
	if readFile(projectConfig) != beforeProject {
		return fmt.Errorf("项目级文件不应被修改:\n%s", readFile(projectConfig))
	}
	statuses, err = listStatuses(env, project, binary)
	if err != nil {
		return err
	}
	if base, ok := statuses["base"]; !ok || base.Enabled {
		return fmt.Errorf("base 应显示为已停用: %#v", base)
	}

	// 6) local 层（项目私有）：写入后必须真正进入发现链，且优先级高于 project/user。
	if out, err = runCLI(env, project, binary, "mcp", "add", "private-tool", "http://127.0.0.1:9/mcp",
		"--scope", "local"); err != nil {
		return fmt.Errorf("mcp add --scope local: %v\n%s", err, out)
	}
	localConfig := filepath.Join(home, ".aicli", "projects", projectSlugForScript(project), "mcp.yaml")
	if !strings.Contains(readFile(localConfig), "private-tool") {
		return fmt.Errorf("local 层文件缺少新增 server（%s）", localConfig)
	}
	statuses, err = listStatuses(env, project, binary)
	if err != nil {
		return err
	}
	if item, ok := statuses["private-tool"]; !ok || item.ConfigSource != "local" {
		return fmt.Errorf("local 层未生效: %#v", item)
	}

	// local 覆盖同名 project/user 定义。
	if out, err = runCLI(env, project, binary, "mcp", "add", "shared", "http://127.0.0.1:9/from-local",
		"--scope", "local"); err != nil {
		return fmt.Errorf("mcp add --scope local（覆盖）: %v\n%s", err, out)
	}
	statuses, err = listStatuses(env, project, binary)
	if err != nil {
		return err
	}
	shared, ok = statuses["shared"]
	if !ok || shared.ConfigSource != "local" {
		return fmt.Errorf("local 应覆盖同名低优先级定义: %#v", shared)
	}
	if len(shared.ShadowedSources) == 0 || shared.ShadowedSources[0].Source != "user" {
		return fmt.Errorf("应记录被覆盖的用户级定义: %#v", shared.ShadowedSources)
	}

	// 不带 --scope 时沿用「命中即写」：local 文件存在时应写入 local。
	if out, err = runCLI(env, project, binary, "mcp", "add", "default-target", "http://127.0.0.1:9/mcp"); err != nil {
		return fmt.Errorf("mcp add（默认写入路径）: %v\n%s", err, out)
	}
	if !strings.Contains(readFile(localConfig), "default-target") {
		return fmt.Errorf("默认写入应命中已存在的 local 文件: %s", readFile(localConfig))
	}
	return nil
}

type statusItem struct {
	Name            string `json:"name"`
	Enabled         bool   `json:"enabled"`
	ConfigSource    string `json:"configSource"`
	ConfigPath      string `json:"configPath"`
	ShadowedSources []struct {
		Source string `json:"source"`
		Path   string `json:"path"`
	} `json:"shadowedSources"`
}

// listStatuses 读取 `mcp list --output json --envelope` 并按 server 名索引。
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

func hermeticEnv(home, root string) []string {
	env := make([]string, 0, len(os.Environ())+3)
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

func normalize(text string) string {
	return strings.ReplaceAll(filepath.ToSlash(strings.TrimSpace(text)), "\\", "/")
}

// jsonPayload 从可能混有日志的输出里截出 JSON 主体。
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

// projectSlugForScript 复刻 internal/aiclipaths.ProjectScopeSlug 的规则
// （脚本不引入内部包，避免 e2e 依赖被测代码本身）。
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
	target := filepath.Join(os.TempDir(), "aicli-mcp-scope-e2e-"+name)
	cmd := exec.Command("go", "build", "-o", target, "./cmd/aicli")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("go build ./cmd/aicli: %v\n%s", err, out)
	}
	return target, nil
}
