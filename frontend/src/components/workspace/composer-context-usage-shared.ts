// composer 上下文进度图标的纯逻辑：把 runtime.analytics.v1 的会话用量明细
// 派生为「当前上下文占用」快照，并给出环形进度/面板需要的格式化结果。
//
// 数据现实（为什么这么取）：
//   * 后端没有「context 占用率」的实时 REST 端点，context 数据只出现在
//     llm_request 结束事件与 usageanalytics 落库的 step 明细里；
//   * 因此前端用会话用量明细的 steps 反推：取「最近一次带 context 信号的
//     LLM 请求」，而不是对全会话求最大值 —— 环上显示的必须是此刻在用的量；
//   * 缺字段不猜测：窗口未知时 utilization 为 null，环退化为中性态并如实
//     显示「—」，绝不拿历史峰值冒充当前占用。

import { formatUsageNumber } from "@/components/workspace/session-usage-panel-shared";
import type { SessionCompactOutcome } from "@/api/runtime";
import type { AnalyticsSessionUsageDetail, AnalyticsStepUsage } from "@/types/runtime";

/** 上下文数据来源：窗口上限 / prompt 预算 / 手动压缩结果 / 无。 */
export type ContextUsageSource =
  | "context_window"
  | "prompt_budget"
  | "compact"
  | "none";

export type ContextUsageLevel = "unknown" | "normal" | "warning" | "critical";

export type ContextUsageSnapshot = {
  /** 当前上下文已占用 token（最近一次请求的 prompt/context token）。 */
  usedTokens: number;
  /** 上下文窗口上限；0 = 未知。 */
  windowTokens: number;
  /** prompt 预算（触发/可用上限）；null = 后端未上报。 */
  budgetTokens: number | null;
  /** 0..1 占用率；null = 窗口未知，无法给百分比。 */
  utilization: number | null;
  /** 剩余可用 token；null = 窗口未知。 */
  remainingTokens: number | null;
  level: ContextUsageLevel;
  source: ContextUsageSource;
  /** 该数据的观测时间（step 时间戳）。 */
  observedAt?: string;
  traceId?: string;
  step?: number;
};

/** 阈值与后端 compactruntime 的告警口径保持一致：75% 预警、90% 危险。 */
export const CONTEXT_USAGE_WARNING_RATIO = 0.75;
export const CONTEXT_USAGE_CRITICAL_RATIO = 0.9;

function toFiniteNumber(value: unknown): number | null {
  return typeof value === "number" && Number.isFinite(value) ? value : null;
}

function clampRatio(value: number): number {
  return Math.min(Math.max(value, 0), 1);
}

function positiveNumber(value: unknown): number | null {
  const numeric = toFiniteNumber(value);
  return numeric !== null && numeric > 0 ? numeric : null;
}

/**
 * step 是否观测到了「上下文已用量」。
 *
 * 只认已用量字段（context_prompt_tokens / prompt_tokens）：窗口/预算只说明分母，
 * 单独出现时若当成信号会推出 used=0、占用率=0% 的假进度，
 * 因此这类步骤直接跳过（继续往更早的步骤找真正观测到用量的那一次）。
 */
function hasContextSignal(step: AnalyticsStepUsage): boolean {
  return (
    positiveNumber(step?.context_prompt_tokens) !== null ||
    positiveNumber(step?.prompt_tokens) !== null
  );
}

