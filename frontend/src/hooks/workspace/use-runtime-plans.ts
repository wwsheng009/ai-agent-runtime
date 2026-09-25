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
import { getRuntimePlan, listRuntimePlans } from "@/lib/runtime-api";
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

  const lastRuntimeEventKey = buildRuntimeEventReloadKey(
    lastRuntimeEventType,
    runtimeEventCount,
  );
  const lastRuntimeEventKeyRef = useRef(lastRuntimeEventKey);
  const selectedPlanIdRef = useRef<string | null>(null);
  const listRequestSeq = useRef(0);
  const detailRequestSeq = useRef(0);

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
    }
  }, [
    lastHandledEventKey,
    lastRuntimeEventKey,
    lastRuntimeEventType,
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
      }
    },
    [loadDetail],
  );

  /** 手动刷新：列表 + （若已打开详情）当前详情。 */
  const refresh = useCallback(async () => {
    const currentDetailId = selectedPlanIdRef.current;
    await Promise.all([
      loadPlans(lastRuntimeEventKeyRef.current),
      currentDetailId ? loadDetail(currentDetailId) : Promise.resolve(),
    ]);
  }, [loadDetail, loadPlans]);

  return {
    detailError,
    detailLoading,
    loadedOnce,
    plans,
    plansError,
    plansLoading,
    refresh,
    select,
    selectedPlan,
    selectedPlanId,
  };
}
