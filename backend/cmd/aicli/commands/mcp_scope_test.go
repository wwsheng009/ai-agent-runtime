package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
	mcpadmin "github.com/wwsheng009/ai-agent-runtime/internal/mcp/admin"
	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
)

func isolateMCPCommandHome(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOMEDRIVE", "")
	t.Setenv("HOMEPATH", "")
}

func TestResolveMCPWritePathForScope(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	isolateMCPCommandHome(t, home)
	project := t.TempDir()
	t.Chdir(project)

	path, err := resolveMCPWritePathForScope(mcpWriteScopeUser)
	if err != nil {
		t.Fatalf("user scope: %v", err)
	}
	if want := filepath.Join(home, ".aicli", "mcp.yaml"); filepath.Clean(path) != filepath.Clean(want) {
		t.Fatalf("user scope = %q, want %q", path, want)
	}

	path, err = resolveMCPWritePathForScope(mcpWriteScopeProject)
	if err != nil {
		t.Fatalf("project scope: %v", err)
	}
	if want := filepath.Join(project, ".aicli", "mcp.yaml"); filepath.Clean(path) != filepath.Clean(want) {
		t.Fatalf("project scope = %q, want %q", path, want)
	}

	path, err = resolveMCPWritePathForScope(mcpWriteScopeLocal)
	if err != nil {
		t.Fatalf("local scope: %v", err)
	}
	slug := aiclipaths.ProjectScopeSlug(project)
	if slug == "" {
		t.Fatalf("临时项目路径应能生成 slug: %q", project)
	}
	if want := filepath.Join(home, ".aicli", "projects", slug, "mcp.yaml"); filepath.Clean(path) != filepath.Clean(want) {
		t.Fatalf("local scope = %q, want %q", path, want)
	}

	// 大小写不敏感；非法取值给出可行动错误。
	if _, err := resolveMCPWritePathForScope("PROJECT"); err != nil {
		t.Fatalf("scope 应大小写不敏感: %v", err)
	}
	if _, err := resolveMCPWritePathForScope("global"); err == nil || !strings.Contains(err.Error(), "user, local, project") {
		t.Fatalf("非法 scope 错误 = %v", err)
	}
}

