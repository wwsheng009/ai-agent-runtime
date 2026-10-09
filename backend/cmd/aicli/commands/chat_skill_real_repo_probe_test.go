package commands

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/functions"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// repoBackendDir 从本测试文件位置反推 backend/ 目录，避免写死绝对路径。
func repoBackendDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// <repo>/backend/cmd/aicli/commands/chat_skill_real_repo_probe_test.go
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", "..", ".."))
}

// TestProbeRealRepoSkillLoad 在**真实仓库**里跑一遍生产用的私有 manager 路径。
// 上一版探针跑在 temp 目录：那里祖先链没有 .aicli/runtime.yaml，所以 runtime 配置
// 走的是内置默认；真实仓库里该文件存在，是探针与生产之间唯一剩下的变量。
// 这里只打印不断言，定位到具体失败点后再补断言。
func TestProbeRealRepoSkillLoad(t *testing.T) {
	backend := repoBackendDir(t)
	chdirTest(t, backend)

	cfg := &config.Config{
		SkillsRuntime: &config.SkillsRuntimeConfig{Enabled: true, ConfigFile: "configs/runtime.yaml"},
	}
	session := &ChatSession{
		ProviderName:     "nvidia",
		Model:            "z-ai/glm4.7",
		FunctionRegistry: functions.NewFunctionRegistry(),
		Config:           cfg,
	}

	runtimePath := resolveChatRuntimeConfigPath(cfg, session)
	t.Logf("A resolveChatRuntimeConfigPath = %q", runtimePath)

	runtimeConfig, fromCache, err := loadCachedRuntimeConfig(runtimePath)
	t.Logf("B loadCachedRuntimeConfig: err=%v nil=%v fromCache=%v", err, runtimeConfig == nil, fromCache)
	if err != nil {
		// 到这里就说明绑定是 nil + error，和"安静地 0 skills"是不同的故障。
		t.Logf("C => 加载 runtime 配置失败，initSkillFunctions 会走 `加载 skills runtime 配置失败` 分支返回 error")
	}

	dirs := resolveChatSkillDirs(cfg, session, nil)
	t.Logf("D resolveChatSkillDirs = %v", dirs)

	binding, initErr := initSkillFunctions(cfg, session, nil, nil, 0, "")
	t.Logf("E initSkillFunctions: binding=%v err=%v", binding != nil, initErr)
	if binding != nil {
		t.Logf("F binding.roots=%v skillFunctions=%d", binding.roots, len(binding.skillFunctions))
		_ = binding.Close()
	}
}
