package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

// Batch 12 的 server 侧断言（设计文档 §18.1 五阶段 + A1/A2/A3 的 Web 半程）。
// 与 CLI 侧的 `chat_profile_switch_test.go` 同构：同一份报告契约、同一组失效动作，
// 差异只在「下一 turn 生效」的实现路径（server 靠驱逐 actor 后重建，V15/V19）。

func newProfileSwitchTestHandler(t *testing.T) (*Handler, *llm.LLMRuntime, *testLLMProvider) {
	t.Helper()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
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
	return handler, runtime, provider
}

// writeProfileSwitchFixture 落一个最小可用 profile（单 agent + 单 system prompt），
// 目录本身即 profile 引用（与 CLI/actor 解析路径一致）。
func writeProfileSwitchFixture(t *testing.T, systemPrompt string) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "agents", "tester", "prompts"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "profile.yaml"),
		[]byte("profile:\n  name: dev\n  default_agent: tester\nagents:\n  tester: {}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "agents", "tester", "prompts", "system.md"),
		[]byte(systemPrompt), 0o644))
	return root
}

func requestHasSystemPrompt(req *llm.LLMRequest, fragment string) bool {
	if req == nil {
		return false
	}
	for _, message := range req.Messages {
		if message.Role == "system" && strings.Contains(message.Content, fragment) {
			return true
		}
	}
	return false
}

// TestSessionProfileSwitch_PersistsBindingAndClearsInvalidation 覆盖五阶段核心：
// 身份四键落盘、冻结锚点删除、token 计数清零、无 actor 时 scope=none（不凭空建 actor）。
func TestSessionProfileSwitch_PersistsBindingAndClearsInvalidation(t *testing.T) {
	ctx := context.Background()
	handler, _, _ := newProfileSwitchTestHandler(t)
	profileRoot := writeProfileSwitchFixture(t, "Profile A system prompt.")

	session, err := handler.sessionManager.Create(ctx, "user-profile-switch-core")
	require.NoError(t, err)
	session.Metadata.Context = map[string]interface{}{
		sessionmeta.SystemPromptFrozen: "frozen-anchor",
		sessionmeta.ContextWindowCount: 12345,
	}
	require.NoError(t, handler.sessionManager.Update(ctx, session))

	report, err := handler.applySessionProfileSwitch(ctx, session.ID, profileRoot)
	require.NoError(t, err)
	require.NotNil(t, report)

	assert.Equal(t, "", report.From, "无绑定会话的基线引用为空")
	assert.Equal(t, profileRoot, report.To)
	assert.Equal(t, "next_turn", report.EffectiveAt)
	assert.NotEmpty(t, report.CacheNotice)
	assert.False(t, report.InFlightTurn)
	assert.True(t, report.AnchorCleared, "冻结锚点必须被删除，否则下一次 compose 复用旧 head")
	assert.True(t, report.ContextTokenCountReset, "前缀重建后旧 token 计数必须清零")
	assert.Equal(t, sessionProfileSwitchScopeNone, report.ToolSurfaceScope, "无活体 actor 时不得凭空建 actor")
	assert.False(t, report.ToolSurfaceInvalidated)
	assert.False(t, report.ActorEvicted)
	assert.True(t, report.Changed.PromptChanged, "从无 profile 切入必须报告 prompt 变化")
	assert.False(t, report.Changed.ProviderChanged, "provider 只报告不落地（D30）")

	stored, err := handler.sessionManager.GetStorage().Load(ctx, session.ID)
	require.NoError(t, err)
	assert.Equal(t, profileRoot, sessionmeta.String(stored.Metadata.Context, sessionmeta.ProfileRef))
	assert.Equal(t, "dev", sessionmeta.String(stored.Metadata.Context, sessionmeta.ProfileName))
	assert.Equal(t, "tester", sessionmeta.String(stored.Metadata.Context, sessionmeta.ProfileAgent))
	assert.Equal(t, profileRoot, sessionmeta.String(stored.Metadata.Context, sessionmeta.ProfileRoot))
	_, anchorOK := stored.Metadata.Context[sessionmeta.SystemPromptFrozen]
	assert.False(t, anchorOK, "锚点必须从持久化状态删除")
	_, tokenOK := stored.Metadata.Context[sessionmeta.ContextWindowCount]
	assert.False(t, tokenOK, "token 计数必须从持久化状态删除")
}