func TestValidateProjectScopeSecrets(t *testing.T) {
	cases := []struct {
		name    string
		request mcpadmin.UpsertRequest
		wantErr bool
		field   string
	}{
		{
			name:    "明文 bearer 头被拒",
			request: mcpadmin.UpsertRequest{Headers: map[string]string{"Authorization": "Bearer sk-live-123"}},
			wantErr: true,
			field:   "header Authorization",
		},
		{
			name:    "引用式 bearer 头允许",
			request: mcpadmin.UpsertRequest{Headers: map[string]string{"Authorization": "Bearer ${MY_TOKEN}"}},
		},
		{
			name:    "明文 api key 被拒",
			request: mcpadmin.UpsertRequest{Headers: map[string]string{"X-Api-Key": "abc123"}},
			wantErr: true,
			field:   "header X-Api-Key",
		},
		{
			name:    "带默认值的引用允许",
			request: mcpadmin.UpsertRequest{Headers: map[string]string{"x-api-key": "${KEY:-}"}},
		},
		{
			name:    "明文 token 型 env 被拒",
			request: mcpadmin.UpsertRequest{Env: map[string]string{"GITHUB_TOKEN": "ghp_secret"}},
			wantErr: true,
			field:   "env GITHUB_TOKEN",
		},
		{
			name:    "普通 env 允许",
			request: mcpadmin.UpsertRequest{Env: map[string]string{"FS_ROOT": "/data"}},
		},
		{
			name:    "oauth client secret 被拒",
			request: mcpadmin.UpsertRequest{Auth: &mcpconfig.MCPAuthConfig{Type: "oauth", ClientSecret: "s3cr3t"}},
			wantErr: true,
			field:   "auth.clientSecret",
		},
		{
			name:    "oauth 公共客户端（无 secret）允许",
			request: mcpadmin.UpsertRequest{Auth: &mcpconfig.MCPAuthConfig{Type: "oauth", ClientID: "cid"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateProjectScopeSecrets(tc.request, "/repo/.aicli/mcp.yaml")
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("不应报错: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("应拒绝明文凭证")
			}
			message := err.Error()
			if !strings.Contains(message, "明文凭证") || !strings.Contains(message, "--scope user") {
				t.Fatalf("错误信息缺少可行动提示: %v", err)
			}
			if !strings.Contains(message, tc.field) {
				t.Fatalf("错误信息应点名字段 %q: %v", tc.field, err)
			}
		})
	}
}

// 项目级写入：明文凭证直接失败且不落盘；${VAR} 引用则正常写入项目文件。
func TestRunMCPAddCommandProjectScope(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	isolateMCPCommandHome(t, home)
	project := t.TempDir()
	t.Chdir(project)

	previousConfigFile := mcpConfigFile
	mcpConfigFile = ""
	t.Cleanup(func() { mcpConfigFile = previousConfigFile })

	_, err := runMCPAddCommand(mcpAddCommandOptions{
		Name:      "secret-server",
		Target:    "http://127.0.0.1:9/mcp",
		Transport: "streamable",
		Headers:   []string{"Authorization: Bearer sk-live-abc"},
		Scope:     mcpWriteScopeProject,
	})
	if err == nil {
		t.Fatal("项目级写入明文凭证应被拒绝")
	}
	projectFile := filepath.Join(project, ".aicli", "mcp.yaml")
	if _, statErr := os.Stat(projectFile); statErr == nil {
		t.Fatalf("拒绝后不应留下配置文件: %s", projectFile)
	}

	result, err := runMCPAddCommand(mcpAddCommandOptions{
		Name:      "ref-server",
		Target:    "http://127.0.0.1:9/mcp",
		Transport: "streamable",
		Headers:   []string{"Authorization: Bearer ${MY_TOKEN}"},
		Scope:     mcpWriteScopeProject,
	})
	if err != nil {
		t.Fatalf("引用式凭证应可写入项目级: %v", err)
	}
	if filepath.Clean(result.ConfigPath) != filepath.Clean(projectFile) {
		t.Fatalf("写入路径 = %q, want %q", result.ConfigPath, projectFile)
	}
	content, err := os.ReadFile(projectFile)
	if err != nil {
		t.Fatalf("读取项目级配置: %v", err)
	}
	if !strings.Contains(string(content), "${MY_TOKEN}") {
		t.Fatalf("应保留 ${VAR} 字面量:\n%s", content)
	}
}

// 启停/删除作用在「实际定义该 server 的文件」上。
func TestMCPAdminServiceTargetsDefiningFile(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	isolateMCPCommandHome(t, home)
	project := t.TempDir()
	t.Chdir(project)

	userPath := writeMCPConfigFileForTest(t, filepath.Join(home, ".aicli"), "mcp.yaml", `mcpServers:
  base:
    type: stdio
    command: base-cmd
    timeout: 30s
  shared:
    type: stdio
    command: from-user
    timeout: 30s
`)
	projectPath := writeMCPConfigFileForTest(t, filepath.Join(project, ".aicli"), "mcp.yaml", `mcpServers:
  shared:
    type: stdio
    command: from-project
    timeout: 30s
`)

	previousConfigFile := mcpConfigFile
	mcpConfigFile = ""
	t.Cleanup(func() { mcpConfigFile = previousConfigFile })

	if got := locateMCPServerConfigFile("base"); filepath.Clean(got) != filepath.Clean(userPath) {
		t.Fatalf("base 定义文件 = %q, want %q", got, userPath)
	}
	if got := locateMCPServerConfigFile("shared"); filepath.Clean(got) != filepath.Clean(projectPath) {
		t.Fatalf("shared 定义文件应为项目级: %q", got)
	}
	if got := locateMCPServerConfigFile("nope"); got != "" {
		t.Fatalf("未定义的 server 应返回空串: %q", got)
	}

	// 通过服务定位断言写入目标。
	if got := newMCPAdminServiceForServer("base", false).ConfigPath(); filepath.Clean(got) != filepath.Clean(userPath) {
		t.Fatalf("base 写入目标 = %q, want %q", got, userPath)
	}
	if got := newMCPAdminServiceForServer("shared", false).ConfigPath(); filepath.Clean(got) != filepath.Clean(projectPath) {
		t.Fatalf("shared 写入目标 = %q, want %q", got, projectPath)
	}

	// 删除项目级定义后，用户级定义应重新生效（同名覆盖的自然语义）。
	if err := newMCPAdminServiceForServer("shared", false).Remove(t.Context(), "shared"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	result, err := loadMCPConfigLayered()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	origin, ok := result.Origin("shared")
	if !ok || filepath.Clean(origin.Path) != filepath.Clean(userPath) {
		t.Fatalf("删除项目级后应由用户级接管: %#v", origin)
	}
}
