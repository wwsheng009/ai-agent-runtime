package tools

import (
	"context"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/wwsheng009/ai-agent-runtime/internal/artifact"
	"github.com/wwsheng009/ai-agent-runtime/internal/observability"
	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
)

// localArtifactSink serves the shared artifact_read handler from the session's
// local artifact store. Everything the local tool has always enforced lives
// here: the read scope (this session or its direct parent), the fail-closed
// answers for unscoped and out-of-scope records, the corrupt-store recovery
// route, the dereference metrics, and the rule that a page never splits a rune
// and never stalls without advancing at least one.
//
// The scope is session state and the store is local storage, so neither can
// move into the shared handler; the handler only asks for a byte window.
type localArtifactSink struct {
	ctx context.Context
}

// Put is not part of reading. The local surface archives captures through its
// own store binding and the shared handler never writes.
func (s localArtifactSink) Put(context.Context, toolcore.ArtifactRef, []byte, toolcore.ArtifactMeta) (toolcore.ArtifactRef, error) {
	return toolcore.ArtifactRef{}, errors.New("artifact_read does not write artifacts")
}

func (s localArtifactSink) Open(_ context.Context, ref toolcore.ArtifactRef, offset, limit int) (toolcore.ArtifactChunk, error) {
	store := toolctx.ArtifactStore(s.ctx)
	if store == nil {
		observability.RecordToolArtifactDerefMiss(observability.DerefMissReasonNoStore)
		return toolcore.ArtifactChunk{}, artifactMiss(toolcore.ErrUnsupported,
			"run artifact_read inside the runtime agent loop",
			"artifact store 不可用：artifact_read 只能在运行时 agent 循环内执行")
	}
	scope, scoped := toolctx.ArtifactReadScope(s.ctx)
	if !scoped {
		// Older hosts bind only the session id. Fall back to self-only rather
		// than unrestricted: a missing scope must never widen the read boundary.
		if active := toolctx.SessionID(s.ctx); active != "" {
			scope = artifact.ReadScope{SessionIDs: map[string]bool{active: true}}
			scoped = true
		}
	}
	if !scoped {
		observability.RecordToolArtifactDerefMiss(observability.DerefMissReasonBadArgs)
		return toolcore.ArtifactChunk{}, artifactMiss(toolcore.ErrInvalidArgument,
			"run artifact_read inside an agent session",
			"artifact_read 需要会话上下文：无法确定读取范围")
	}

	record, err := store.GetOwned(s.ctx, ref.ID, scope)
	if err != nil {
		switch {
		case errors.Is(err, artifact.ErrArtifactReadOutOfScope):
			observability.RecordToolArtifactDerefMiss(observability.DerefMissReasonCrossSession)
			return toolcore.ArtifactChunk{}, artifactMiss(toolcore.ErrArtifactScopeDenied,
				"only dereference ids this session or its parent produced",
				fmt.Sprintf("artifact %s 不属于当前会话或父会话的读取范围；只能解引用本会话或直接父会话产生的指针：%v", ref.ID, err))
		case errors.Is(err, artifact.ErrArtifactReadUnscoped):
			observability.RecordToolArtifactDerefMiss(observability.DerefMissReasonUnscoped)
			return toolcore.ArtifactChunk{}, artifactMiss(toolcore.ErrArtifactScopeDenied,
				"the record has no session owner; re-run the command that produced the capture",
				fmt.Sprintf("artifact %s 缺少会话归属，已拒绝读取（fail-closed）", ref.ID))
		}
		if sqliteutil.IsCorruptError(err) {
			observability.RecordToolArtifactDerefMiss(observability.DerefMissReasonStoreCorrupt)
			return toolcore.ArtifactChunk{}, artifactMiss(toolcore.ErrInternal,
				"run `aicli storage compact --target artifacts --dry-run` before repairing the store",
				fmt.Sprintf("artifact store 已损坏，已拒绝读取；请先运行 `aicli storage compact --target artifacts --dry-run` 做只读确认，保留原库后按修复流程处理：%v", err))
		}
		observability.RecordToolArtifactDerefMiss(observability.DerefMissReasonNotFound)
		return toolcore.ArtifactChunk{}, artifactMiss(toolcore.ErrInternal,
			"check the artifact id and retry",
			fmt.Sprintf("读取 artifact 失败: %v", err))
	}
	if record == nil {
		observability.RecordToolArtifactDerefMiss(observability.DerefMissReasonNotFound)
		return toolcore.ArtifactChunk{}, artifactMiss(toolcore.ErrNotFound,
			"re-run the command that produced the capture",
			fmt.Sprintf("artifact not found: %s", ref.ID))
	}

	content := record.Content
	total := len(content)
	start := artifactRuneStartAtOrBefore(content, offset)
	end := artifactRuneStartAtOrBefore(content, offset+limit)
	if end < start {
		end = start
	}
	// A tiny limit that lands mid-rune can snap back onto the window start and
	// stall the page chain; always advance at least one full rune.
	if end <= start && start < total {
		if _, size := utf8.DecodeRuneInString(content[start:]); size > 0 {
			end = start + size
		}
	}
	if end > total {
		end = total
	}

	// Dereference call distribution (first vs followup hop) and the returned
	// window size - the followup ratio approximates average dereference hops.
	observability.RecordToolArtifactDeref(offset > 0)
	observability.RecordToolOutputBytes(observability.MetricToolArtifactDerefBytes, observability.ArchiveLayerGateway, end-start)

	return toolcore.ArtifactChunk{
		Data:             []byte(content[start:end]),
		Size:             int64(total),
		Offset:           int64(start),
		EOF:              end >= total,
		SourceTool:       record.ToolName,
		SourceToolCallID: record.ToolCallID,
	}, nil
}

// artifactMiss carries the stable code and the recovery route across the sink
// boundary, so the shared handler publishes the same class of failure the local
// tool always did instead of collapsing every miss into INVALID_ARGUMENT.
func artifactMiss(code, nextAction, message string) error {
	return &toolcore.CodedError{Code: code, NextAction: nextAction, Err: errors.New(message)}
}
