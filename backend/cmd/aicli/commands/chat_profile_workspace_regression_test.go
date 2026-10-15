package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
)

// 回归（用户报告，2026-10-15）：`/profile use xxx` 热切换后，会话的工作目录
// （workspace）漂移到新 profile 的根目录（~/.aicli/profiles/<name> 或
// <ws>/.aicli/profiles/<name>）。
//
// 根因是两层叠加：
//  1. resolveLocalWorkspacePath 把 ProfileRoot 当工作区 fallback / 相对
//     workspace.root 的锚点——profile 切换改写 ProfileRoot 后，工具面 base
//     path、sync 写回的 workspace_path、/status Directory 全部跟着漂；
//  2. syncChatRuntimeContext 每次 sync 重算并覆盖 sessionmeta.WorkspacePath，
//     把漂移值持久化。
//
// 修复（方案 1+2）：ProfileRoot 只代表配置来源，不再参与工作区推导；会话引入
// WorkspaceAnchor 权威锚点（启动/恢复固化，切换不改写），sync 只保持不重算。
// 本文件断言整条链路：解析函数、切换后的会话元数据、status 目录展示。

func TestResolveLocalWorkspacePathNeverAnchorsProfileRoot(t *testing.T) {
	t.Parallel()

	profileRoot := t.TempDir()
	session := &ChatSession{ProfileRoot: profileRoot}

	// 无 runtime 配置：不得回退到 ProfileRoot。
	if got := resolveLocalWorkspacePath(nil, session); got == profileRoot {
		t.Fatalf("resolveLocalWorkspacePath 不得把 profile 根当工作区: %q", got)
	}

	// 相对 workspace.root 相对进程 cwd，不相对 ProfileRoot。
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	got := resolveLocalWorkspacePath(&runtimecfg.RuntimeConfig{
		Workspace: runtimecfg.WorkspaceConfig{Root: "sub"},
	}, session)
	want := filepath.Clean(filepath.Join(cwd, "sub"))
	if got != want {
		t.Fatalf("相对 workspace.root 应相对 cwd 解析: got %q, want %q", got, want)
	}

	// nil session 仍按 cwd → git 根推导（既有行为；测试 cwd 位于仓库内）。
	cwdRaw, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	repoRoot := findGitRoot(cwdRaw)
	if repoRoot == "" {
		repoRoot = cwdRaw
	}
	if got := resolveLocalWorkspacePath(nil, nil); got != repoRoot {
		t.Fatalf("nil session 应回退 git 根/cwd: got %q, want %q", got, repoRoot)
	}
}

func TestResolveLocalWorkspacePathPrefersWorkspaceAnchor(t *testing.T) {
	t.Parallel()

	anchor := t.TempDir()
	session := &ChatSession{WorkspaceAnchor: anchor, ProfileRoot: t.TempDir()}
	if got := resolveLocalWorkspacePath(nil, session); got != anchor {
		t.Fatalf("WorkspaceAnchor 必须优先: got %q, want %q", got, anchor)
	}
}

// freezeChatWorkspaceAnchor：恢复路径优先持久化值；新会话路径解析后固化。
func TestFreezeChatWorkspaceAnchorPrefersPersistedWorkspace(t *testing.T) {
	t.Parallel()

	persisted := t.TempDir()
	session := &ChatSession{}
	if got := freezeChatWorkspaceAnchor(session, nil, persisted); got != filepath.Clean(persisted) {
		t.Fatalf("恢复路径应固化持久化 workspace: got %q", got)
	}
	if session.WorkspaceAnchor != filepath.Clean(persisted) {
		t.Fatalf("锚点未写入会话: %q", session.WorkspaceAnchor)
	}

	// 空持久化值 → 解析当前工作区并固化。
	fresh := &ChatSession{}
	if got := freezeChatWorkspaceAnchor(fresh, nil, ""); got == "" {
		t.Fatal("新会话应解析出工作区锚点")
	}
	if fresh.WorkspaceAnchor == "" {
		t.Fatal("锚点未固化")
	}
}

