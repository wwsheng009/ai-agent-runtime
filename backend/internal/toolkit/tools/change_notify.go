package tools

import (
	"context"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
)

// notifyFileChanges 报告编辑类工具落盘成功后的文件变更（04 §5 Phase 5 变更源 1：
// agent edit hook 的**同步标记**侧）。
//
// 接收方由会话装配方注入到工具执行 ctx（`toolctx.WithFileChangeNotifier`，见
// `internal/agent` 的 toolCallContext / approvedToolCallContext），实现是知识层的
// `MarkChanged`——它只做去重入队（非阻塞），真正的索引在 debounce 后由单 worker 串行执行。
//
// 未注入（知识层 off / reader 角色 / 非会话路径）时是 no-op：工具行为与无知识层逐字节一致。
// 传绝对路径；相对路径也接受（`IndexPaths` 会按工作区归一化并拒绝越界路径）。
func notifyFileChanges(ctx context.Context, paths ...string) {
	if ctx == nil || len(paths) == 0 {
		return
	}
	notify := toolctx.FileChangeNotifierFromContext(ctx)
	if notify == nil {
		return
	}
	notify(paths...)
}
