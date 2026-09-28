package commands

import (
	"context"
	"strings"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
)

// chatKnowledgeActivation 是 aicli 进程内的知识层接入句柄。
//
// Phase 1 交付 6（接入/激活）要求 `cmd/aicli`（cmd/tui）与 acp 两个入口在
// workspace 解析之后接入知识层。两者共享本类型，用引用计数管理生命周期：
// ACP 宿主在一个进程里可以服务多个会话（各自 workspace 不同），因此不能
// 用"进程级单例 + 首次 Close 释放"的写法，否则第二个会话接入时会拿到已经
// 关闭的句柄。
//
// 不变量：
//   - `knowledge.mode=off`（默认）时 Activate 在打开 store 之前就返回
//     `(nil, nil)`，因此 off 路径零副作用（不建库、不建锁文件、不起
//     goroutine），与"无知识层"逐字节一致。
//   - Activate 返回 `(nil, nil)`（防御分支）时同样不缓存。
type chatKnowledgeActivation struct {
	mu   sync.Mutex
	act  *knowledge.Activation
	refs int
}

var (
	chatKnowledgeMu    sync.Mutex
	chatKnowledgeByWS  = map[string]*chatKnowledgeActivation{}
	chatKnowledgeShut  bool
	chatKnowledgeClose = func(ws string, act *knowledge.Activation) {
		if act == nil {
			return
		}
		if err := act.Close(); err != nil {
			logpkg.Debugf("knowledge: close activation for %q: %v", ws, err)
		}
	}
)

// acquireChatKnowledge 取得（必要时建立）workspace 上的知识层接入，并持有一次引用。
//
// 返回 nil 表示本次没有知识层（mode=off 或 Activate 防御性返回 nil），调用方
// 不需要任何分支——知识层的所有消费点都接受 nil。
func acquireChatKnowledge(cfg *knowledge.Config, workspace string) (*knowledge.Activation, error) {
	if cfg == nil {
		return nil, nil
	}
	ws := strings.TrimSpace(workspace)
	chatKnowledgeMu.Lock()
	if chatKnowledgeShut {
		// 进程已开始收尾（runExitCleanup 已跑过进程级释放）：不再新开 store，
		// 让调用方退化成"无知识层"，而不是短暂持有又立刻被关掉。
		chatKnowledgeMu.Unlock()
		return nil, nil
	}
	if entry, ok := chatKnowledgeByWS[ws]; ok {
		entry.mu.Lock()
		entry.refs++
		act := entry.act
		entry.mu.Unlock()
		chatKnowledgeMu.Unlock()
		return act, nil
	}
	chatKnowledgeMu.Unlock()

	// Activate 会做磁盘 IO（open store + 后台索引），放在锁外执行；并发首建
	// 同一 workspace 的最坏结果是多开一次 store——owner 仲裁会把后到者降级为
	// reader，因此不会破坏单写者不变量。
	act, err := knowledge.Activate(context.Background(), *cfg, ws, knowledge.ActivationOptions{
		OnIndexDone: func(result knowledge.IndexResult, indexErr error) {
			if indexErr != nil {
				logpkg.Debugf("knowledge: initial index for %q failed: %v", ws, indexErr)
				return
			}
			logpkg.Debugf("knowledge: initial index for %q done: scanned=%d symbols=%d refs=%d",
				ws, result.Scanned, result.Symbols, result.Refs)
		},
	})
	if err != nil {
		return nil, err
	}
	if act == nil {
		return nil, nil
	}

	entry := &chatKnowledgeActivation{act: act, refs: 1}
	chatKnowledgeMu.Lock()
	if chatKnowledgeShut {
		chatKnowledgeMu.Unlock()
		chatKnowledgeClose(ws, act)
		return nil, nil
	}
	if existing, ok := chatKnowledgeByWS[ws]; ok {
		// 并发首建竞态：保留先到者，关掉自己刚开的这一个。
		existing.mu.Lock()
		existing.refs++
		shared := existing.act
		existing.mu.Unlock()
		chatKnowledgeMu.Unlock()
		chatKnowledgeClose(ws, act)
		return shared, nil
	}
	chatKnowledgeByWS[ws] = entry
	chatKnowledgeMu.Unlock()
	return act, nil
}

// releaseChatKnowledge 释放一次引用；引用归零时关闭该 workspace 的接入。
func releaseChatKnowledge(workspace string, act *knowledge.Activation) {
	if act == nil {
		return
	}
	ws := strings.TrimSpace(workspace)

	chatKnowledgeMu.Lock()
	entry, ok := chatKnowledgeByWS[ws]
	if !ok {
		chatKnowledgeMu.Unlock()
		return
	}
	entry.mu.Lock()
	entry.refs--
	last := entry.refs <= 0
	entry.mu.Unlock()
	if last {
		delete(chatKnowledgeByWS, ws)
	}
	chatKnowledgeMu.Unlock()

	if last {
		chatKnowledgeClose(ws, entry.act)
	}
}

// releaseAllChatKnowledge 关闭本进程注册的全部接入（进程收尾/exit cleanup）。
//
// 这是一次性开关：置位后 acquireChatKnowledge 不再新开 store。已经在跑的
// 后台首次索引会被 Close 取消并等待退出，避免"进程退出了索引还在写库"。
func releaseAllChatKnowledge() {
	chatKnowledgeMu.Lock()
	chatKnowledgeShut = true
	pending := make([]*chatKnowledgeActivation, 0, len(chatKnowledgeByWS))
	workspaces := make([]string, 0, len(chatKnowledgeByWS))
	for ws, entry := range chatKnowledgeByWS {
		workspaces = append(workspaces, ws)
		pending = append(pending, entry)
		delete(chatKnowledgeByWS, ws)
	}
	chatKnowledgeMu.Unlock()

	for i, entry := range pending {
		chatKnowledgeClose(workspaces[i], entry.act)
	}
}

// attachChatKnowledge 在 workspace 解析之后接入知识层，并把句柄挂到会话上。
//
// workspace 锚点与工具层**同源**（`loadRuntimeToolConfig` 里写入的
// `Workspace.Root`），避免"索引的根"和"工具看到的根"不一致。
// 任何失败（配置非法 / store 打不开 / 锁冲突）都只降级为"无知识层"，
// 绝不让 chat 启动失败。
func attachChatKnowledge(session *ChatSession) {
	if session == nil || session.Config == nil {
		return
	}
	// 知识层配置与 workspace 锚点都取自 runtime.yaml（`runtimecfg.RuntimeConfig`），
	// 与工具层同源：aicli 自己的 agentconfig 不含 knowledge 段。
	runtimeConfig := loadRuntimeToolConfig(session.Config, session)
	workspace := strings.TrimSpace(runtimeConfig.Workspace.Root)
	if workspace == "" {
		return
	}
	act, err := acquireChatKnowledge(&runtimeConfig.Knowledge, workspace)
	if err != nil {
		logpkg.Debugf("knowledge: activation for %q skipped: %v", workspace, err)
		return
	}
	session.Knowledge = act
}
