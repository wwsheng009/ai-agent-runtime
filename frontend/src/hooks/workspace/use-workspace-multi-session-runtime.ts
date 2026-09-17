// P0-2 拆分：多会话并发运行时（Batch 2/3/4）的页面接线自
// `pages/workspace-page.tsx` 机械搬迁而来，仅搬迁不改语义。
//
// 收口四件事（方案 §4.2 / §4.3 / §4.7）：
// - 后台订阅候选 + 调度（`useSessionStreamSupervisor`）；
// - 建连游标来源（轨迹窗口 lastEventSeq）+ 后台事件 → 待交互归约；
// - 侧栏活动投影（注册表条目 ∪ 本地信号）；
// - 通知（完成 / 待交互 toast）与「按会话就地停止」。

import { useEffect, useMemo } from "react";

import { requestSessionTurnInterrupt } from "@/api/runtime/session-turn-control";
import { buildSidebarSessionActivity } from "@/components/workspace/workspace-sidebar/session-row-status";
import type { Thread } from "@/data/mock";
import {
  type SessionRuntimeNoticesController,
  useSessionRuntimeNotices,
} from "@/hooks/workspace/use-session-runtime-notices";
import {
  setSessionRuntimeReplayCursorProvider,
  useSessionRuntimeEntries,
} from "@/hooks/workspace/use-session-runtime-registry";
import { useSessionStreamSupervisor } from "@/hooks/workspace/use-session-stream-supervisor";
import {
  mergeSessionActivity,
  projectSessionActivity,
  type SessionActivitySignal,
} from "@/lib/session-runtime/activity";
import { MULTI_SESSION_REGISTRY_ENABLED } from "@/lib/session-runtime/flags";
import { dismissSessionRuntimeNoticesForSession } from "@/lib/session-runtime/notices";
import type { SessionRuntimeEntrySnapshot } from "@/lib/session-runtime/types";
import { normalizeSessionId } from "@/lib/session-id";
import type { TrajectoryStorePool } from "@/lib/trajectory/store-pool";
import type { SessionRuntimeEvent } from "@/types/runtime";

export type UseWorkspaceMultiSessionRuntimeOptions = {
  /** 本地已知「在途回合」的会话键（草稿线程落库前也含 threadId）。 */
  activeSessionKeys: readonly string[];
  /** §4.3：后台会话事件也喂入待交互归约（D1-B：只归约，不写 threads）。 */
  applyPendingInteractionEvent: (event: SessionRuntimeEvent) => void;
  /** 选中会话的待交互类型（本地信号，优先于注册表投影）。 */
  pendingInteractionKind: "approval" | "question" | "plan_review" | null;
  /** 选中会话是否在途（本地 + 续传）。 */
  responding: boolean;
  /** 选中会话的完整停止路径（收敛未决卡片 + 本地 abort）。 */
  stopSelectedSession: () => void;
  /** 选中会话 id（未归一，可空）。 */
  selectedSessionId: string | null;
  /** 会话列表（候选集合与 sessionId → thread 反查共用）。 */
  threads: Thread[];
  /** 轨迹 store 池（后台建连游标来源）。 */
  trajectoryStorePool: TrajectoryStorePool;
  /** 打开线程（通知「前往会话」）。 */
  openThread: (threadId: string) => void;
};

export type WorkspaceMultiSessionRuntime = {
  /** 侧栏活动投影（注册表 ∪ 本地；开关关闭时等于本地信号）。 */
  sessionActivity: Record<string, SessionActivitySignal>;
  sessionRuntimeEntries: readonly SessionRuntimeEntrySnapshot[];
  sessionRuntimeNotices: SessionRuntimeNoticesController;
  /** sessionId → 线程（通知「前往会话」与标题解析共用）。 */
  threadBySessionId: Map<string, Thread>;
  handleOpenRuntimeNoticeSession: (sessionId: string) => void;
  handleStopRuntimeSession: (sessionId: string) => void;
};

