// 归档计划（planstore）浏览器面板的数据源：列表 + 详情 + 事件驱动的刷新。
//
// 与 `use-runtime-plan-mode.ts` 同构（本轮不改变其语义）：
//   * 事件通过面板宿主下传的 `lastRuntimeEventType` / `runtimeEventCount` 投影成
//     「事件键」（buildRuntimeEventReloadKey），命中重载集合且键变化时刷新一次；
//   * 刷新后把当前事件键记为已处理，避免同一事件重复拉取（不写 lastRuntimeEventKey
//     会在面板重渲染时反复请求）。
// 与 plan-mode 的差别：归档计划是**全局**资源（不按会话分片），因此没有 sessionId 维度，
// 面板未打开时不挂载、也就没有请求 —— 「面板已打开才刷新」由挂载时机天然保证。

import { useCallback, useEffect, useRef, useState } from "react";

import { buildRuntimeEventReloadKey } from "@/hooks/workspace/use-runtime-checkpoints";
import {
  getRuntimePlanDiff,
  getRuntimePlan,
  isStoredPlanReopenConflict,
  listRuntimePlans,
  type RuntimePlanDiffResult,
  readStoredPlanReopenHint,
  reopenRuntimePlan,
} from "@/lib/runtime-api";
import { useRuntimePlanComments } from "@/hooks/workspace/use-runtime-plan-comments";
import type { RuntimeStoredPlan } from "@/types/runtime";

/** 运行时事件名：计划被请求评审（后端 `chat.EventPlanReviewRequested`）。 */
export const PLAN_REVIEW_REQUESTED_EVENT = "plan_review_requested";

/** 会改变归档计划读数的运行时事件（其余事件不触发重载）。 */
const PLAN_RELOAD_EVENTS = new Set([
  PLAN_REVIEW_REQUESTED_EVENT,
  "plan_review_available",
  "plan_updated",
  "plan_mode_changed",
]);

export type UseRuntimePlansOptions = {
  lastRuntimeEventType?: string;
  runtimeEventCount?: number;
};

/** 重新评审（归档回灌）的进行态，供渲染层展示 running/成功/冲突/失败。 */
export type RuntimePlanReopenState = {
  status: "idle" | "running" | "succeeded" | "conflict" | "failed";
  planId: string;
  version: number;
  unchanged: boolean;
  forced: boolean;
  message: string;
  hint: string;
};

export type RuntimePlanReopenOutcome =
  | {
      ok: true;
      planId: string;
      version: number;
      created: boolean;
      unchanged: boolean;
      forced: boolean;
    }
  | { ok: false; conflict: boolean; message: string; hint: string };

const IDLE_REOPEN_STATE: RuntimePlanReopenState = {
  status: "idle",
  planId: "",
  version: 0,
  unchanged: false,
  forced: false,
  message: "",
  hint: "",
};

/** 轮次差异（`/plans/{id}/diff`）的一次读取状态。 */
export type RuntimePlanDiffState = {
  status: "idle" | "loading" | "ready" | "error";
  planId: string;
  from: number;
  to: number;
  result: RuntimePlanDiffResult | null;
  error: string;
};

const IDLE_DIFF_STATE: RuntimePlanDiffState = {
  status: "idle",
  planId: "",
  from: 0,
  to: 0,
  result: null,
  error: "",
};

type ShouldReloadRuntimePlansOptions = {
  lastHandledEventKey?: string;
  lastRuntimeEventKey?: string;
  lastRuntimeEventType?: string;
};

export function shouldReloadRuntimePlans({
  lastHandledEventKey,
  lastRuntimeEventKey,
  lastRuntimeEventType,
}: ShouldReloadRuntimePlansOptions) {
  if (!lastRuntimeEventType || !lastRuntimeEventKey) {
    return false;
  }
  if (!PLAN_RELOAD_EVENTS.has(lastRuntimeEventType)) {
    return false;
  }
  return lastRuntimeEventKey !== lastHandledEventKey;
}

function readErrorMessage(error: unknown, fallback: string) {
  return error instanceof Error && error.message.trim() ? error.message : fallback;
}

