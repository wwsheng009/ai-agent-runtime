/**
 * 会话目标 live 捕获 store（P2-9 子片 3「目标四相指示」MVP）。
 *
 * 为什么需要这个 store：goal 只在 chat SSE 的 `tool_end` 事件里以**完整**
 * `tool.content` 到达（后端 `buildToolEventPayload`）；下游所有保留路径都是截断的
 * （线程层 240 字符、轨迹层不读 `tool.content`），直接解析它们会得到半截 JSON。
 * 因此唯一写入口放在 SSE 边界，读出口只有 `useSessionGoal`：
 *
 * - 写入：`recordGoalToolEnd(sessionId, payload)`（`stream-handlers.ts` 的 onToolEnd）；
 * - 读取：`useSessionGoal(sessionId)`（useSyncExternalStore，稳定引用）；
 * - 语义：无法判定的结果不写入（保持既有投影）；`goal: null` / `goal_missing`
 *   如实清空（该观测表示「此刻没有目标」）。
 *
 * 进程内内存态：页面刷新后为空 → 指示条不渲染（不回落成猜测值、不补零）。
 * 权威快照（REST）就绪后由后端字段替换本路径，届时不做双写。
 */
import { useCallback, useSyncExternalStore } from "react";

import {
  deriveSessionGoal,
  GOAL_TOOL_NAMES,
  parseGoalToolResult,
  type GoalObservation,
  type SessionGoal,
} from "./derive";

const observations = new Map<string, GoalObservation>();
const projected = new Map<string, SessionGoal | null>();
const listeners = new Set<() => void>();

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function readToolName(payload: Record<string, unknown>): string {
  for (const key of ["tool", "tool_call"] as const) {
    const container = payload[key];
    if (isRecord(container)) {
      const name = container["name"];
      if (typeof name === "string" && name.trim() !== "") {
        return name.trim();
      }
    }
  }
  const fallback = payload["tool_name"];
  return typeof fallback === "string" ? fallback.trim() : "";
}

/** 取完整结果文本（不截断）；缺字段返回空串。 */
function readToolResultText(payload: Record<string, unknown>): string {
  const tool = payload["tool"];
  if (isRecord(tool)) {
    for (const key of ["content", "result", "output"] as const) {
      const value = tool[key];
      if (typeof value === "string" && value.trim() !== "") {
        return value;
      }
    }
  }
  const content = payload["content"];
  return typeof content === "string" ? content : "";
}

function notify(): void {
  for (const listener of listeners) {
    listener();
  }
}

export function isGoalToolName(name: string): boolean {
  return (GOAL_TOOL_NAMES as readonly string[]).includes(name.trim());
}

/** SSE `tool_end` 落点：只处理 goal 工具；无法判定时不改动既有投影。 */
export function recordGoalToolEnd(
  sessionId: string | undefined,
  payload: Record<string, unknown> | null | undefined,
): void {
  if (!sessionId || !payload || typeof payload !== "object") {
    return;
  }
  if (!isGoalToolName(readToolName(payload))) {
    return;
  }
  const observation = parseGoalToolResult(readToolResultText(payload));
  if (observation.kind === "unknown") {
    return;
  }
  observations.set(sessionId, observation);
  projected.delete(sessionId);
  notify();
}

/** 读取当前投影（无数据 → null；引用稳定，可安全用于 useSyncExternalStore）。 */
export function getSessionGoal(sessionId: string | undefined): SessionGoal | null {
  if (!sessionId) {
    return null;
  }
  if (projected.has(sessionId)) {
    return projected.get(sessionId) ?? null;
  }
  const observation = observations.get(sessionId);
  const goal = observation ? deriveSessionGoal([observation]) : null;
  projected.set(sessionId, goal);
  return goal;
}

export function subscribeSessionGoal(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

/** 测试用：清空全部会话投影与订阅（生产代码不调用）。 */
export function resetSessionGoalStore(): void {
  observations.clear();
  projected.clear();
  listeners.clear();
}

export function useSessionGoal(sessionId: string | undefined): SessionGoal | null {
  const subscribe = useCallback(
    (listener: () => void) => subscribeSessionGoal(listener),
    [],
  );
  const getSnapshot = useCallback(() => getSessionGoal(sessionId), [sessionId]);
  return useSyncExternalStore(subscribe, getSnapshot, getSnapshot);
}