export function useWorkspaceMultiSessionRuntime({
  activeSessionKeys,
  applyPendingInteractionEvent,
  pendingInteractionKind,
  responding,
  stopSelectedSession,
  selectedSessionId,
  threads,
  trajectoryStorePool,
  openThread,
}: UseWorkspaceMultiSessionRuntimeOptions): WorkspaceMultiSessionRuntime {
  // P1-9：侧栏行状态的基础信号只来自本地已知的当前会话；Batch 3 起与注册表投影
  // 合并（后台会话的「运行中 / 等待审批」来自注册表条目，§4.7.1）。
  const localSessionActivity = useMemo(
    () =>
      buildSidebarSessionActivity({
        sessionId: selectedSessionId ?? undefined,
        pendingInteractionKind,
        responding,
      }),
    [pendingInteractionKind, responding, selectedSessionId],
  );

  // Batch 3（§4.2 / §4.7）：订阅候选 = 除选中会话外的会话列表；本地在途回合与
  // 「最近活动」分别决定 live / poll（预算由注册表收口）。
  //
  // 前台（选中）会话连接仍由 useWorkspaceLive 持有（连接预算里的那条 live）：
  // 注册表本轮只接管后台订阅（≤2 条 live，其余 poll）。同一会话同时挂两条 SSE
  // 会超出 §4.6 预算，因此候选集合与 selected 都不含当前选中会话——把前台也
  // 搬进注册表（§4.2 "selected" 分支）是后续迁移点。
  const selectedSessionIdForRegistry =
    normalizeSessionId(selectedSessionId) || null;
  const localRunningSessionIds = useMemo(
    () =>
      Array.from(
        new Set(
          activeSessionKeys
            .map((key) => normalizeSessionId(key) || key.trim())
            .filter((key) => key.length > 0),
        ),
      ),
    [activeSessionKeys],
  );
  const supervisorCandidates = useMemo(() => {
    const activeIds = new Set(localRunningSessionIds);
    return threads
      .map((thread) => {
        const sessionId =
          normalizeSessionId(thread.sessionId) ||
          normalizeSessionId(thread.id) ||
          "";
        return {
          sessionId,
          updatedAt: thread.updatedAt,
          hasActiveTurn: activeIds.has(sessionId),
        };
      })
      .filter(
        (candidate) =>
          candidate.sessionId.length > 0 &&
          candidate.sessionId !== selectedSessionIdForRegistry,
      );
  }, [localRunningSessionIds, selectedSessionIdForRegistry, threads]);
  const sessionRegistry = useSessionStreamSupervisor({
    // 选中会话由前台 hook 持有连接：显式传 null，避免同会话双连接。
    selectedSessionId: null,
    candidates: supervisorCandidates,
  });

  // §4.3 步 1：后台建连游标 = 该会话轨迹窗口已回放到的 seq（避免从 0 整份 dump）。
  useEffect(() => {
    setSessionRuntimeReplayCursorProvider((sessionId) => {
      const lastEventSeq =
        trajectoryStorePool.peek(sessionId)?.getSnapshot().lastEventSeq ?? 0;
      return lastEventSeq > 0 ? lastEventSeq : undefined;
    });
    return () => setSessionRuntimeReplayCursorProvider(null);
  }, [trajectoryStorePool]);

  const sessionRuntimeEntries = useSessionRuntimeEntries();
  // §4.3：后台会话的事件也喂入待交互归约（D1-B：只做轻量归约、不写 threads），
  // 否则侧栏报「等待审批」而审批条目不在表里；呈现仍按选中会话过滤。
  useEffect(() => {
    if (!MULTI_SESSION_REGISTRY_ENABLED) {
      return;
    }
    return sessionRegistry.onEvent((_sessionId, event) => {
      applyPendingInteractionEvent(event);
    });
  }, [applyPendingInteractionEvent, sessionRegistry]);

  const sessionActivity = useMemo(() => {
    if (!MULTI_SESSION_REGISTRY_ENABLED) {
      return localSessionActivity;
    }
    const registryActivity = projectSessionActivity(sessionRuntimeEntries, {
      runningSessionIds: localRunningSessionIds,
    });
    // 选中会话以本地信号为准：两者应一致，本地信号更新更及时。
    return mergeSessionActivity(registryActivity, localSessionActivity);
  }, [localRunningSessionIds, localSessionActivity, sessionRuntimeEntries]);

  // §4.7.2/3：sessionId → 线程，供通知「前往会话」与标题解析共用（缺省回落 sessionId）。
  const threadBySessionId = useMemo(() => {
    const byId = new Map<string, Thread>();
    for (const thread of threads) {
      const sessionId = normalizeSessionId(thread.sessionId);
      if (sessionId) {
        byId.set(sessionId, thread);
      }
      byId.set(normalizeSessionId(thread.id), thread);
    }
    return byId;
  }, [threads]);

  // §4.7.3：非选中会话的「完成 / 待交互」→ 页内 toast（选中会话由前台消息流与卡片呈现）。
  const sessionRuntimeNotices = useSessionRuntimeNotices({
    entries: sessionRuntimeEntries,
    selectedSessionId,
  });

  function handleOpenRuntimeNoticeSession(sessionId: string) {
    const thread = threadBySessionId.get(normalizeSessionId(sessionId));
    openThread(thread?.id ?? sessionId);
  }

  // §4.8：侧栏行内停止 —— 按会话投递 interrupt，后台会话没有本地 controller 也能停。
  // 选中会话且（本地或续传的）在途回合就挂在停止按钮上，复用完整路径（收敛未决卡片 +
  // 本地 abort）；其余会话只发服务端命令，回合状态由该会话的注册表条目自然收敛。
  // best-effort：`requestSessionTurnInterrupt` 吞掉 409 / 网络错误，不做重试风暴。
  function handleStopRuntimeSession(sessionId: string) {
    if (!MULTI_SESSION_REGISTRY_ENABLED) {
      return;
    }
    const targetSessionId = normalizeSessionId(sessionId);
    if (!targetSessionId) {
      return;
    }
    if (targetSessionId === normalizeSessionId(selectedSessionId)) {
      stopSelectedSession();
      return;
    }
    const entry = sessionRuntimeEntries.find(
      (item) => normalizeSessionId(item.sessionId) === targetSessionId,
    );
    void requestSessionTurnInterrupt(
      targetSessionId,
      entry?.activeTurn?.turnId ?? null,
    );
    // 用户已就地处置该会话，清掉它的待办提示，避免「已经停了还挂着等审批」。
    dismissSessionRuntimeNoticesForSession(targetSessionId);
  }

  return {
    sessionActivity,
    sessionRuntimeEntries,
    sessionRuntimeNotices,
    threadBySessionId,
    handleOpenRuntimeNoticeSession,
    handleStopRuntimeSession,
  };
}
