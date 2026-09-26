package runtimeapi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

func newProfileInheritanceHandler(t *testing.T) *Handler {
	t.Helper()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	t.Cleanup(sessionManager.Stop)
	t.Cleanup(handler.getSessionHub().StopAll)
	handler.SetSessionManager(sessionManager)
	handler.SetRuntimeConfig(runtimecfg.DefaultRuntimeConfig(), "")
	return handler
}

// TestSessionAgentControllerSpawn_InheritsParentProfileBinding 覆盖 FR-9/D8（V5 回归）：
// spawn 装配点必须把父会话的 profile 绑定快照进子会话，否则子 actor 构建期
// profileState 为 nil，子工具面/模型会比已收窄的父策略更宽。
func TestSessionAgentControllerSpawn_InheritsParentProfileBinding(t *testing.T) {
	ctx := context.Background()
	handler := newProfileInheritanceHandler(t)

	parent, err := handler.sessionManager.Create(ctx, "user-profile-inherit")
	require.NoError(t, err)
	if parent.Metadata.Context == nil {
		parent.Metadata.Context = map[string]interface{}{}
	}
	profileRoot := t.TempDir()
	sessionmeta.Set(parent.Metadata.Context, sessionmeta.ProfileRef, profileRoot, sessionmeta.LegacyAPIProfileReference)
	sessionmeta.Set(parent.Metadata.Context, sessionmeta.ProfileName, "coding")
	sessionmeta.Set(parent.Metadata.Context, sessionmeta.ProfileAgent, "explore")
	sessionmeta.Set(parent.Metadata.Context, sessionmeta.ProfileRoot, profileRoot)
	require.NoError(t, handler.sessionManager.Update(ctx, parent))

	controller := handler.getAgentSessionController()
	require.NotNil(t, controller)
	childID := "profile-inherit-child"
	result, err := controller.Spawn(ctx, parent.ID, toolbroker.SpawnAgentArgs{ID: childID})
	require.NoError(t, err)
	require.NotNil(t, result)

	// 从存储重新加载：证明绑定不仅进了内存对象，也随子会话持久化（重启恢复/独立
	// 构建 actor 时仍能解析父级 profile）。
	child, err := handler.sessionManager.GetStorage().Load(ctx, childID)
	require.NoError(t, err)
	assert.Equal(t, profileRoot, sessionmeta.String(child.Metadata.Context, sessionmeta.ProfileRef))
	assert.Equal(t, profileRoot, child.Metadata.Context[sessionmeta.LegacyAPIProfileReference])
	assert.Equal(t, "coding", sessionmeta.String(child.Metadata.Context, sessionmeta.ProfileName))
	assert.Equal(t, "explore", sessionmeta.String(child.Metadata.Context, sessionmeta.ProfileAgent))
	assert.Equal(t, profileRoot, sessionmeta.String(child.Metadata.Context, sessionmeta.ProfileRoot))
}

// TestSessionAgentControllerSpawn_WithoutParentProfileKeepsChildUnbound 守护 NFR-1：
// 父会话未绑定 profile 时，spawn 不得往子会话写任何 profile 键（不声明 = 零变化）。
func TestSessionAgentControllerSpawn_WithoutParentProfileKeepsChildUnbound(t *testing.T) {
	ctx := context.Background()
	handler := newProfileInheritanceHandler(t)

	parent, err := handler.sessionManager.Create(ctx, "user-no-profile")
	require.NoError(t, err)

	controller := handler.getAgentSessionController()
	require.NotNil(t, controller)
	childID := "no-profile-child"
	result, err := controller.Spawn(ctx, parent.ID, toolbroker.SpawnAgentArgs{ID: childID})
	require.NoError(t, err)
	require.NotNil(t, result)
	child, err := handler.sessionManager.GetStorage().Load(ctx, childID)
	require.NoError(t, err)

	for _, key := range []string{
		sessionmeta.ProfileRef,
		sessionmeta.LegacyAPIProfileReference,
		sessionmeta.ProfileName,
		sessionmeta.ProfileAgent,
		sessionmeta.ProfileRoot,
	} {
		_, ok := child.Metadata.Context[key]
		assert.False(t, ok, "父会话未绑定 profile 时子会话不得写入 %s（不声明 = 零变化）", key)
	}
}

