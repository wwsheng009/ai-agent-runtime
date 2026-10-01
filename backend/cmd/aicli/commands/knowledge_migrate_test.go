package commands

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// writeKnowledgeTestRuntimeYAML 落一份临时 runtime.yaml 并返回其绝对路径。
// 绝对路径属于非约定值，ResolveRuntimeConfigBootstrapPath 会原样采用，
// 使测试的 mode/开关不受仓库/用户层配置影响。
func writeKnowledgeTestRuntimeYAML(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runtime.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write runtime.yaml: %v", err)
	}
	return path
}

func knowledgeTestConfig(path string) func() *config.Config {
	return func() *config.Config {
		return &config.Config{SkillsRuntime: &config.SkillsRuntimeConfig{ConfigFile: path}}
	}
}

// TestRunKnowledgeMigrateOffWorkspace：mode=off 是合法状态，命令必须报告
// "无需迁移"并成功退出（不得去建库/建锁文件）。
func TestRunKnowledgeMigrateOffWorkspace(t *testing.T) {
	ws := t.TempDir()
	cfgPath := writeKnowledgeTestRuntimeYAML(t, "knowledge:\n  mode: off\n")
	var buf bytes.Buffer
	if err := runKnowledgeMigrate(knowledgeTestConfig(cfgPath), ws, false, true, 10*time.Second, &buf); err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(buf.String(), "无需迁移") {
		t.Fatalf("out = %q, want 含 无需迁移", buf.String())
	}
	if _, err := os.Stat(filepath.Join(ws, ".aicli", "knowledge", "knowledge.db")); err == nil {
		t.Fatal("mode=off 不得创建 store（零副作用契约）")
	}
}

// TestRunKnowledgeMigrateCreatesStore：mode=on + 全新 workspace，writer 打开
// 即建库并应用迁移（--no-index 跳过等待）；输出 owner 与最终状态。
func TestRunKnowledgeMigrateCreatesStore(t *testing.T) {
	ws := t.TempDir()
	cfgPath := writeKnowledgeTestRuntimeYAML(t, "knowledge:\n  mode: on\n  code_tools: on\n")
	var buf bytes.Buffer
	if err := runKnowledgeMigrate(knowledgeTestConfig(cfgPath), ws, false, true, 30*time.Second, &buf); err != nil {
		t.Fatalf("err = %v", err)
	}
	out := buf.String()
	for _, want := range []string{"已应用 schema 迁移", "mode", "owner"} {
		if !strings.Contains(out, want) {
			t.Fatalf("out 缺少 %q：\n%s", want, out)
		}
	}
	if _, err := os.Stat(filepath.Join(ws, ".aicli", "knowledge", "knowledge.db")); err != nil {
		t.Fatalf("store 未创建: %v", err)
	}
}

// TestNewKnowledgeCommand_MigrateFlags 钉住 `knowledge migrate` 的 CLI 契约：
// 子命令存在、四个 flag 齐备（workspace / json / no-index / timeout）。
func TestNewKnowledgeCommand_MigrateFlags(t *testing.T) {
	cmd := NewKnowledgeCommand(func() *config.Config { return nil })
	var migrate *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "migrate" {
			migrate = sub
			break
		}
	}
	if migrate == nil {
		t.Fatalf("knowledge migrate subcommand missing; got %v", cmd.Commands())
	}
	for _, flag := range []string{"workspace", "json", "no-index", "timeout"} {
		if migrate.Flags().Lookup(flag) == nil {
			t.Errorf("knowledge migrate missing --%s flag", flag)
		}
	}
}
