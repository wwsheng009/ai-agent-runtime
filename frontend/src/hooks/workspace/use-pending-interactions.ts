import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import {
  applyPendingInteractionEvent,
  convergePendingInteractions,
  emptyPendingInteractionState,
  failPendingInteractionResolve,
  hydratePendingInteractions,
  markPendingInteractionResolving,
  pendingPlanReviewFromPlan,
  settlePendingInteraction,
  selectPendingInteraction,
  type PendingInteraction,
  type PendingInteractionConvergeReason,
  type PendingInteractionState,
} from "@/lib/pending-interaction";
import {
  answerSessionQuestion as answerSessionQuestionApi,
  resolveSessionToolApproval,
  type RuntimeSessionPlanMode,
  type RuntimeSessionState,
  type SessionRuntimeEvent,
} from "@/lib/runtime-api";

type UsePendingInteractionsOptions = {
  /** 当前会话：事件归约全局累积，呈现按会话过滤（切换会话不误杀旧会话条目）。 */
  sessionId?: string;
  /** 计划模式投影（active 即 plan_review 条目，退出即消失）。 */
  plan?: RuntimeSessionPlanMode | null;
  /**
   * P2-1A：运行时状态快照（`GET /sessions/{id}/runtime`）。到达即做幂等重建：
   * 重载 / 重连后恢复未决审批与提问；陈旧快照不回退 `resolving` / 终态条目。
   */
  runtimeState?: RuntimeSessionState | null;
  getErrorMessage?: (error: unknown, fallback: string) => string;
};

function defaultGetErrorMessage(error: unknown, fallback: string): string {
  if (error instanceof Error && error.message.trim()) {
    return error.message;
  }
  if (typeof error === "string" && error.trim()) {
    return error;
  }
  return fallback;
}

/**
 * P1-7：待交互生命周期控制器（审批 / 提问 / 计划评审共用呈现位）。
 *
 * 单一数据源：运行时事件流（`applyRuntimeEvent` 由 `useSessionRuntimeStream`
 * 逐条投递）→ 纯函数归约；用户决定投递后端后由事件回填终态，网络失败回退到
 * `pending` 并保留错误原因（可重试）。取消（signal）与卸载通过 AbortController
 * 收口，避免在途请求在会话切换后污染状态。
 */
export function usePendingInteractions({
  sessionId,
  plan,
  runtimeState,
  getErrorMessage = defaultGetErrorMessage,
}: UsePendingInteractionsOptions = {}) {
  const [state, setState] = useState<PendingInteractionState>(
    emptyPendingInteractionState,
  );
  const controllersRef = useRef<Record<string, AbortController>>({});

  const applyRuntimeEvent = useCallback((event: SessionRuntimeEvent) => {
    setState((current) => applyPendingInteractionEvent(current, event));
  }, []);

  // P2-1A：快照重建（幂等）。事件流仍是主数据源，快照只补齐「不在线期间」的缺口。
  useEffect(() => {
    if (!runtimeState) {
      return;
    }
    setState((current) => hydratePendingInteractions(current, runtimeState));
  }, [runtimeState]);

  const converge = useCallback(
    (reason: PendingInteractionConvergeReason, targetSessionId?: string) => {
      for (const controller of Object.values(controllersRef.current)) {
        controller.abort();
      }
      controllersRef.current = {};
      setState((current) =>
        convergePendingInteractions(current, {
          sessionId: targetSessionId ?? sessionId,
          reason,
        }),
      );
    },
    [sessionId],
  );

  // 卸载：中止在途决定投递（结果回填依赖事件流，页面已不在也不再需要等待）。
  useEffect(
    () => () => {
      for (const controller of Object.values(controllersRef.current)) {
        controller.abort();
      }
      controllersRef.current = {};
    },
    [],
  );

  const resolveApproval = useCallback(
    async (requestId: string, allow: boolean): Promise<boolean> => {
      if (!sessionId || !requestId) {
        return false;
      }
      const controller = new AbortController();
      controllersRef.current[requestId] = controller;
      setState((current) => markPendingInteractionResolving(current, requestId));
      try {
        await resolveSessionToolApproval(
          sessionId,
          { requestId, allow },
          { signal: controller.signal },
        );
        // 乐观收敛；随后到达的 `approval_resolved` 事件按 id 幂等覆盖。
        setState((current) =>
          settlePendingInteraction(current, requestId, {
            status: "resolved",
            resolution: allow ? "allow" : "deny",
          }),
        );
        return true;
      } catch (error) {
        if (controller.signal.aborted) {
          return false;
        }
        setState((current) =>
          failPendingInteractionResolve(
            current,
            requestId,
            getErrorMessage(error, "failed to submit approval"),
          ),
        );
        return false;
      } finally {
        delete controllersRef.current[requestId];
      }
    },
    [getErrorMessage, sessionId],
  );

  const answerQuestion = useCallback(
    async (questionId: string, answer: string): Promise<boolean> => {
      if (!sessionId || !questionId) {
        return false;
      }
      const controller = new AbortController();
      controllersRef.current[questionId] = controller;
      setState((current) =>
        markPendingInteractionResolving(current, questionId),
      );
      try {
        await answerSessionQuestionApi(
          sessionId,
          { questionId, answer },
          { signal: controller.signal },
        );
        setState((current) =>
          settlePendingInteraction(current, questionId, {
            status: "resolved",
            resolution: answer ? `answered:${answer}` : "answered",
          }),
        );
        return true;
      } catch (error) {
        if (controller.signal.aborted) {
          return false;
        }
        setState((current) =>
          failPendingInteractionResolve(
            current,
            questionId,
            getErrorMessage(error, "failed to submit answer"),
          ),
        );
        return false;
      } finally {
        delete controllersRef.current[questionId];
      }
    },
    [getErrorMessage, sessionId],
  );

  const planReview = useMemo(
    () => pendingPlanReviewFromPlan(plan ?? null, sessionId),
    [plan, sessionId],
  );

  // 呈现位优先级：真实交互（审批/提问）> 计划评审投影——计划模式激活期间
  // 仍可能有工具审批阻塞，审批不能被恒存的投影长期遮蔽。
  const pending: PendingInteraction | null = useMemo(
    () => selectPendingInteraction(state, sessionId) ?? planReview,
    [planReview, sessionId, state],
  );

  return {
    answerQuestion,
    applyRuntimeEvent,
    converge,
    pending,
    planReview,
    resolveApproval,
    state,
  };
}
