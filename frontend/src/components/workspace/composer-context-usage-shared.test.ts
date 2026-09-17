import { describe, expect, it } from "vitest";

import type { SessionCompactOutcome } from "@/api/runtime";
import type { AnalyticsSessionUsageDetail, AnalyticsStepUsage } from "@/types/runtime";

import {
  CONTEXT_USAGE_CRITICAL_RATIO,
  CONTEXT_USAGE_WARNING_RATIO,
  contextUsageLevel,
  formatContextPercent,
  formatContextPercentCompact,
  resolveContextUsage,
  resolveContextUsageFromCompact,
} from "./composer-context-usage-shared";

function buildStep(overrides: Partial<AnalyticsStepUsage> = {}): AnalyticsStepUsage {
  return {
    success: true,
    usage_available: true,
    ...overrides,
  };
}

function buildDetail(steps: AnalyticsStepUsage[]): AnalyticsSessionUsageDetail {
  return {
    schema_version: "runtime.analytics.v1",
    generated_at: "2026-09-17T00:00:00Z",
    coverage: {
      sessions: 1,
      sessions_with_usage: 1,
      usage_session_rate: 1,
      llm_requests: steps.length,
      llm_requests_with_usage: steps.length,
      usage_request_rate: 1,
      tool_results_observed: 0,
      dropped_messages: 0,
    },
    partial: false,
    partial_reasons: [],
    session: {
      session_id: "session-1",
      title: "上下文进度会话",
      directory: "E:/projects/demo",
      rel_path: ".",
      total_requests: steps.length,
      total_responses: steps.length,
      total_tool_calls: 0,
      total_tokens: 0,
      prompt_tokens: 0,
      completion_tokens: 0,
      cached_tokens: 0,
      reasoning_tokens: 0,
      llm_requests: steps.length,
      llm_errors: 0,
      turn_count: 1,
      failed_turns: 0,
      recovered_turns: 0,
      tool_results_observed: 0,
      tool_errors: 0,
      average_response_time_ms: 0,
      total_duration_ms: 0,
      usage_quality: "provider_reported",
      usage_complete: true,
      usage_coverage: 1,
      partial: false,
      partial_reasons: [],
      dropped_messages: 0,
      reconciliation_status: "matched",
      reconciliation_delta: 0,
      provider: "opencode.ai",
      model: "deepseek-v4.1-flash",
      status: "active",
    },
    steps,
    step_count: steps.length,
    turns: [],
    diagnostics: [],
    error_categories: {},
  };
}

describe("resolveContextUsage", () => {
  it("没有会话明细时返回 null，而不是伪造 0%", () => {
    expect(resolveContextUsage(null)).toBeNull();
    expect(resolveContextUsage(undefined)).toBeNull();
  });

  it("只有 completion_tokens 的步骤看不到上下文，返回 null", () => {
    const detail = buildDetail([buildStep({ completion_tokens: 20 })]);

    expect(resolveContextUsage(detail)).toBeNull();
  });

  it("只有窗口/预算、没有已用量的步骤不算占用观测（避免伪造 0%）", () => {
    const detail = buildDetail([
      buildStep({ context_window_tokens: 100_000, prompt_budget: 60_000 }),
    ]);

    expect(resolveContextUsage(detail)).toBeNull();
  });

  it("只有 prompt_tokens 时：已用量可见，但占用率为 null（不拿未知窗口硬算）", () => {
    const detail = buildDetail([
      buildStep({ prompt_tokens: 100, completion_tokens: 20 }),
    ]);

    const snapshot = resolveContextUsage(detail);

    expect(snapshot?.usedTokens).toBe(100);
    expect(snapshot?.windowTokens).toBe(0);
    expect(snapshot?.utilization).toBeNull();
    expect(snapshot?.level).toBe("unknown");
  });

  it("取最近一次带 context 信号的请求，而不是历史峰值", () => {
    const detail = buildDetail([
      buildStep({
        step: 1,
        context_prompt_tokens: 90_000,
        context_window_tokens: 100_000,
        context_utilization: 0.9,
      }),
      buildStep({
        step: 2,
        context_prompt_tokens: 12_000,
        context_window_tokens: 100_000,
      }),
    ]);

    const snapshot = resolveContextUsage(detail);

    expect(snapshot?.step).toBe(2);
    expect(snapshot?.usedTokens).toBe(12_000);
    expect(snapshot?.windowTokens).toBe(100_000);
    expect(snapshot?.utilization).toBeCloseTo(0.12);
    expect(snapshot?.remainingTokens).toBe(88_000);
    expect(snapshot?.level).toBe("normal");
    expect(snapshot?.source).toBe("context_window");
  });

  it("缺 context_prompt_tokens 时回落到 prompt_tokens", () => {
    const detail = buildDetail([
      buildStep({ prompt_tokens: 25_000, context_window_tokens: 100_000 }),
    ]);

    const snapshot = resolveContextUsage(detail);

    expect(snapshot?.usedTokens).toBe(25_000);
    expect(snapshot?.utilization).toBeCloseTo(0.25);
  });

  it("缺 context_window_tokens 时用 prompt_budget 当窗口，并标记来源", () => {
    const detail = buildDetail([
      buildStep({ context_prompt_tokens: 30_000, prompt_budget: 60_000 }),
    ]);

    const snapshot = resolveContextUsage(detail);

    expect(snapshot?.source).toBe("prompt_budget");
    expect(snapshot?.windowTokens).toBe(60_000);
    expect(snapshot?.budgetTokens).toBe(60_000);
    expect(snapshot?.utilization).toBeCloseTo(0.5);
  });

  it("后端上报的 context_utilization 优先，并钳制到 0..1", () => {
    const detail = buildDetail([
      buildStep({
        context_prompt_tokens: 10_000,
        context_window_tokens: 100_000,
        context_utilization: 1.4,
      }),
    ]);

    expect(resolveContextUsage(detail)?.utilization).toBe(1);
    expect(resolveContextUsage(detail)?.level).toBe("critical");
  });

  it("窗口未知时占用率为 null（环退化为中性态），不拿已用量硬算", () => {
    const detail = buildDetail([buildStep({ context_prompt_tokens: 5_000 })]);

    const snapshot = resolveContextUsage(detail);

    expect(snapshot?.usedTokens).toBe(5_000);
    expect(snapshot?.windowTokens).toBe(0);
    expect(snapshot?.utilization).toBeNull();
    expect(snapshot?.remainingTokens).toBeNull();
    expect(snapshot?.level).toBe("unknown");
    expect(snapshot?.source).toBe("none");
  });
});

