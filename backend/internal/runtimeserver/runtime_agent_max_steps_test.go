package runtimeserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

func TestPersistRuntimeAgentMaxStepsYAMLPreservesOtherContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime.yaml")
	original := "# 顶部注释\nserver:\n  port: 8090\nagent:\n  # 步数上限\n  maxSteps: 0\n  maxToolCalls: 12\ncustom_section:\n  keep: true\n"
	require.NoError(t, os.WriteFile(path, []byte(original), 0o644))

	require.NoError(t, PersistRuntimeAgentMaxSteps(path, 11))

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	text := string(raw)
	require.Contains(t, text, "# 顶部注释")
	require.Contains(t, text, "# 步数上限")
	require.Contains(t, text, "custom_section:")
	require.Contains(t, text, "keep: true")
	require.Contains(t, text, "maxToolCalls: 12")
	require.Contains(t, text, "maxSteps: 11")
	require.NotContains(t, text, "maxSteps: 0")

	backups, err := filepath.Glob(path + ".*.bak")
	require.NoError(t, err)
	require.Len(t, backups, 1)
	backupRaw, err := os.ReadFile(backups[0])
	require.NoError(t, err)
	require.Equal(t, original, string(backupRaw))
}

func TestPersistRuntimeAgentMaxStepsCreatesMissingSections(t *testing.T) {
	dir := t.TempDir()

	cases := []struct {
		name     string
		original string
	}{
		{name: "missing agent section", original: "server:\n  port: 8090\n"},
		{name: "empty file", original: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, "runtime.yaml")
			require.NoError(t, os.WriteFile(path, []byte(tc.original), 0o644))

			require.NoError(t, PersistRuntimeAgentMaxSteps(path, 5))

			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Contains(t, string(raw), "maxSteps: 5")
		})
	}
}

func TestPersistRuntimeAgentMaxStepsJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime.json")
	require.NoError(t, os.WriteFile(path, []byte("{\n  \"agent\": {\n    \"maxSteps\": 0,\n    \"maxToolCalls\": 12\n  },\n  \"server\": {\n    \"port\": 8090\n  }\n}\n"), 0o644))

	require.NoError(t, PersistRuntimeAgentMaxSteps(path, 7))

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	text := string(raw)
	require.Contains(t, text, "\"maxSteps\": 7")
	require.Contains(t, text, "\"maxToolCalls\": 12")
	require.Contains(t, text, "\"port\": 8090")
}

func TestPersistRuntimeAgentMaxStepsRejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime.yaml")
	require.NoError(t, os.WriteFile(path, []byte("agent:\n  maxSteps: 3\n"), 0o644))

	require.Error(t, PersistRuntimeAgentMaxSteps(path, -1))
	require.Error(t, PersistRuntimeAgentMaxSteps("", 3))
	// 不支持的格式仍然拒绝；「文件不存在」已改为新建成功，见 CreatesFileWhenMissing。
	require.Error(t, PersistRuntimeAgentMaxSteps(filepath.Join(dir, "runtime.toml"), 3))

	// 失败时不应污染原文件。
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(raw), "maxSteps: 3")
}

func TestPersistRuntimeAgentMaxStepsRejectsNonMappingAgent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime.yaml")
	require.NoError(t, os.WriteFile(path, []byte("agent: 3\n"), 0o644))

	require.Error(t, PersistRuntimeAgentMaxSteps(path, 3))
}

func TestPersistRuntimeAgentMaxStepsKeepsInlineComment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime.yaml")
	require.NoError(t, os.WriteFile(path, []byte("agent:\n  maxSteps: 3 # 行内注释保留\n"), 0o644))

	require.NoError(t, PersistRuntimeAgentMaxSteps(path, 8))

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(raw), "maxSteps: 8")
	require.Contains(t, string(raw), "# 行内注释保留")
}

func TestPersistRuntimeAgentMaxStepsKeepsFileLayout(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime.yaml")
	original := "version: \"v1\"\n\nagent:\n  maxSteps: 0\n  maxToolCalls: 0\n\nbackground:\n  defaultTimeout: 0s\n"
	require.NoError(t, os.WriteFile(path, []byte(original), 0o644))

	require.NoError(t, PersistRuntimeAgentMaxSteps(path, 6))

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	// 只改标量：空行与其余行必须逐字节保持原样。
	require.Equal(t, strings.Replace(original, "maxSteps: 0", "maxSteps: 6", 1), string(raw))
}

func TestNewRuntimeAgentMaxStepsPersisterUpdatesMemoryAndFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime.yaml")
	require.NoError(t, os.WriteFile(path, []byte("agent:\n  maxSteps: 3\n  maxToolCalls: 12\n"), 0o644))

	manager := runtimecfg.NewRuntimeManager(path)
	require.NoError(t, manager.Load())
	require.Equal(t, 3, manager.Get().Agent.MaxMaxSteps)

	persister := NewRuntimeAgentMaxStepsPersister(manager)
	configFile, err := persister(9)
	require.NoError(t, err)
	require.Equal(t, path, configFile)

	// 落盘：配置文件被改写。
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(raw), "maxSteps: 9")
	require.Contains(t, string(raw), "maxToolCalls: 12")

	// 内存：RuntimeManager 快照更新，per-request 的 resolver 走的就是这个函数。
	selected, selectedPath := manager.SelectConfigForScopeWithPath("")
	require.Equal(t, path, selectedPath)
	require.NotNil(t, selected)
	require.Equal(t, 9, selected.Agent.MaxMaxSteps)
}