// TestSessionActor_ChildSessionResolvesInheritedProfilePrompt 是端到端对照：spawn 出的
// 子会话构建 actor 时，必须解析出继承自父会话的 profile 指令（修复前该请求里没有
// profile prompt，因为绑定从未复制到子会话）。
func TestSessionActor_ChildSessionResolvesInheritedProfilePrompt(t *testing.T) {
	ctx := context.Background()
	profileRoot := t.TempDir()
	writeProfileFile := func(path, contents string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(contents), 0o644))
	}
	writeProfileFile(filepath.Join(profileRoot, "profile.yaml"), "profile:\n  name: dev\n  default_agent: tester\nagents:\n  tester: {}\n")
	writeProfileFile(filepath.Join(profileRoot, "agents", "tester", "prompts", "system.md"), "Profile system prompt.")

	registry := skill.NewRegistry(nil)
	handler := NewHandler(registry, nil, nil)
	handler.SetProfileSupport(ProfileSupportConfig{Registry: profilesys.NewRegistry("")})
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	t.Cleanup(sessionManager.Stop)
	t.Cleanup(handler.getSessionHub().StopAll)
	handler.SetSessionManager(sessionManager)
	handler.SetRuntimeConfig(runtimecfg.DefaultRuntimeConfig(), "")

	provider := &testLLMProvider{name: "test-model", content: "ok"}
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("test-model", provider))
	handler.SetLLMRuntime(runtime)

	parent, err := sessionManager.Create(ctx, "user-inherit-actor")
	require.NoError(t, err)
	parent.SetContext(apiProfileContextReference, profileRoot)
	require.NoError(t, sessionManager.Update(ctx, parent))

	controller := handler.getAgentSessionController()
	require.NotNil(t, controller)
	childID := "inherit-actor-child"
	result, err := controller.Spawn(ctx, parent.ID, toolbroker.SpawnAgentArgs{ID: childID})
	require.NoError(t, err)
	require.NotNil(t, result)

	actor, err := handler.buildSessionActor(childID)
	require.NoError(t, err)
	defer actor.Stop()

	_, err = actor.SubmitPrompt(ctx, "hi", nil)
	require.NoError(t, err)

	require.NotEmpty(t, provider.requests)
	found := false
	for _, message := range provider.requests[0].Messages {
		if message.Role == "system" && strings.Contains(message.Content, "Profile system prompt.") {
			found = true
		}
	}
	assert.True(t, found, "子会话必须解析出继承自父会话的 profile 指令")
}

// TestApplyAPIChildAgentdefToolPolicy_NeverWidensParentProfileAllowlist 覆盖 FR-9 的
// 「只收窄」：父 profile 已收窄的允许集之上叠加 explore agentdef 后，派生结果既不能
// 放宽父允许集，也不能恢复 explore 明确拒绝的写入工具。
func TestApplyAPIChildAgentdefToolPolicy_NeverWidensParentProfileAllowlist(t *testing.T) {
	parentAllowlist := []string{"view", "grep", "write"}
	parentPolicy := agent.NewToolExecutionPolicy(parentAllowlist, false)
	apiAgent := agent.NewAgent(&agent.Config{Name: "child", Provider: "test", Model: "test"}, nil)
	apiAgent.SetToolExecutionPolicy(parentPolicy)

	applyAPIChildAgentdefToolPolicy(apiAgent, "explore", t.TempDir())

	child := apiAgent.GetToolExecutionPolicy()
	require.NotNil(t, child)
	assert.NotSame(t, parentPolicy, child, "子策略必须是派生副本，不得原地改写父策略")
	assert.True(t, child.ReadOnly, "explore 的 read_only 必须叠加到父策略之上")
	assert.True(t, child.DeniedTools["write"])
	assert.True(t, child.DeniedTools["edit"])
	assert.False(t, child.AllowsDefinition("write"), "写入工具不得在子会话可见")
	assert.False(t, child.AllowsDefinition("bash"), "子策略不得获得父允许集之外的工具")
	assert.True(t, child.AllowsDefinition("view"), "父允许集内的只读工具必须保留")
	assert.False(t, parentPolicy.ReadOnly, "父策略不得被派生过程改写")
}

// TestSessionAgentControllerSpawn_ProfileSwitchDoesNotRetroact 覆盖 Batch 5 DoD 的
// 「不追溯已存在子代理」（D8/§17.4）：spawn 是绑定快照，父会话之后切换 profile
// 不得改写已存在的子会话绑定。
func TestSessionAgentControllerSpawn_ProfileSwitchDoesNotRetroact(t *testing.T) {
	ctx := context.Background()
	handler := newProfileInheritanceHandler(t)

	parent, err := handler.sessionManager.Create(ctx, "user-profile-switch")
	require.NoError(t, err)
	if parent.Metadata.Context == nil {
		parent.Metadata.Context = map[string]interface{}{}
	}
	firstRoot := t.TempDir()
	sessionmeta.Set(parent.Metadata.Context, sessionmeta.ProfileRef, firstRoot, sessionmeta.LegacyAPIProfileReference)
	require.NoError(t, handler.sessionManager.Update(ctx, parent))

	controller := handler.getAgentSessionController()
	require.NotNil(t, controller)
	childID := "profile-snapshot-child"
	result, err := controller.Spawn(ctx, parent.ID, toolbroker.SpawnAgentArgs{ID: childID})
	require.NoError(t, err)
	require.NotNil(t, result)

	// 父会话切换到新 profile 并持久化。
	secondRoot := t.TempDir()
	sessionmeta.Set(parent.Metadata.Context, sessionmeta.ProfileRef, secondRoot, sessionmeta.LegacyAPIProfileReference)
	require.NoError(t, handler.sessionManager.Update(ctx, parent))

	child, err := handler.sessionManager.GetStorage().Load(ctx, childID)
	require.NoError(t, err)
	assert.Equal(t, firstRoot, sessionmeta.String(child.Metadata.Context, sessionmeta.ProfileRef),
		"已有子代理必须保持 spawn 时的绑定快照")
}