// TestSessionProfileSwitch_UnknownProfileIsValidationErrorAndLeavesStateUntouched 覆盖
// FR-5：解析失败显式报错、不静默回退，且失败不得改写任何会话状态。
func TestSessionProfileSwitch_UnknownProfileIsValidationErrorAndLeavesStateUntouched(t *testing.T) {
	ctx := context.Background()
	handler, _, _ := newProfileSwitchTestHandler(t)

	session, err := handler.sessionManager.Create(ctx, "user-profile-switch-unknown")
	require.NoError(t, err)
	require.NoError(t, handler.sessionManager.Update(ctx, session))

	_, err = handler.applySessionProfileSwitch(ctx, session.ID, "does-not-exist-profile")
	require.Error(t, err)
	assert.True(t, isSessionProfileSwitchValidationError(err), "未知 profile 必须是 400 级错误，而不是 500")

	_, err = handler.applySessionProfileSwitch(ctx, session.ID, "   ")
	require.Error(t, err)
	assert.True(t, isSessionProfileSwitchValidationError(err), "空引用必须在解析前被拒绝")

	stored, err := handler.sessionManager.GetStorage().Load(ctx, session.ID)
	require.NoError(t, err)
	assert.Equal(t, "", sessionmeta.String(stored.Metadata.Context, sessionmeta.ProfileRef),
		"失败的切换不得写入任何身份键")
}

// TestSessionProfileSwitch_EvictsIdleActorSoNextTurnUsesNewProfile 是 A1/A2 的 server
// 半程：空闲 actor 必须被驱逐，使下一次构建按新 profile 组装 system prompt——
// 只写 sessionmeta 而不驱逐等于假开关（V15/V19）。
func TestSessionProfileSwitch_EvictsIdleActorSoNextTurnUsesNewProfile(t *testing.T) {
	ctx := context.Background()
	handler, _, provider := newProfileSwitchTestHandler(t)
	profileA := writeProfileSwitchFixture(t, "Profile A system prompt.")
	profileB := writeProfileSwitchFixture(t, "Profile B system prompt.")

	session, err := handler.sessionManager.Create(ctx, "user-profile-switch-actor")
	require.NoError(t, err)
	session.SetContext(apiProfileContextReference, profileA)
	require.NoError(t, handler.sessionManager.Update(ctx, session))

	hub := handler.getSessionHub()
	actor, err := hub.GetOrCreate(session.ID)
	require.NoError(t, err)
	_, err = actor.SubmitPrompt(ctx, "hi", nil)
	require.NoError(t, err)
	require.NotEmpty(t, provider.requests)
	assert.True(t, requestHasSystemPrompt(provider.requests[0], "Profile A system prompt."),
		"切换前首个 turn 必须使用 profile A")

	report, err := handler.applySessionProfileSwitch(ctx, session.ID, profileB)
	require.NoError(t, err)
	assert.False(t, report.InFlightTurn)
	assert.Equal(t, sessionProfileSwitchScopeActor, report.ToolSurfaceScope)
	assert.True(t, report.ToolSurfaceInvalidated, "稳定工具面必须失效")
	assert.True(t, report.ActorEvicted, "空闲 actor 必须被驱逐，否则下一 turn 仍用旧 profile")

	if _, ok := hub.Get(session.ID); ok {
		t.Fatal("切换后旧 actor 必须已从 hub 摘除")
	}

	provider.requests = nil
	rebuilt, err := hub.GetOrCreate(session.ID)
	require.NoError(t, err)
	_, err = rebuilt.SubmitPrompt(ctx, "hi again", nil)
	require.NoError(t, err)
	require.NotEmpty(t, provider.requests)
	assert.True(t, requestHasSystemPrompt(provider.requests[0], "Profile B system prompt."),
		"A2：切换后首个 turn 的 system prompt 必须是新 profile 的组合结果")
	assert.False(t, requestHasSystemPrompt(provider.requests[0], "Profile A system prompt."),
		"A2：旧 profile 的 prompt 不得残留")
}

// blockingProfileSwitchProvider 是可控阻塞的 provider：回合起跑后停在上游调用里，
// 直到测试释放。用于验证「切换不打断在途 turn（A3）」与延迟重建兑现。
type blockingProfileSwitchProvider struct {
	testLLMProvider
	started chan struct{}
	release chan struct{}

	mu       sync.Mutex
	captured []*llm.LLMRequest
}

func (p *blockingProfileSwitchProvider) record(req *llm.LLMRequest) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.captured = append(p.captured, cloneLLMRequest(req))
}

