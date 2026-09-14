/**
 * 会话目标（goal）只读派生 —— P2-9 子片 3「目标四相指示」MVP 的纯函数层。
 *
 * 数据面事实（2026-09-13 复核）：
 * - 后端 goal 只以运行时工具 `get_goal` / `update_goal` 暴露（`internal/goal`），
 *   结果 JSON 形如 `{"goal": {...} | null, "remaining_tokens": n}`；
 *   `update_goal` 在无 goal 时返回 `{"updated": false, "goal": null,
 *   "reason": "goal_missing"}`（metadata.no_op=true）——这是合法空操作，
 *   不得当成失败，也不得忽略（它表示「此刻确实没有目标」，需要清掉旧投影）。
 * - 尚无 REST/快照（见优化计划 §6.3 P2-1B），因此本模块只做「有什么说什么」的
 *   只读投影：解析失败 / 字段缺失 / 状态未知一律不猜、不补零、不推算。
 * - 四相状态对齐后端状态机：active / paused / budget_limited / complete。
 */
import { type TrajectoryItem } from "@/lib/trajectory/types";

export const GOAL_TOOL_NAMES = ["get_goal", "update_goal"] as const;

export type SessionGoalPhase = "active" | "paused" | "budget_limited" | "complete";

export type SessionGoal = {
  /** 四相状态；后端返回未知状态时为 null（statusRaw 保真透出，不映射）。 */
  phase: SessionGoalPhase | null;
  /** 后端原始 status 文本（保真；未知状态时用于如实呈现）。 */
  statusRaw: string;
  /** 目标文本；为空串时调用方不渲染目标行（不编造占位文案）。 */
  objective: string;
  tokenBudget?: number;
  tokensUsed?: number;
  remainingTokens?: number;
  completedBy?: string;
  completionSummary?: string;
  updatedAt?: string;
  completedAt?: string;
};

/** 单次 goal 工具结果的观测。`unknown` 表示无法判定，调用方必须保持既有投影。 */
export type GoalObservation =
  | { kind: "goal"; goal: SessionGoal }
  | { kind: "absent" }
  | { kind: "unknown" };

const PHASES: readonly SessionGoalPhase[] = [
  "active",
  "paused",
  "budget_limited",
  "complete",
];

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function readText(container: Record<string, unknown>, key: string): string {
  const value = container[key];
  return typeof value === "string" ? value.trim() : "";
}

/** 只接受有限非负数字；其余（含字符串数字）一律视为缺失，避免补零。 */
function readCount(value: unknown): number | undefined {
  if (typeof value !== "number" || !Number.isFinite(value) || value < 0) {
    return undefined;
  }
  return value;
}

function readOptionalText(
  container: Record<string, unknown>,
  key: string,
): string | undefined {
  const text = readText(container, key);
  return text === "" ? undefined : text;
}

export function normalizeSessionGoalPhase(value: unknown): SessionGoalPhase | null {
  if (typeof value !== "string") {
    return null;
  }
  const normalized = value.trim().toLowerCase();
  return PHASES.find((phase) => phase === normalized) ?? null;
}

/** 解析单个 goal 载荷；不是合法 goal 对象时返回 null（不构造半成品）。 */
export function parseSessionGoal(value: unknown): SessionGoal | null {
  if (!isRecord(value)) {
    return null;
  }
  const statusRaw = readText(value, "status");
  const objective = readText(value, "objective");
  const hasIdentity =
    readText(value, "goal_id") !== "" || statusRaw !== "" || objective !== "";
  if (!hasIdentity) {
    return null;
  }

  return {
    phase: normalizeSessionGoalPhase(statusRaw),
    statusRaw,
    objective,
    tokenBudget: readCount(value["token_budget"]),
    tokensUsed: readCount(value["tokens_used"]),
    completedBy: readOptionalText(value, "completed_by"),
    completionSummary: readOptionalText(value, "completion_summary"),
    updatedAt: readOptionalText(value, "updated_at"),
    completedAt: readOptionalText(value, "completed_at"),
  };
}