function buildSnapshot(
  step: AnalyticsStepUsage,
  source: ContextUsageSource,
): ContextUsageSnapshot {
  const usedTokens =
    positiveNumber(step.context_prompt_tokens) ?? positiveNumber(step.prompt_tokens) ?? 0;
  const windowTokens =
    positiveNumber(step.context_window_tokens) ?? positiveNumber(step.prompt_budget) ?? 0;
  const budgetTokens = positiveNumber(step.prompt_budget);
  const explicitUtilization = toFiniteNumber(step.context_utilization);
  const utilization =
    windowTokens > 0
      ? clampRatio(explicitUtilization !== null ? explicitUtilization : usedTokens / windowTokens)
      : null;
  const resolvedSource: ContextUsageSource =
    source === "none"
      ? positiveNumber(step.context_window_tokens) !== null
        ? "context_window"
        : positiveNumber(step.prompt_budget) !== null
          ? "prompt_budget"
          : "none"
      : source;

  return {
    usedTokens,
    windowTokens,
    budgetTokens,
    utilization,
    remainingTokens: windowTokens > 0 ? Math.max(windowTokens - usedTokens, 0) : null,
    level: contextUsageLevel(utilization),
    source: resolvedSource,
    ...(step.timestamp ?? step.started_at
      ? { observedAt: step.timestamp ?? step.started_at }
      : {}),
    ...(typeof step.trace_id === "string" && step.trace_id ? { traceId: step.trace_id } : {}),
    ...(typeof step.step === "number" ? { step: step.step } : {}),
  };
}

/**
 * 从会话用量明细取「最近一次带上下文信号的 LLM 请求」。
 * 找不到任何信号时返回 null —— 面板显示空态，而不是 0%。
 */
export function resolveContextUsage(
  detail: AnalyticsSessionUsageDetail | null | undefined,
): ContextUsageSnapshot | null {
  const steps = Array.isArray(detail?.steps) ? detail.steps : [];
  for (let index = steps.length - 1; index >= 0; index -= 1) {
    const step = steps[index];
    if (step && hasContextSignal(step)) {
      return buildSnapshot(step, "none");
    }
  }

  return null;
}

/**
 * 手动压缩完成后立刻用响应里的 token_before/after 覆盖显示。
 *
 * 为什么需要：analytics 落库有延迟（hook 里还有一拍防抖），压缩刚完成时
 * 环上若还显示压缩前的占用会让人以为「点了没生效」。
 */
export function resolveContextUsageFromCompact(
  outcome: SessionCompactOutcome | null | undefined,
): ContextUsageSnapshot | null {
  const result = outcome?.result ?? null;
  const status = outcome?.status ?? null;
  if (!result && !status) {
    return null;
  }

  // result 缺失 = 运行时判定无需压缩（skipped），此时 status.tokenBefore 才是当前占用；
  // status 缺失（异常载荷）时也不崩，退化为「已用量已知、窗口未知」。
  const usedTokens = result ? result.tokenAfter : (status?.tokenBefore ?? 0);
  const windowTokens = positiveNumber(status?.maxContextTokens) ?? 0;
  const budgetTokens = positiveNumber(status?.triggerTokenLimit);
  const utilization = windowTokens > 0 ? clampRatio(usedTokens / windowTokens) : null;

  return {
    usedTokens,
    windowTokens,
    budgetTokens,
    utilization,
    remainingTokens: windowTokens > 0 ? Math.max(windowTokens - usedTokens, 0) : null,
    level: contextUsageLevel(utilization),
    source: "compact",
  };
}

export function contextUsageLevel(utilization: number | null): ContextUsageLevel {
  if (utilization === null) {
    return "unknown";
  }
  if (utilization >= CONTEXT_USAGE_CRITICAL_RATIO) {
    return "critical";
  }
  if (utilization >= CONTEXT_USAGE_WARNING_RATIO) {
    return "warning";
  }
  return "normal";
}

/** 面板用的精确百分比（一位小数，<1% 保留两位）。 */
export function formatContextPercent(utilization: number | null): string {
  if (utilization === null) {
    return "—";
  }

  const clamped = clampRatio(utilization);
  const percent = clamped * 100;
  return `${percent.toFixed(clamped > 0 && clamped < 0.01 ? 2 : 1)}%`;
}

/** 环内文字用的极简百分比：整数 + %，避免 32px 圆内挤成两行。 */
export function formatContextPercentCompact(utilization: number | null): string {
  if (utilization === null) {
    return "—";
  }

  const clamped = clampRatio(utilization);
  if (clamped > 0 && clamped < 0.01) {
    return "<1%";
  }
  return `${Math.round(clamped * 100)}%`;
}

export function formatContextTokens(value?: number | null): string {
  return formatUsageNumber(value);
}