func (p *blockingProfileSwitchProvider) firstRequest() *llm.LLMRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.captured) == 0 {
		return nil
	}
	return p.captured[0]
}

func (p *blockingProfileSwitchProvider) signalStarted() {
	select {
	case p.started <- struct{}{}:
	default:
	}
}

func (p *blockingProfileSwitchProvider) waitRelease(ctx context.Context) {
	select {
	case <-p.release:
	case <-ctx.Done():
	}
}

func (p *blockingProfileSwitchProvider) Call(ctx context.Context, req *llm.LLMRequest) (*llm.LLMResponse, error) {
	p.record(req)
	p.signalStarted()
	p.waitRelease(ctx)
	return &llm.LLMResponse{Content: "ok", Model: p.name}, nil
}

func (p *blockingProfileSwitchProvider) Stream(ctx context.Context, req *llm.LLMRequest) (<-chan llm.StreamChunk, error) {
	p.record(req)
	p.signalStarted()
	ch := make(chan llm.StreamChunk, 1)
	go func() {
		defer close(ch)
		p.waitRelease(ctx)
		select {
		case ch <- llm.StreamChunk{Type: llm.EventTypeText, Content: "ok", Done: true}:
		case <-ctx.Done():
		}
	}()
	return ch, nil
}

// TestSessionProfileSwitch_InFlightTurnIsNotInterruptedAndConvergesAtNextBoundary 覆盖
// A3 + V15/V19 的延迟收敛：在途 turn 的冻结前缀不变、actor 不被驱逐；run 结束后由
// 下一个命令入口兑现重建，使后续 turn 使用新 profile。
func TestSessionProfileSwitch_InFlightTurnIsNotInterruptedAndConvergesAtNextBoundary(t *testing.T) {
	ctx := context.Background()
	handler, runtime, _ := newProfileSwitchTestHandler(t)
	profileA := writeProfileSwitchFixture(t, "Profile A system prompt.")
	profileB := writeProfileSwitchFixture(t, "Profile B system prompt.")

	blocking := &blockingProfileSwitchProvider{
		testLLMProvider: testLLMProvider{name: "blocking-model", content: "ok"},
		started:         make(chan struct{}, 1),
		release:         make(chan struct{}),
	}
	require.NoError(t, runtime.RegisterProvider("blocking-model", blocking))

	session, err := handler.sessionManager.Create(ctx, "user-profile-switch-inflight")
	require.NoError(t, err)
	session.SetContext(apiProfileContextReference, profileA)
	session.SetContext(sessionmeta.ProviderName, "blocking-model")
	session.SetContext(sessionmeta.Model, "blocking-model")
	require.NoError(t, handler.sessionManager.Update(ctx, session))

	hub := handler.getSessionHub()
	actor, err := hub.GetOrCreate(session.ID)
	require.NoError(t, err)
	require.NoError(t, actor.SubmitPromptAsync(ctx, "hi", nil))

	select {
	case <-blocking.started:
	case <-time.After(10 * time.Second):
		t.Fatal("在途回合未起跑")
	}
	require.True(t, actor.RunInFlight(), "回合必须处于在途状态")

	report, err := handler.applySessionProfileSwitch(ctx, session.ID, profileB)
	require.NoError(t, err)
	assert.True(t, report.InFlightTurn)
	assert.False(t, report.ActorEvicted, "在途 turn 绝不打断（A3）")
	assert.Equal(t, sessionProfileSwitchScopeActor, report.ToolSurfaceScope)
	assert.True(t, report.ToolSurfaceInvalidated)
	assert.True(t, handler.hasPendingProfileSwitch(session.ID), "在途切换必须留下重建标记")
	if _, ok := hub.Get(session.ID); !ok {
		t.Fatal("在途 actor 不得被驱逐")
	}
	assert.True(t, requestHasSystemPrompt(blocking.firstRequest(), "Profile A system prompt."),
		"A3：在途 turn 的冻结前缀必须是切换前的 profile")

	close(blocking.release)
	deadline := time.Now().Add(10 * time.Second)
	for actor.RunInFlight() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	require.False(t, actor.RunInFlight(), "回合必须正常结束，不能被切换打断")

	handler.reconcilePendingProfileSwitch(session.ID)
	if _, ok := hub.Get(session.ID); ok {
		t.Fatal("边界兑现后旧 actor 必须被驱逐，使下一次构建取新 profile")
	}
	assert.False(t, handler.hasPendingProfileSwitch(session.ID), "兑现后标记必须清除")
}
