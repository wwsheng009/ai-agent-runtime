// Batch 2（多会话并发运行时）：订阅策略接线（方案 §4.2 / §4.6）。
//
// 把"当前会话 / 后台活跃 / 最近活动 / 页面隐藏"翻译成注册表调用，保留在页面层
// （而非注册表内部）：策略可单测、可回滚，注册表只负责预算与调度。
//
// 缺省 `enabled = MULTI_SESSION_REGISTRY_ENABLED`（false）——不打开开关时
// 本 hook 不建立任何后台订阅，行为与改造前一致。
// Batch 4：翻译逻辑收敛到 `lib/session-runtime/policy.ts`（纯函数，可脱离
// React 单测），本 hook 只负责喂最新输入 + 执行指令。

import { useEffect, useRef } from "react";

import { MULTI_SESSION_REGISTRY_ENABLED } from "@/lib/session-runtime/flags";
import {
  DEFAULT_RECENT_WINDOW_MS,
  planSessionSubscriptions,
  type SessionStreamSupervisorCandidate,
} from "@/lib/session-runtime/policy";
import type { SessionRuntimeRegistry } from "@/lib/session-runtime/registry";

import { useSessionRuntimeRegistry } from "./use-session-runtime-registry";

export type { SessionStreamSupervisorCandidate };

export type SessionStreamSupervisorOptions = {
  /** 缺省取回滚开关（false = 完全关闭后台订阅）。 */
  enabled?: boolean;
  selectedSessionId: string | null | undefined;
  candidates: readonly SessionStreamSupervisorCandidate[];
  /** "最近活动"窗口（缺省 10 分钟，§4.2）。 */
  recentWindowMs?: number;
};

/**
 * 策略依赖键：把候选集合压成字符串，避免调用方每次 render 产生的新数组引用
 * 触发重复调度；集合内容变化时键自然变化。
 */
function candidateKey(
  selectedSessionId: string | null | undefined,
  candidates: readonly SessionStreamSupervisorCandidate[],
): string {
  const parts = candidates.map(
    (candidate) =>
      `${candidate.sessionId}:${candidate.updatedAt ?? ""}:${candidate.hasActiveTurn ? 1 : 0}`,
  );
  return `${selectedSessionId ?? ""}|${parts.join("|")}`;
}

export function useSessionStreamSupervisor(
  options: SessionStreamSupervisorOptions,
): SessionRuntimeRegistry {
  const {
    enabled = MULTI_SESSION_REGISTRY_ENABLED,
    selectedSessionId,
    candidates,
    recentWindowMs = DEFAULT_RECENT_WINDOW_MS,
  } = options;
  const registry = useSessionRuntimeRegistry();
  const candidatesRef = useRef(candidates);
  const selectedRef = useRef(selectedSessionId);
  const key = candidateKey(selectedSessionId, candidates);

  // ref 同步 effect 声明在消费 effect 之前：同一提交内按声明顺序执行，
  // 消费 effect 读到的是本次 render 的最新值（react-hooks/refs 禁止渲染期写 ref）。
  useEffect(() => {
    candidatesRef.current = candidates;
    selectedRef.current = selectedSessionId;
  }, [candidates, selectedSessionId]);

  useEffect(() => {
    if (!enabled) {
      return;
    }
    const plan = planSessionSubscriptions({
      selectedSessionId: selectedRef.current,
      candidates: candidatesRef.current,
      knownSessionIds: [...registry.entriesSnapshot().keys()],
      now: Date.now(),
      recentWindowMs,
    });
    for (const entry of plan.ensure) {
      registry.ensure(entry.sessionId, entry.reason);
    }
    for (const entry of plan.release) {
      registry.release(entry.sessionId, entry.reason);
    }
    // 策略幂等：同一依赖键重复执行不产生额外连接（ensure 内部按模式收敛）。
  }, [enabled, key, registry, recentWindowMs]);

  useEffect(() => {
    if (!enabled) {
      return;
    }
    const handleVisibility = () => {
      registry.noteVisibility(
        typeof document !== "undefined" && document.hidden === true,
      );
    };
    handleVisibility();
    document.addEventListener("visibilitychange", handleVisibility);
    return () => {
      document.removeEventListener("visibilitychange", handleVisibility);
    };
  }, [enabled, registry]);

  // 页面卸载时的释放由 `useSessionRuntimeRegistry` 的引用计数负责：本 hook 不再
  // 直接 dispose 单例（StrictMode 的模拟卸载会把注册表打死，见其注释）。

  return registry;
}
