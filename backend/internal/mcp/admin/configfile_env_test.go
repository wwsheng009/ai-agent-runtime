package admin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
)

func TestBuildConfigMergesEnvOnUpdate(t *testing.T) {
	existing := &config.MCPConfig{
		Name: "docs",
		Type: "streamable",
		URL:  "https://example.com/mcp",
		Env: map[string]string{
			"KEEP":     "1",
			"OVERRIDE": "old",
		},
	}
	cfg, err := BuildConfig(UpsertRequest{
		Name: "docs",
		Type: "streamable",
		URL:  "https://example.com/mcp",
		Env: map[string]string{
			"OVERRIDE": "new",
			"ADDED":    "2",
		},
	}, existing)
	if err != nil {
		t.Fatalf("BuildConfig: %v", err)
	}
	if cfg.Env["KEEP"] != "1" {
		t.Fatalf("existing env must survive update, got %#v", cfg.Env)
	}
	if cfg.Env["OVERRIDE"] != "new" {
		t.Fatalf("provided env must override, got %#v", cfg.Env)
	}
	if cfg.Env["ADDED"] != "2" {
		t.Fatalf("new env must be added, got %#v", cfg.Env)
	}
}

func TestBuildConfigKeepsHeaderEnvMapping(t *testing.T) {
	cfg, err := BuildConfig(UpsertRequest{
		Name:    "docs",
		Type:    "streamable",
		URL:     "https://example.com/mcp",
		Headers: map[string]string{"Authorization": "Bearer ${TOKEN}"},
	}, nil)
	if err != nil {
		t.Fatalf("BuildConfig: %v", err)
	}
	if cfg.Env["HEADER_Authorization"] != "Bearer ${TOKEN}" {
		t.Fatalf("header env mapping = %q", cfg.Env["HEADER_Authorization"])
	}
}

// 管理操作（add）读改写回配置时，必须保留 `${VAR}` 原始引用：
// 既不能丢引用，也不能把展开后的真实值固化进文件。
func TestServiceAddKeepsRawEnvRefsOnDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.yaml")
	raw := `mcpServers:
  docs:
    type: streamable
    url: https://example.com/mcp
    enabled: true
    env:
      HEADER_Authorization: Bearer ${TOKEN}
`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	service := NewService(path, WithApplyOnMutate(false))
	if _, err := service.Add(context.Background(), UpsertRequest{
		Name: "added",
		Type: "streamable",
		URL:  "https://example.com/other",
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(content), "Bearer ${TOKEN}") {
		t.Fatalf("raw env ref must survive management write:\n%s", content)
	}
	if !strings.Contains(string(content), "added") {
		t.Fatalf("new server missing:\n%s", content)
	}
}
