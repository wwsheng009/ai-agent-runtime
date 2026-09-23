package runtimeserver

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	skillsapi "github.com/wwsheng009/ai-agent-runtime/internal/api/skills"
)

// TestRuntimeConfigWritersHonorSharedWriteLock：runtime-server 侧对**同一份 config.yaml**
// 的写点必须与 aicli（chat/theme/provider/routing 等）共用 agentconfig 的同一把配置文件
// 写锁。判据与 agentconfig 侧一致：先占住目标文件的锁，再发起写点的写入——写入必须等待
// 锁释放；能直接完成说明该写点没走共享锁（读-改-写仍可并发丢更新）。
func TestRuntimeConfigWritersHonorSharedWriteLock(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("server:\n  port: 8101\n"), 0o644))

	writers := []struct {
		name  string
		write func() error
	}{
		{
			name: "agent_max_steps",
			write: func() error {
				return PersistRuntimeAgentMaxSteps(configPath, 7)
			},
		},
		{
			name: "skills_runtime_policy",
			write: func() error {
				return persistSkillsRuntimeConfigSection(configPath, configPath, &agentconfig.SkillsRuntimeConfig{})
			},
		},
		{
			name: "config_document_save",
			write: func() error {
				raw := "server:\n  port: 8102\n"
				_, err := NewLocalConfigDocumentService(configPath).SaveDocument(
					skillsapi.ConfigDocumentSaveRequest{Raw: &raw, Mode: "raw"},
				)
				return err
			},
		},
	}

	for _, tc := range writers {
		t.Run(tc.name, func(t *testing.T) {
			unlock := agentconfig.LockConfigFileWrite(configPath)
			done := make(chan error, 1)
			go func() { done <- tc.write() }()

			select {
			case err := <-done:
				unlock()
				t.Fatalf("写点未走共享写锁：锁被占用时写入已完成（err=%v）", err)
			case <-time.After(200 * time.Millisecond):
			}
			unlock()

			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(10 * time.Second):
				t.Fatal("锁释放后写点仍未完成（可能二次取锁自锁）")
			}
		})
	}
}