// 核心回归：/profile use 切换后，会话工作区（sessionmeta.WorkspacePath）不得
// 漂移到新 profile 的根目录。测试会话的 RuntimeSession 预置持久化 workspace
// （模拟恢复路径固化的锚点），切换后断言它原样保留。
func TestProfileSwitchKeepsSessionWorkspace(t *testing.T) {
	profilesRoot := t.TempDir()
	writeProfileSwitchFixture(t, profilesRoot, "coding", `profile:
  name: coding
  default_agent: coder
agents:
  coder:
    model: fixture
`, "Coding prompt.", "")

	writeProfileSwitchFixture(t, profilesRoot, "minimal", `profile:
  name: minimal
  default_agent: coder
agents:
  coder:
    model: fixture
`, "Minimal prompt.", "")

	session, cleanup := newProfileSwitchTestSession(t, profilesRoot)
	defer cleanup()

	workspace := t.TempDir()
	session.WorkspaceAnchor = filepath.Clean(workspace)
	session.RuntimeSession.Metadata.Context[sessionmeta.WorkspacePath] = session.WorkspaceAnchor

	if _, err := applyRuntimeProfileSwitch(session, "coding"); err != nil {
		t.Fatalf("switch to coding: %v", err)
	}
	if _, err := applyRuntimeProfileSwitch(session, "minimal"); err != nil {
		t.Fatalf("switch to minimal: %v", err)
	}

	if session.ProfileRoot == "" {
		t.Fatal("切换应更新 ProfileRoot（配置来源）")
	}
	if !sameChatSessionWorkspace(session.WorkspaceAnchor, workspace) {
		t.Fatalf("切换不得改写 WorkspaceAnchor: got %q, want %q", session.WorkspaceAnchor, workspace)
	}
	stored := sessionmeta.String(session.RuntimeSession.Metadata.Context, sessionmeta.WorkspacePath)
	if !sameChatSessionWorkspace(stored, workspace) {
		t.Fatalf("切换后 sessionmeta.WorkspacePath 漂移: got %q, want %q", stored, workspace)
	}
	if strings.EqualFold(filepath.Clean(stored), filepath.Clean(profilesRoot)) ||
		strings.EqualFold(filepath.Clean(stored), filepath.Clean(session.ProfileRoot)) {
		t.Fatalf("工作区不得锚定 profile 目录: %q (profile root %q)", stored, session.ProfileRoot)
	}
}

// 无锚点的手工会话：sync 不得覆盖已持久化的 workspace_path（保持语义）。
func TestSyncChatRuntimeContextKeepsPersistedWorkspaceWithoutAnchor(t *testing.T) {
	t.Parallel()

	persisted := t.TempDir()
	session := &ChatSession{}
	runtimeSession := runtimechat.NewSession("tester")
	runtimeSession.Metadata.Context[sessionmeta.WorkspacePath] = persisted

	// 锚点为空 + 已持久化值存在 → sync 保持，不重算。
	syncChatRuntimeContext(session, runtimeSession)
	if got := sessionmeta.String(runtimeSession.Metadata.Context, sessionmeta.WorkspacePath); !sameChatSessionWorkspace(got, persisted) {
		t.Fatalf("sync 不得覆盖已持久化 workspace: got %q, want %q", got, persisted)
	}

	// 锚点非空 → 以锚点为准。
	session.WorkspaceAnchor = t.TempDir()
	syncChatRuntimeContext(session, runtimeSession)
	if got := sessionmeta.String(runtimeSession.Metadata.Context, sessionmeta.WorkspacePath); !sameChatSessionWorkspace(got, session.WorkspaceAnchor) {
		t.Fatalf("锚点非空时应以锚点为准: got %q, want %q", got, session.WorkspaceAnchor)
	}
}

// status 的 Directory 行走权威锚点，profile 切换后不显示 profile 目录。
func TestResolveChatStatusCurrentDirectoryUsesWorkspaceAnchor(t *testing.T) {
	t.Parallel()

	anchor := t.TempDir()
	session := &ChatSession{WorkspaceAnchor: anchor, ProfileRoot: t.TempDir()}
	if got := resolveChatStatusCurrentDirectory(session); got != anchor {
		t.Fatalf("status Directory 应取 WorkspaceAnchor: got %q, want %q", got, anchor)
	}

	// 无锚点回退 cwd（不再回退 ProfileRoot）。
	plain := &ChatSession{ProfileRoot: t.TempDir()}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	if got := resolveChatStatusCurrentDirectory(plain); got != cwd {
		t.Fatalf("无锚点应回退 cwd 而非 ProfileRoot: got %q, want %q", got, cwd)
	}
}
