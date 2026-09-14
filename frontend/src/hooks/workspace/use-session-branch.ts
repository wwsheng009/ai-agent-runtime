// 批次 1（§5.3）：会话分支编排 hook —— 消息级锚点分支与侧栏整会话分支共用一条链路。
//
// 语义：`POST /api/runtime/sessions/{id}/branch` 由**服务端**裁剪历史前缀写进新会话，
// 源会话零改动；前端只传锚点、只消费新会话。与既有会话行动作保持一致：
// **不做** `setThreads` 乐观插入，新会话靠 `refreshSessions()` 快照 + pinned 深链兜底
// 进入侧栏（`runtime-sessions-data/loading.ts` 的既有机制）。
//
// 并发：分支是用户触发的低频动作，同一时刻只允许一个在途请求（`pendingRef` 门），
// 避免连点产生两条同源分支会话。

import { useCallback, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";

import { branchRuntimeSession } from "@/api/runtime/session-branch";
import { buildBranchSessionRequest } from "@/components/workspace/workspace-sidebar/session-row-actions";
import { getErrorMessage } from "@/hooks/workspace/thread-runtime";
import { normalizeSessionId } from "@/lib/session-id";

type UseSessionBranchOptions = {
  /** 运行客户端身份里的用户 id（`selectedUserId` 为空时的回落）。 */
  clientUserId: string;
  /** 轨迹硬重置（分支跳转后事件日志独立自增，必须显式清游标）。 */
  onResetTrajectory: () => void;
  /** 会话列表刷新（新会话由快照物化，不写乐观线程）。 */
  refreshSessions: () => void;
  /** 多用户视图下选中的用户 id（优先于 clientUserId）。 */
  selectedUserId: string;
};

type PendingBranch = {
  sessionId: string;
  /** 消息级分支的锚点消息 id；整会话分支为 null。 */
  messageId: string | null;
};

export function useSessionBranch({
  clientUserId,
  onResetTrajectory,
  refreshSessions,
  selectedUserId,
}: UseSessionBranchOptions) {
  const navigate = useNavigate();
  const { t } = useTranslation("workspace");
  const [pending, setPending] = useState<PendingBranch | null>(null);
  const [branchError, setBranchError] = useState<string | null>(null);
  const pendingRef = useRef(false);

  const branch = useCallback(
    async (
      sessionId: string,
      sourceTitle: string,
      anchorMessageId?: string,
    ) => {
      const source = normalizeSessionId(sessionId);
      if (!source || pendingRef.current) return;

      pendingRef.current = true;
      const anchor = anchorMessageId?.trim() ?? "";
      setBranchError(null);
      setPending({ sessionId: source, messageId: anchor || null });

      try {
        const response = await branchRuntimeSession(
          source,
          buildBranchSessionRequest({
            sessionId: source,
            sourceTitle,
            userId: selectedUserId || clientUserId,
            branchSuffix: t("sidebar.session.forkSuffix"),
            ...(anchor ? { anchorMessageId: anchor } : {}),
          }),
        );
        const createdSessionId = normalizeSessionId(response.session?.id ?? "");
        refreshSessions();
        if (createdSessionId) {
          // 与「目录内新建会话」同口径：先重置轨迹，再跳 canonical 会话路由，
          // 等待快照刷新后由 mergeRuntimeSessionsIntoThreads 生成对应线程。
          onResetTrajectory();
          navigate(
            `/workspace/sessions/${encodeURIComponent(createdSessionId)}`,
          );
        }
      } catch (error) {
        // 失败不改路由（§6.1 错误表）：只提示原因，用户可自行重试。
        setBranchError(
          getErrorMessage(error, t("panels.messages.branch.failed")),
        );
      } finally {
        pendingRef.current = false;
        setPending(null);
      }
    },
    [
      clientUserId,
      navigate,
      onResetTrajectory,
      refreshSessions,
      selectedUserId,
      t,
    ],
  );

  const branchFromMessage = useCallback(
    (sessionId: string, sourceTitle: string, anchorMessageId: string) =>
      branch(sessionId, sourceTitle, anchorMessageId),
    [branch],
  );

  const forkSession = useCallback(
    (sessionId: string, sourceTitle: string) => branch(sessionId, sourceTitle),
    [branch],
  );

  const clearBranchError = useCallback(() => setBranchError(null), []);

  return {
    branchError,
    branchFromMessage,
    branchPendingMessageId: pending?.messageId ?? null,
    branchPendingSessionId: pending?.sessionId ?? null,
    clearBranchError,
    forkSession,
  };
}