describe("resolveContextUsageFromCompact", () => {
  it("压缩成功时用 token_after 覆盖显示", () => {
    const snapshot = resolveContextUsageFromCompact({
      status: {
        mode: "local",
        phase: "pre_turn",
        reason: "",
        provider: "",
        model: "",
        triggerTokenLimit: 60_000,
        maxContextTokens: 100_000,
        tokenBefore: 80_000,
      },
      result: {
        mode: "local",
        phase: "pre_turn",
        reason: "",
        provider: "",
        model: "",
        triggerTokenLimit: 60_000,
        maxContextTokens: 100_000,
        tokenBefore: 80_000,
        tokenAfter: 20_000,
        compactedMessages: 6,
        checkpointIds: ["ckpt-1"],
        usageSource: "provider",
      },
    });

    expect(snapshot?.usedTokens).toBe(20_000);
    expect(snapshot?.utilization).toBeCloseTo(0.2);
    expect(snapshot?.source).toBe("compact");
    expect(snapshot?.budgetTokens).toBe(60_000);
  });

  it("skipped（result=null）时展示压缩前的占用，不误报为已压缩", () => {
    const snapshot = resolveContextUsageFromCompact({
      status: {
        mode: "auto",
        phase: "pre_turn",
        reason: "below_threshold",
        provider: "",
        model: "",
        triggerTokenLimit: 0,
        maxContextTokens: 100_000,
        tokenBefore: 30_000,
      },
      result: null,
    });

    expect(snapshot?.usedTokens).toBe(30_000);
    expect(snapshot?.utilization).toBeCloseTo(0.3);
    expect(snapshot?.budgetTokens).toBeNull();
  });

  it("空结果返回 null", () => {
    expect(resolveContextUsageFromCompact(null)).toBeNull();
  });

  it("异常载荷（只有 result 没有 status）不崩，退化为窗口未知", () => {
    const outcome = {
      result: {
        mode: "local",
        phase: "pre_turn",
        reason: "",
        provider: "",
        model: "",
        triggerTokenLimit: 0,
        maxContextTokens: 0,
        tokenBefore: 80_000,
        tokenAfter: 20_000,
        compactedMessages: 6,
        checkpointIds: [],
        usageSource: "provider",
      },
    } as unknown as SessionCompactOutcome;

    const snapshot = resolveContextUsageFromCompact(outcome);

    expect(snapshot?.usedTokens).toBe(20_000);
    expect(snapshot?.windowTokens).toBe(0);
    expect(snapshot?.utilization).toBeNull();
    expect(snapshot?.source).toBe("compact");
  });
});

describe("contextUsageLevel / 格式化", () => {
  it("阈值与后端告警口径一致", () => {
    expect(contextUsageLevel(null)).toBe("unknown");
    expect(contextUsageLevel(CONTEXT_USAGE_WARNING_RATIO - 0.01)).toBe("normal");
    expect(contextUsageLevel(CONTEXT_USAGE_WARNING_RATIO)).toBe("warning");
    expect(contextUsageLevel(CONTEXT_USAGE_CRITICAL_RATIO)).toBe("critical");
  });

  it("百分比格式化：一位小数、极小值两位、空值给破折号", () => {
    expect(formatContextPercent(0.424)).toBe("42.4%");
    expect(formatContextPercent(0.004)).toBe("0.40%");
    expect(formatContextPercent(null)).toBe("—");
  });

  it("环内文字取整，<1% 单独标注", () => {
    expect(formatContextPercentCompact(0.424)).toBe("42%");
    expect(formatContextPercentCompact(0.004)).toBe("<1%");
    expect(formatContextPercentCompact(1.2)).toBe("100%");
    expect(formatContextPercentCompact(null)).toBe("—");
  });
});