func TestNewRuntimeAgentMaxStepsPersisterRollsBackMemoryWhenFileWriteFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime.yaml")
	require.NoError(t, os.WriteFile(path, []byte("agent:\n  maxSteps: 4\n"), 0o644))

	manager := runtimecfg.NewRuntimeManager(path)
	require.NoError(t, manager.Load())

	// 让落盘必然失败：把路径上的「父目录」占成一个普通文件（文件不存在本身已不再算失败）。
	blocked := filepath.Join(dir, "blocked")
	require.NoError(t, os.WriteFile(blocked, []byte("not a directory"), 0o644))
	manager.SetFilePath(filepath.Join(blocked, "runtime.yaml"))
	persister := NewRuntimeAgentMaxStepsPersister(manager)

	_, err := persister(11)
	require.Error(t, err)
	require.Equal(t, 4, manager.Get().Agent.MaxMaxSteps)
}

func TestNewRuntimeAgentMaxStepsPersisterRejectsNilManager(t *testing.T) {
	persister := NewRuntimeAgentMaxStepsPersister(nil)

	_, err := persister(5)
	require.Error(t, err)
}

func TestNewRuntimeAgentMaxStepsReaderReadsMemorySnapshotAndPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime.yaml")
	require.NoError(t, os.WriteFile(path, []byte("agent:\n  maxSteps: 7\n  maxToolCalls: 12\n"), 0o644))

	manager := runtimecfg.NewRuntimeManager(path)
	require.NoError(t, manager.Load())

	reader := NewRuntimeAgentMaxStepsReader(manager)
	maxSteps, configFile, err := reader()
	require.NoError(t, err)
	require.Equal(t, 7, maxSteps)
	require.Equal(t, path, configFile)

	// 只读：内存快照与配置文件都不应被改动。
	require.Equal(t, 7, manager.Get().Agent.MaxMaxSteps)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "agent:\n  maxSteps: 7\n  maxToolCalls: 12\n", string(raw))
}

func TestNewRuntimeAgentMaxStepsReaderTracksMemoryUpdates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime.yaml")
	require.NoError(t, os.WriteFile(path, []byte("agent:\n  maxSteps: 3\n"), 0o644))

	manager := runtimecfg.NewRuntimeManager(path)
	require.NoError(t, manager.Load())

	// 保存端点先改内存：读取端必须立刻看到新值（前端回显的就是它）。
	persister := NewRuntimeAgentMaxStepsPersister(manager)
	_, err := persister(9)
	require.NoError(t, err)

	maxSteps, configFile, err := NewRuntimeAgentMaxStepsReader(manager)()
	require.NoError(t, err)
	require.Equal(t, 9, maxSteps)
	require.Equal(t, path, configFile)
}

func TestNewRuntimeAgentMaxStepsReaderRejectsNilManager(t *testing.T) {
	maxSteps, configFile, err := NewRuntimeAgentMaxStepsReader(nil)()
	require.Error(t, err)
	require.Equal(t, 0, maxSteps)
	require.Empty(t, configFile)
}

func TestNewRuntimeAgentMaxStepsReaderFallsBackToDefaultsWhenFileMissing(t *testing.T) {
	// NewRuntimeManager 会先装入 DefaultRuntimeConfig，Load 遇到文件不存在也算成功，
	// 因此「尚未落盘」时 Get() 不是 nil：读取端回默认值 0 + 路径，保存端点负责新建文件。
	dir := t.TempDir()
	path := filepath.Join(dir, "missing.yaml")

	manager := runtimecfg.NewRuntimeManager(path)
	require.NoError(t, manager.Load())

	maxSteps, configFile, err := NewRuntimeAgentMaxStepsReader(manager)()
	require.NoError(t, err)
	require.Equal(t, 0, maxSteps)
	require.Equal(t, path, configFile)
}

func TestPersistRuntimeAgentMaxStepsCreatesFileWhenMissing(t *testing.T) {
	// 文件不存在时按空文档处理并新建（父目录一并创建），而不是返回错误。
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "runtime.yaml")

	require.NoError(t, PersistRuntimeAgentMaxSteps(path, 15))

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	// 只写这一个键：其余配置仍由 DefaultRuntimeConfig 兜底，不凭空生成其它键值。
	require.Equal(t, "agent:\n  maxSteps: 15\n", string(raw))
}

func TestNewRuntimeAgentMaxStepsPersisterCreatesMissingConfigFile(t *testing.T) {
	// 全新安装：skillsRuntime.configFile 指向的 runtime 配置文件尚未落盘，Load 用默认值继续跑
	// （GET 回 0 + 路径），此时保存必须能补出文件，而不是 500 断链。
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "runtime.yaml")

	manager := runtimecfg.NewRuntimeManager(path)
	require.NoError(t, manager.Load())

	reader := NewRuntimeAgentMaxStepsReader(manager)
	maxSteps, configFile, err := reader()
	require.NoError(t, err)
	require.Equal(t, 0, maxSteps)
	require.Equal(t, path, configFile)

	configFile, err = NewRuntimeAgentMaxStepsPersister(manager)(20)
	require.NoError(t, err)
	require.Equal(t, path, configFile)

	// 内存与文件同时更新；保存前端会 refresh，再次读取必须拿到新值。
	require.Equal(t, 20, manager.Get().Agent.MaxMaxSteps)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(raw), "maxSteps: 20")

	maxSteps, _, err = reader()
	require.NoError(t, err)
	require.Equal(t, 20, maxSteps)
}
