package runtimeapi

import (
	"context"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

// P0-4：runtime-server 宿主的 completion-payload 读通道。
//
// 子代理终态投递写进父会话的 AgentControl session mailbox
// （session_runtime_support.go 的 DeliverMailboxEventFirst →
// SessionEventMailboxStore.AppendAgentControlMailbox）。read_agent_result 的
// 兜底读取必须看同一份行，因此这里暴露一个惰性解析的只读访问器：读取时再取
// getSessionEventStore()，既能拿到首次使用后才惰性创建的 store，也不需要把
// session store 字段写进 Handler 的构造契约。

// SupervisionAgentControlMailboxReader returns the lazy session mailbox reader
// behind the P0-4 completion-payload fallback. The returned reader is never
// nil: when the host has no session event store it answers an empty read.
func (h *Handler) SupervisionAgentControlMailboxReader() chat.AgentControlMailboxReaderStore {
	return supervisionAgentControlMailboxReader{handler: h}
}

type supervisionAgentControlMailboxReader struct {
	handler *Handler
}

func (r supervisionAgentControlMailboxReader) ListAgentControlMailbox(ctx context.Context, sessionID string, afterSeq int64, limit int) ([]team.MailMessage, error) {
	if r.handler == nil {
		return nil, nil
	}
	reader, _ := r.handler.getSessionEventStore().(chat.AgentControlMailboxReaderStore)
	if reader == nil {
		return nil, nil
	}
	return reader.ListAgentControlMailbox(ctx, sessionID, afterSeq, limit)
}