export function useRuntimePlans({
  lastRuntimeEventType,
  runtimeEventCount,
}: UseRuntimePlansOptions = {}) {
  const [plans, setPlans] = useState<RuntimeStoredPlan[]>([]);
  const [plansLoading, setPlansLoading] = useState(false);
  const [plansError, setPlansError] = useState<string | null>(null);
  const [loadedOnce, setLoadedOnce] = useState(false);
  const [lastHandledEventKey, setLastHandledEventKey] = useState("");
  const [selectedPlanId, setSelectedPlanId] = useState<string | null>(null);
  const [selectedPlan, setSelectedPlan] = useState<RuntimeStoredPlan | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [detailError, setDetailError] = useState<string | null>(null);
  const [reopenState, setReopenState] = useState<RuntimePlanReopenState>(IDLE_REOPEN_STATE);
  const [diffState, setDiffState] = useState<RuntimePlanDiffState>(IDLE_DIFF_STATE);
  // 行级评论是独立状态机（见 use-runtime-plan-comments）：只在选中计划/事件刷新时联动。
  const {
    clear: clearComments,
    commentsState,
    create: createComment,
    load: loadComments,
    remove: deleteComment,
  } = useRuntimePlanComments();

  const lastRuntimeEventKey = buildRuntimeEventReloadKey(
    lastRuntimeEventType,
    runtimeEventCount,
  );
  const lastRuntimeEventKeyRef = useRef(lastRuntimeEventKey);
  const selectedPlanIdRef = useRef<string | null>(null);
  const listRequestSeq = useRef(0);
  const detailRequestSeq = useRef(0);
  const reopenRequestSeq = useRef(0);
  const diffRequestSeq = useRef(0);

  useEffect(() => {
    lastRuntimeEventKeyRef.current = lastRuntimeEventKey;
  }, [lastRuntimeEventKey]);

  useEffect(() => {
    selectedPlanIdRef.current = selectedPlanId;
  }, [selectedPlanId]);

  const loadPlans = useCallback(async (handledEventKey: string) => {
    const requestSeq = listRequestSeq.current + 1;
    listRequestSeq.current = requestSeq;
    setPlansLoading(true);
    setPlansError(null);

    try {
      const response = await listRuntimePlans();
      if (requestSeq !== listRequestSeq.current) {
        return;
      }
      setPlans(response.plans);
      setLoadedOnce(true);
      setLastHandledEventKey(handledEventKey);
    } catch (error) {
      if (requestSeq !== listRequestSeq.current) {
        return;
      }
      setPlansError(readErrorMessage(error, "failed to load archived plans"));
      // 失败也标记「已加载过」，让空态与错误态互斥，重试由显式按钮触发。
      setLoadedOnce(true);
      setLastHandledEventKey(handledEventKey);
    } finally {
      if (requestSeq === listRequestSeq.current) {
        setPlansLoading(false);
      }
    }
  }, []);

  const loadDetail = useCallback(async (id: string) => {
    const requestSeq = detailRequestSeq.current + 1;
    detailRequestSeq.current = requestSeq;
    setDetailLoading(true);
    setDetailError(null);

    try {
      const record = await getRuntimePlan(id);
      if (requestSeq !== detailRequestSeq.current) {
        return;
      }
      setSelectedPlan(record);
    } catch (error) {
      if (requestSeq !== detailRequestSeq.current) {
        return;
      }
      setSelectedPlan(null);
      setDetailError(readErrorMessage(error, "failed to load plan detail"));
    } finally {
      if (requestSeq === detailRequestSeq.current) {
        setDetailLoading(false);
      }
    }
  }, []);


  // 首次挂载拉一次列表；之后由运行时事件驱动（见下方 effect）。
  useEffect(() => {
    void loadPlans("");
  }, [loadPlans]);

  useEffect(() => {
    if (
      !shouldReloadRuntimePlans({
        lastHandledEventKey,
        lastRuntimeEventKey,
        lastRuntimeEventType,
      })
    ) {
      return;
    }

    void loadPlans(lastRuntimeEventKey);

    const currentDetailId = selectedPlanIdRef.current;
    if (currentDetailId) {
      void loadDetail(currentDetailId);
      void loadComments(currentDetailId);
    }
  }, [
    lastHandledEventKey,
    lastRuntimeEventKey,
    lastRuntimeEventType,
    loadComments,
    loadDetail,
    loadPlans,
  ]);

  /** 选择（或清空）详情：立即清掉上一条详情，避免旧正文与新标题错配。 */
  const select = useCallback(
    (id: string | null) => {
      const nextId = id?.trim() ? id.trim() : null;
      // 让在途详情请求失效，防止慢响应覆盖新选择。
      detailRequestSeq.current += 1;
      setSelectedPlanId(nextId);
      setSelectedPlan(null);
      setDetailError(null);
      setDetailLoading(false);

      if (nextId) {
        void loadDetail(nextId);
        void loadComments(nextId);
      } else {
        clearComments();
      }
    },
    [clearComments, loadComments, loadDetail],
  );

  /** 手动刷新：列表 + （若已打开详情）当前详情。 */
  const refresh = useCallback(async () => {
    const currentDetailId = selectedPlanIdRef.current;
    await Promise.all([
      loadPlans(lastRuntimeEventKeyRef.current),
      currentDetailId ? loadDetail(currentDetailId) : Promise.resolve(),
      currentDetailId ? loadComments(currentDetailId) : Promise.resolve(),
    ]);
  }, [loadComments, loadDetail, loadPlans]);

  /**
   * 回灌一条归档快照并进入 plan mode（POST /sessions/{id}/plan/reopen）。
   * 成功后刷新列表与已打开详情——reopen 会同时改写计划文件与 plan mode 状态。
   * 冲突（409）不写盘：把 hint 交给渲染层，由用户确认后带 force=true 重试。
   */
  const reopen = useCallback(
    async (
      sessionId: string,
      planId: string,
      options: { version?: number; force?: boolean } = {},
    ): Promise<RuntimePlanReopenOutcome> => {
      const seq = reopenRequestSeq.current + 1;
      reopenRequestSeq.current = seq;
      const trimmedPlanId = planId.trim();
      const trimmedSessionId = sessionId.trim();
      if (!trimmedSessionId) {
        const outcome: RuntimePlanReopenOutcome = {
          ok: false,
          conflict: false,
          message: "runtime plan reopen requires an active session",
          hint: "",
        };
        setReopenState({
          ...IDLE_REOPEN_STATE,
          status: "failed",
          planId: trimmedPlanId,
          message: outcome.message,
        });
        return outcome;
      }

      setReopenState({
        ...IDLE_REOPEN_STATE,
        status: "running",
        planId: trimmedPlanId,
        version: options.version ?? 0,
      });

      try {
        const result = await reopenRuntimePlan(trimmedSessionId, trimmedPlanId, options);
        if (seq === reopenRequestSeq.current) {
          setReopenState({
            ...IDLE_REOPEN_STATE,
            status: "succeeded",
            planId: result.plan_id,
            version: result.version,
            unchanged: result.unchanged,
            forced: result.forced,
          });
        }
        await refresh();
        return {
          ok: true,
          planId: result.plan_id,
          version: result.version,
          created: result.created,
          unchanged: result.unchanged,
          forced: result.forced,
        };
      } catch (error) {
        const conflict = isStoredPlanReopenConflict(error);
        const message = readErrorMessage(error, "failed to reopen archived plan");
        const hint = conflict ? readStoredPlanReopenHint(error) : "";
        if (seq === reopenRequestSeq.current) {
          setReopenState({
            ...IDLE_REOPEN_STATE,
            status: conflict ? "conflict" : "failed",
            planId: trimmedPlanId,
            version: options.version ?? 0,
            message,
            hint,
          });
        }
        return { ok: false, conflict, message, hint };
      }
    },
    [refresh],
  );

  /** 关闭重新评审提示（切换选择或返回列表时调用）。 */
  const clearReopenState = useCallback(() => {
    reopenRequestSeq.current += 1;
    setReopenState(IDLE_REOPEN_STATE);
  }, []);

  /**
   * 读取一条归档计划的轮次差异（`from`/`to` 为 0 时由后端推导：上一轮 → 最新轮）。
   * 失败只落在 `diffState`，不影响列表/详情；`null` 返回值让调用方可以静默处理。
   */
  const loadDiff = useCallback(
    async (
      planId: string,
      options: { from?: number; to?: number } = {},
    ): Promise<RuntimePlanDiffResult | null> => {
      const seq = diffRequestSeq.current + 1;
      diffRequestSeq.current = seq;
      const trimmedPlanId = planId.trim();
      setDiffState({
        ...IDLE_DIFF_STATE,
        status: "loading",
        planId: trimmedPlanId,
        from: options.from ?? 0,
        to: options.to ?? 0,
      });

      try {
        const result = await getRuntimePlanDiff(trimmedPlanId, options);
        if (seq === diffRequestSeq.current) {
          setDiffState({
            status: "ready",
            planId: result.plan_id,
            from: result.from_version,
            to: result.to_version,
            result,
            error: "",
          });
        }
        return result;
      } catch (error) {
        const message = readErrorMessage(error, "failed to load plan diff");
        if (seq === diffRequestSeq.current) {
          setDiffState({
            ...IDLE_DIFF_STATE,
            status: "error",
            planId: trimmedPlanId,
            from: options.from ?? 0,
            to: options.to ?? 0,
            error: message,
          });
        }
        return null;
      }
    },
    [],
  );

  /** 收起差异面板（同一轮次再次点击或切换计划时调用）。 */
  const clearDiff = useCallback(() => {
    diffRequestSeq.current += 1;
    setDiffState(IDLE_DIFF_STATE);
  }, []);

  return {
    clearDiff,
    clearComments,
    clearReopenState,
    commentsState,
    createComment,
    deleteComment,
    detailError,
    detailLoading,
    diffState,
    loadDiff,
    loadComments,
    loadedOnce,
    plans,
    plansError,
    plansLoading,
    refresh,
    reopen,
    reopenState,
    select,
    selectedPlan,
    selectedPlanId,
  };
}