function withRemainingTokens(
  goal: SessionGoal,
  container: Record<string, unknown>,
): SessionGoal {
  const remainingTokens = readCount(container["remaining_tokens"]);
  return remainingTokens === undefined ? goal : { ...goal, remainingTokens };
}

/**
 * 解析 goal 工具结果文本。
 *
 * 接受的形状：
 * - `{"goal": {...}}`（get_goal；`remaining_tokens` 一并取用）
 * - `{"updated": true, "goal": {...}}`（update_goal）
 * - `{"updated": false, "goal": null, "reason": "goal_missing"}` → absent
 * - `{"goal": null}` → absent
 * - 裸 goal 对象（回放/降级路径）
 *
 * 非 JSON、截断 JSON、无 goal 字段的对象一律 unknown：调用方保持既有投影。
 */
export function parseGoalToolResult(text: string): GoalObservation {
  if (typeof text !== "string" || text.trim() === "") {
    return { kind: "unknown" };
  }

  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch {
    return { kind: "unknown" };
  }
  if (!isRecord(parsed)) {
    return { kind: "unknown" };
  }

  if ("goal" in parsed) {
    const rawGoal = parsed["goal"];
    if (rawGoal === null || rawGoal === undefined) {
      return { kind: "absent" };
    }
    const goal = parseSessionGoal(rawGoal);
    return goal ? { kind: "goal", goal: withRemainingTokens(goal, parsed) } : { kind: "unknown" };
  }

  const goal = parseSessionGoal(parsed);
  return goal ? { kind: "goal", goal } : { kind: "unknown" };
}

/** 按序折叠观测：unknown 跳过（不覆盖既有结论）；absent 明确清空。 */
export function deriveSessionGoal(
  observations: readonly GoalObservation[],
): SessionGoal | null {
  let current: SessionGoal | null = null;
  for (const observation of observations) {
    if (observation.kind === "unknown") {
      continue;
    }
    current = observation.kind === "absent" ? null : observation.goal;
  }
  return current;
}

function observationFromTrajectoryItem(item: TrajectoryItem): GoalObservation | null {
  if (item.head.kind !== "tool" || item.head.phase !== "finished") {
    return null;
  }
  const name = item.head.name.trim();
  if (!(GOAL_TOOL_NAMES as readonly string[]).includes(name)) {
    return null;
  }
  return parseGoalToolResult(item.head.resultSummary ?? "");
}

/**
 * 轨迹派生（降级路径）。
 *
 * 注意：轨迹层对工具结果只保留摘要（`resultSummary`，且线程层截断为 240 字符），
 * 真实的 goal JSON 通常无法在此完整还原。本函数因此经常返回 null —— 这是如实
 * 行为（宁可无指示，也不显示半截目标）。live 路径见 `store.ts`（SSE 完整性捕获）。
 */
export function sessionGoalFromTrajectoryItems(
  items: readonly TrajectoryItem[],
): SessionGoal | null {
  const ordered = [...items].sort((left, right) => left.updatedAt - right.updatedAt);
  const observations: GoalObservation[] = [];
  for (const item of ordered) {
    const observation = observationFromTrajectoryItem(item);
    if (observation) {
      observations.push(observation);
    }
  }
  return deriveSessionGoal(observations);
}

/** token 计数的紧凑展示（1.2k / 20k / 1.5M）；为非负有限数。 */
export function formatGoalTokens(value: number): string {
  if (!Number.isFinite(value) || value < 0) {
    return "";
  }
  if (value < 1000) {
    return String(Math.round(value));
  }
  if (value < 1_000_000) {
    const scaled = value / 1000;
    return `${scaled >= 100 ? Math.round(scaled) : Math.round(scaled * 10) / 10}k`;
  }
  const scaled = value / 1_000_000;
  return `${scaled >= 100 ? Math.round(scaled) : Math.round(scaled * 10) / 10}M`;
}
