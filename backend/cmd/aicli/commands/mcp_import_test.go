package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
	mcpadmin "github.com/wwsheng009/ai-agent-runtime/internal/mcp/admin"
	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
)

func writeMCPImportFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func importFixtureClaude(t *testing.T, home string) {
	t.Helper()
	writeMCPImportFile(t, filepath.Join(home, ".claude.json"), `{
  "mcpServers": {
    "context7": {"type": "http", "url": "https://mcp.context7.com/mcp",
                 "headers": {"Authorization": "Bearer sk-live-context7"}},
    "files": {"type": "stdio", "command": "npx", "args": ["-y", "server-filesystem", "/data"],
              "env": {"FS_ROOT": "/data"}}
  }
}`)
}

func TestRunMCPImportCommandDryRunThenApplyLocalScope(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	isolateMCPCommandHome(t, home)
	project := t.TempDir()
	t.Chdir(project)
	importFixtureClaude(t, home)

	previousConfigFile := mcpConfigFile
	mcpConfigFile = ""
	t.Cleanup(func() { mcpConfigFile = previousConfigFile })

	preview, err := runMCPImportCommand(mcpImportOptions{
		From: "claude", Scope: mcpWriteScopeLocal, DryRun: true,
	})
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if preview.Imported != 2 || preview.Failed != 0 {
		t.Fatalf("预览计数错误: %#v", preview)
	}
	localPath := filepath.Join(home, ".aicli", "projects", aiclipaths.ProjectScopeSlug(project), "mcp.yaml")
	if _, statErr := os.Stat(localPath); statErr == nil {
		t.Fatalf("--dry-run 不应写文件: %s", localPath)
	}

	applied, err := runMCPImportCommand(mcpImportOptions{
		From: "claude", Scope: mcpWriteScopeLocal,
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if applied.Imported != 2 {
		t.Fatalf("应用计数错误: %#v", applied)
	}
	content := readFileForTest(t, localPath)
	if !strings.Contains(content, "context7") || !strings.Contains(content, "server-filesystem") {
		t.Fatalf("local 层应包含导入的 server:\n%s", content)
	}
	// local 是个人层，明文凭证原样保留（只对可提交的 project 层做剥离）。
	if !strings.Contains(content, "sk-live-context7") {
		t.Fatalf("local 层不应改写凭证:\n%s", content)
	}

	// 导入结果能直接被分层加载看到。
	statuses := loadMCPStatusNamesForTest(t, localPath)
	if _, ok := statuses["context7"]; !ok {
		t.Fatalf("导入后应能在配置中看到 context7: %#v", statuses)
	}
}

func TestRunMCPImportCommandProjectScopeMasksSecrets(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	isolateMCPCommandHome(t, home)
	project := t.TempDir()
	t.Chdir(project)
	importFixtureClaude(t, home)

	previousConfigFile := mcpConfigFile
	mcpConfigFile = ""
	t.Cleanup(func() { mcpConfigFile = previousConfigFile })

	plan, err := runMCPImportCommand(mcpImportOptions{
		From: "claude", Scope: mcpWriteScopeProject,
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if plan.Imported != 2 {
		t.Fatalf("plan = %#v", plan)
	}
	projectFile := filepath.Join(project, ".aicli", "mcp.yaml")
	content := readFileForTest(t, projectFile)
	if strings.Contains(content, "sk-live-context7") {
		t.Fatalf("项目级不应落明文凭证:\n%s", content)
	}
	if !strings.Contains(content, "${CONTEXT7_AUTHORIZATION}") {
		t.Fatalf("项目级应改写为 ${VAR} 引用:\n%s", content)
	}
	if len(plan.EnvHintVars) != 1 || plan.EnvHintVars[0] != "CONTEXT7_AUTHORIZATION" {
		t.Fatalf("应提示需设置的变量: %#v", plan.EnvHintVars)
	}

	// reject 策略：含明文凭证的 server 不写入，并在摘要里标出。
	home2 := filepath.Join(t.TempDir(), "home2")
	isolateMCPCommandHome(t, home2)
	project2 := t.TempDir()
	t.Chdir(project2)
	importFixtureClaude(t, home2)

	rejected, err := runMCPImportCommand(mcpImportOptions{
		From: "claude", Scope: mcpWriteScopeProject, OnSecrets: mcpImportSecretsReject,
	})
	if err != nil {
		t.Fatalf("import reject: %v", err)
	}
	if rejected.Rejected != 1 || rejected.Imported != 1 {
		t.Fatalf("reject 计数错误: %#v", rejected)
	}
	content2 := readFileForTest(t, filepath.Join(project2, ".aicli", "mcp.yaml"))
	if !strings.Contains(content2, "files") || strings.Contains(content2, "context7") {
		t.Fatalf("reject 应只写入无凭证的 server:\n%s", content2)
	}
}

func TestRunMCPImportCommandConflictPolicies(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	isolateMCPCommandHome(t, home)
	project := t.TempDir()
	t.Chdir(project)
	importFixtureClaude(t, home)

	previousConfigFile := mcpConfigFile
	mcpConfigFile = ""
	t.Cleanup(func() { mcpConfigFile = previousConfigFile })

	// 目标文件先占住 context7。
	selfPath := filepath.Join(home, ".aicli", "mcp.yaml")
	writeMCPImportFile(t, selfPath, `mcpServers:
  context7:
    type: streamable
    url: https://existing.example.com/mcp
`)

	skipped, err := runMCPImportCommand(mcpImportOptions{From: "claude", Scope: mcpWriteScopeUser})
	if err != nil {
		t.Fatalf("skip: %v", err)
	}
	if skipped.Skipped != 1 || skipped.Imported != 1 {
		t.Fatalf("skip 计数错误: %#v", skipped)
	}
	if !strings.Contains(readFileForTest(t, selfPath), "existing.example.com") {
		t.Fatalf("skip 不应覆盖已有定义:\n%s", readFileForTest(t, selfPath))
	}

	renamed, err := runMCPImportCommand(mcpImportOptions{
		From: "claude", Scope: mcpWriteScopeUser, OnConflict: mcpImportConflictRename,
	})
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	// 上一轮 skip 已把 files 写进目标文件，因此这一轮 context7 与 files 都属冲突。
	if renamed.Renamed != 2 {
		t.Fatalf("rename 计数错误: %#v", renamed)
	}
	content := readFileForTest(t, selfPath)
	if !strings.Contains(content, "context7-imported") || !strings.Contains(content, "files-imported") {
		t.Fatalf("rename 应写入新名字:\n%s", content)
	}

	overwritten, err := runMCPImportCommand(mcpImportOptions{
		From: "claude", Scope: mcpWriteScopeUser, OnConflict: mcpImportConflictOverwrite,
	})
	if err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	if overwritten.Imported != 2 {
		t.Fatalf("overwrite 计数错误: %#v", overwritten)
	}
	content = readFileForTest(t, selfPath)
	if !strings.Contains(content, "mcp.context7.com") {
		t.Fatalf("overwrite 应替换为导入内容:\n%s", content)
	}
}

func TestRunMCPImportCommandValidatesFlags(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	isolateMCPCommandHome(t, home)
	t.Chdir(t.TempDir())

	previousConfigFile := mcpConfigFile
	mcpConfigFile = ""
	t.Cleanup(func() { mcpConfigFile = previousConfigFile })

	if _, err := runMCPImportCommand(mcpImportOptions{From: "nope"}); err == nil ||
		!strings.Contains(err.Error(), "claude") {
		t.Fatalf("非法来源错误: %v", err)
	}
	if _, err := runMCPImportCommand(mcpImportOptions{From: "claude", OnConflict: "merge"}); err == nil ||
		!strings.Contains(err.Error(), "skip, overwrite, rename") {
		t.Fatalf("非法冲突策略错误: %v", err)
	}
	if _, err := runMCPImportCommand(mcpImportOptions{From: "claude", OnSecrets: "unknown"}); err == nil ||
		!strings.Contains(err.Error(), "mask, reject, keep") {
		t.Fatalf("非法秘密策略错误: %v", err)
	}
	// --only 过滤未命中：不报错，只是什么都不导入。
	plan, err := runMCPImportCommand(mcpImportOptions{
		From: "claude", Scope: mcpWriteScopeLocal, DryRun: true, Only: []string{"missing"},
	})
	if err != nil {
		t.Fatalf("only: %v", err)
	}
	if plan.Imported != 0 || len(plan.Items) != 0 {
		t.Fatalf("--only 未命中的结果: %#v", plan)
	}
}

// 同一段明文凭证出现在多个 server 时复用同一个变量名，避免让用户设两遍。
func TestSecretMaskerReusesVariableForSameValue(t *testing.T) {
	masker := newSecretMasker()
	first := mcpconfig.MCPConfig{Headers: map[string]string{"Authorization": "shared-token"}}
	second := mcpconfig.MCPConfig{Env: map[string]string{"API_TOKEN": "shared-token"}}

	varsFirst := masker.mask("alpha", &first)
	varsSecond := masker.mask("beta", &second)
	if len(varsFirst) != 1 || len(varsSecond) != 1 || varsFirst[0] != varsSecond[0] {
		t.Fatalf("相同明文应复用变量名: %#v vs %#v", varsFirst, varsSecond)
	}
	if !strings.Contains(first.Headers["Authorization"], "${"+varsFirst[0]+"}") {
		t.Fatalf("header 应改为引用: %#v", first.Headers)
	}
	if !strings.Contains(second.Env["API_TOKEN"], "${"+varsFirst[0]+"}") {
		t.Fatalf("env 应改为引用: %#v", second.Env)
	}
}

func TestParseMCPAddJSONRequest(t *testing.T) {
	request, _, err := parseMCPAddJSONRequest("context7",
		`{"type":"http","url":"https://mcp.context7.com/mcp","headers":{"Authorization":"Bearer ${T}"},"timeoutSeconds":45,"enabled":false}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if request.Type != "streamable" || request.URL == "" {
		t.Fatalf("type 别名应归一化: %#v", request)
	}
	if request.TimeoutSeconds == nil || *request.TimeoutSeconds != 45 {
		t.Fatalf("timeoutSeconds 未解析: %#v", request.TimeoutSeconds)
	}
	if request.Enabled == nil || *request.Enabled {
		t.Fatalf("enabled=false 未解析: %#v", request.Enabled)
	}

	// 接受 `mcp get --json` 的 .config 片段。
	wrapped, _, err := parseMCPAddJSONRequest("files",
		`{"config":{"command":"npx","args":["-y","pkg"],"env":{"A":"1"}}}`)
	if err != nil {
		t.Fatalf("parse wrapped: %v", err)
	}
	if wrapped.Command != "npx" || len(wrapped.Args) != 2 || wrapped.Env["A"] != "1" {
		t.Fatalf("config 片段未解析: %#v", wrapped)
	}
	// 未声明 type 时按 url/command 推断，否则 admin 会按 stdio 要求 command。
	if wrapped.Type != "stdio" {
		t.Fatalf("仅 command 应推断为 stdio: %q", wrapped.Type)
	}
	urlOnly, _, err := parseMCPAddJSONRequest("remote-only", `{"url":"https://x.example.com/mcp"}`)
	if err != nil {
		t.Fatalf("parse url-only: %v", err)
	}
	if urlOnly.Type != "streamable" {
		t.Fatalf("仅 url 应推断为 streamable: %q", urlOnly.Type)
	}

	// 缺 url/command 报错；非法 JSON 报错。
	if _, _, err := parseMCPAddJSONRequest("empty", `{"type":"http"}`); err == nil ||
		!strings.Contains(err.Error(), "command") {
		t.Fatalf("缺少 url/command 应报错: %v", err)
	}
	if _, _, err := parseMCPAddJSONRequest("bad", `{`); err == nil {
		t.Fatal("非法 JSON 应报错")
	}

	// disabled: true 等价于 enabled=false。
	disabled, _, err := parseMCPAddJSONRequest("d", `{"command":"x","disabled":true}`)
	if err != nil {
		t.Fatalf("parse disabled: %v", err)
	}
	if disabled.Enabled == nil || *disabled.Enabled {
		t.Fatalf("disabled=true 应映射为 enabled=false: %#v", disabled.Enabled)
	}
}

// `mcp get --json` 的输出必须能原样喂回 add-json（跨机复制路径）。
func TestMCPGetJSONRoundTripsIntoAddJSON(t *testing.T) {
	server := mcpconfig.MCPConfig{
		Name:    "context7",
		Type:    "streamable",
		URL:     "https://mcp.context7.com/mcp",
		Headers: map[string]string{"Authorization": "Bearer ${CONTEXT7_TOKEN}"},
		Enabled: true,
	}
	payload, err := json.Marshal(map[string]interface{}{
		"name":         "context7",
		"config":       server,
		"configSource": "user",
		"configPath":   "/home/u/.aicli/mcp.yaml",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	request, _, err := parseMCPAddJSONRequest("context7", string(payload))
	if err != nil {
		t.Fatalf("round-trip parse: %v", err)
	}
	if request.URL != server.URL || request.Type != "streamable" {
		t.Fatalf("round-trip 丢失字段: %#v", request)
	}
	if request.Headers["Authorization"] != "Bearer ${CONTEXT7_TOKEN}" {
		t.Fatalf("round-trip 丢失 header: %#v", request.Headers)
	}
}

func TestRunMCPAddJSONCommandProjectScopeRejectsPlaintext(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	isolateMCPCommandHome(t, home)
	project := t.TempDir()
	t.Chdir(project)

	previousConfigFile := mcpConfigFile
	mcpConfigFile = ""
	t.Cleanup(func() { mcpConfigFile = previousConfigFile })

	request, _, err := parseMCPAddJSONRequest("team",
		`{"url":"https://team.example.com/mcp","headers":{"Authorization":"Bearer sk-live"}}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, err := runMCPAddJSONCommand("team", request, mcpWriteScopeProject); err == nil ||
		!strings.Contains(err.Error(), "明文凭证") {
		t.Fatalf("项目级应拒绝明文凭证: %v", err)
	}

	request.Headers["Authorization"] = "Bearer ${TEAM_TOKEN}"
	result, err := runMCPAddJSONCommand("team", request, mcpWriteScopeProject)
	if err != nil {
		t.Fatalf("引用式凭证应可写入: %v", err)
	}
	if !strings.Contains(readFileForTest(t, result.ConfigPath), "${TEAM_TOKEN}") {
		t.Fatalf("应保留引用:\n%s", readFileForTest(t, result.ConfigPath))
	}
}

func readFileForTest(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// loadMCPStatusNamesForTest 用 admin 读回单文件，确认导入结果确实可被解析。
func loadMCPStatusNamesForTest(t *testing.T, path string) map[string]struct{} {
	t.Helper()
	cfg, err := mcpadmin.LoadFile(path)
	if err != nil {
		t.Fatalf("load %s: %v", path, err)
	}
	names := make(map[string]struct{}, len(cfg.MCPServers))
	for name := range cfg.MCPServers {
		names[name] = struct{}{}
	}
	return names
}

// ---- M6：JSON 输入（add-json @file / import --from json） ----

func TestResolveMCPAddJSONInput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notion.json")
	writeMCPImportFile(t, path, "\uFEFF"+`{"url":"https://mcp.notion.com/mcp"}`+"\n")

	inline, err := resolveMCPAddJSONInput(`  {"url":"https://x.example.com/mcp"}  `, false)
	if err != nil || inline != `{"url":"https://x.example.com/mcp"}` {
		t.Fatalf("内联 JSON 应原样返回: %q, %v", inline, err)
	}

	fromFile, err := resolveMCPAddJSONInput("@"+path, false)
	if err != nil {
		t.Fatalf("@file: %v", err)
	}
	if !strings.HasPrefix(fromFile, `{"url":"https://mcp.notion.com/mcp"}`) {
		t.Fatalf("@file 应剥掉 BOM 并 trim: %q", fromFile)
	}

	if _, err := resolveMCPAddJSONInput("", false); err == nil || !strings.Contains(err.Error(), "不能为空") {
		t.Fatalf("空输入应报错: %v", err)
	}
	if _, err := resolveMCPAddJSONInput("@", false); err == nil || !strings.Contains(err.Error(), "文件路径") {
		t.Fatalf("@ 后缺路径应报错: %v", err)
	}
	if _, err := resolveMCPAddJSONInput("@"+filepath.Join(dir, "missing.json"), false); err == nil ||
		!strings.Contains(err.Error(), "读取 JSON 文件失败") {
		t.Fatalf("文件不存在应报错: %v", err)
	}
	// chat 不能读 stdin：`-` 必须被拒绝。
	if _, err := resolveMCPAddJSONInput("-", false); err == nil || !strings.Contains(err.Error(), "stdin") {
		t.Fatalf("chat 上下文应拒绝 stdin: %v", err)
	}
}

func TestResolveMCPAddJSONInputFromStdin(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "stdin-*.json")
	if err != nil {
		t.Fatalf("temp: %v", err)
	}
	if _, err := file.WriteString(`{"command":"npx","args":["-y","pkg"]}`); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		t.Fatalf("seek: %v", err)
	}
	previous := os.Stdin
	os.Stdin = file
	t.Cleanup(func() {
		os.Stdin = previous
		_ = file.Close()
	})

	input, err := resolveMCPAddJSONInput("-", true)
	if err != nil {
		t.Fatalf("stdin: %v", err)
	}
	if input != `{"command":"npx","args":["-y","pkg"]}` {
		t.Fatalf("stdin 内容 = %q", input)
	}
}

func TestReadLimitedRejectsOversize(t *testing.T) {
	if _, err := readLimited(strings.NewReader(strings.Repeat("x", 32)), 16); err == nil ||
		!strings.Contains(err.Error(), "上限") {
		t.Fatalf("超限应报错: %v", err)
	}
}

func TestResolveMCPImportFilePath(t *testing.T) {
	if got, err := resolveMCPImportFilePath(" a.json ", ""); err != nil || got != "a.json" {
		t.Fatalf("位置参数优先: %q, %v", got, err)
	}
	if got, err := resolveMCPImportFilePath("", " b.json "); err != nil || got != "b.json" {
		t.Fatalf("--file 生效: %q, %v", got, err)
	}
	if got, err := resolveMCPImportFilePath("same.json", "same.json"); err != nil || got != "same.json" {
		t.Fatalf("两者相同应通过: %q, %v", got, err)
	}
	if _, err := resolveMCPImportFilePath("a.json", "b.json"); err == nil || !strings.Contains(err.Error(), "不一致") {
		t.Fatalf("两者不一致应报错: %v", err)
	}
}

func TestRunMCPImportCommandFromJSONFile(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	isolateMCPCommandHome(t, home)
	project := t.TempDir()
	t.Chdir(project)

	previousConfigFile := mcpConfigFile
	mcpConfigFile = ""
	t.Cleanup(func() { mcpConfigFile = previousConfigFile })

	jsonPath := filepath.Join(project, "mcp.json")
	writeMCPImportFile(t, jsonPath, `{"mcpServers":{
	  "context7": {"type":"http","url":"https://mcp.context7.com/mcp"},
	  "local-fs": {"command":"npx","args":["-y","server-filesystem","/data"]}
	}}`)

	// 缺路径：必须在最前面报错，而不是静默「扫到 0 个」。
	if _, err := runMCPImportCommand(mcpImportOptions{From: "json", Scope: mcpWriteScopeUser}); err == nil ||
		!strings.Contains(err.Error(), "--file") {
		t.Fatalf("缺少 JSON 路径应报错: %v", err)
	}
	// 路径不存在：显式点名的文件必须存在。
	if _, err := runMCPImportCommand(mcpImportOptions{
		From: "json", Scope: mcpWriteScopeUser, File: filepath.Join(project, "nope.json"),
	}); err == nil || !strings.Contains(err.Error(), "读取 JSON 文件失败") {
		t.Fatalf("不存在的文件应报错: %v", err)
	}
	// --file 只属于 --from json。
	if _, err := runMCPImportCommand(mcpImportOptions{
		From: "claude", Scope: mcpWriteScopeUser, File: jsonPath,
	}); err == nil || !strings.Contains(err.Error(), "仅适用于 --from json") {
		t.Fatalf("非 json 来源带 --file 应报错: %v", err)
	}

	target := filepath.Join(home, ".aicli", "mcp.yaml")
	preview, err := runMCPImportCommand(mcpImportOptions{
		From: "json", Scope: mcpWriteScopeUser, DryRun: true, File: jsonPath,
	})
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if preview.Imported != 2 || preview.Failed != 0 || preview.DryRun != true {
		t.Fatalf("预览计数错误: %#v", preview)
	}
	if _, statErr := os.Stat(target); statErr == nil {
		t.Fatalf("--dry-run 不应写文件: %s", target)
	}

	applied, err := runMCPImportCommand(mcpImportOptions{
		From: "json", Scope: mcpWriteScopeUser, File: jsonPath, Only: []string{"context7"},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if applied.Imported != 1 {
		t.Fatalf("--only 过滤后应导入 1 个: %#v", applied)
	}
	content := readFileForTest(t, target)
	if !strings.Contains(content, "context7") || strings.Contains(content, "server-filesystem") {
		t.Fatalf("落盘内容不符:\n%s", content)
	}
}
