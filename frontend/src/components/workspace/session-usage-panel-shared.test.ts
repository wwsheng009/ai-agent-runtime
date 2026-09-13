import { describe, expect, it } from "vitest";

import type { AnalyticsSessionUsageDetail } from "@/types/runtime";

import {
  buildSessionUsageReloadKey,
  formatUsageDuration,
  formatUsageNumber,
  formatUsageRate,
  hasSessionUsageData,
  isUsageRelevantRuntimeEvent,
  resolveSessionUsageSummary,
  sessionUsageHref,
} from "./session-usage-panel-shared";

function buildDetail(
  overrides: Partial<AnalyticsSessionUsageDetail["session"]> = {},
  detailOverrides: Partial<AnalyticsSessionUsageDetail> = {},
): AnalyticsSessionUsageDetail {
  return {
    schema_version: "runtime.analytics.v1",
    generated_at: "2026-09-13T00:00:00Z",
    coverage: {
      sessions: 1,
      sessions_with_usage: 1,
      usage_session_rate: 1,
      llm_requests: 2,
      llm_requests_with_usage: 2,
      usage_request_rate: 1,
      tool_results_observed: 0,
      dropped_messages: 0,
    },
    partial: false,
    partial_reasons: [],
    session: {
      session_id: "session-1",
      title: "用量面板会话",
      directory: "E:/projects/demo",
      rel_path: ".",
      total_requests: 2,
      total_responses: 2,
      total_tool_calls: 0,
      total_tokens: 12345,
      prompt_tokens: 10000,
      completion_tokens: 2345,
      cached_tokens: 2500,
      reasoning_tokens: 100,
      llm_requests: 2,
      llm_errors: 0,
      turn_count: 3,
      failed_turns: 1,
      recovered_turns: 0,
      tool_results_observed: 0,
      tool_errors: 0,
      average_response_time_ms: 1500,
      total_duration_ms: 60_000,
      usage_quality: "provider_reported",
      usage_complete: true,
      usage_coverage: 0.98,
      partial: false,
      partial_reasons: [],
      dropped_messages: 0,
      reconciliation_status: "matched",
      reconciliation_delta: 0,
      provider: "opencode.ai",
      model: "deepseek-v4.1-flash",
      status: "active",
      last_observed_at: "2026-09-13T01:00:00Z",
      ...overrides,
    },
    steps: [],
    step_count: 0,
    turns: [],
    diagnostics: [],
    error_categories: {},
    ...detailOverrides,
  };
}

describe("resolveSessionUsageSummary", () => {
  it("derives the session rollup used by the rail panel", () => {
    const summary = resolveSessionUsageSummary(buildDetail());

    expect(summary).not.toBeNull();
    expect(summary?.sessionId).toBe("session-1");
    expect(summary?.provider).toBe("opencode.ai");
    expect(summary?.model).toBe("deepseek-v4.1-flash");
    expect(summary?.totalTokens).toBe(12345);
    expect(summary?.llmRequests).toBe(2);
    expect(summary?.turns).toBe(3);
    expect(summary?.failedTurns).toBe(1);
    expect(summary?.cacheHitRatio).toBeCloseTo(0.25);
    expect(summary?.usageCoverage).toBeCloseTo(0.98);
    expect(summary?.observedAt).toBe("2026-09-13T01:00:00Z");
    expect(hasSessionUsageData(summary)).toBe(true);
  });

  it("tolerates missing session payloads and null collections", () => {
    expect(resolveSessionUsageSummary(null)).toBeNull();
    expect(resolveSessionUsageSummary(undefined)).toBeNull();
    expect(
      resolveSessionUsageSummary({} as unknown as AnalyticsSessionUsageDetail),
    ).toBeNull();

    const detailWithoutCollections = buildDetail(
      {},
      {
        steps: null,
        turns: null,
        diagnostics: null,
        error_categories: null,
      } as unknown as Partial<AnalyticsSessionUsageDetail>,
    );
    expect(resolveSessionUsageSummary(detailWithoutCollections)?.totalTokens).toBe(12345);
  });

  it("falls back to step-level cache hit ratio when rollup tokens are missing", () => {
    const summary = resolveSessionUsageSummary(
      buildDetail(
        { cached_tokens: 0, prompt_tokens: 0 },
        {
          steps: [
            { success: true, usage_available: true, cache_hit_ratio: 0.4 },
            { success: true, usage_available: true, cache_hit_ratio: 0.6 },
          ],
        },
      ),
    );

    expect(summary?.cacheHitRatio).toBeCloseTo(0.5);
  });

  it("reports no usage data for an idle session", () => {
    const summary = resolveSessionUsageSummary(
      buildDetail({
        total_tokens: 0,
        prompt_tokens: 0,
        completion_tokens: 0,
        llm_requests: 0,
        total_requests: 0,
        turn_count: 0,
      }),
    );

    expect(hasSessionUsageData(summary)).toBe(false);
  });
});

describe("session usage formatting", () => {
  it("formats numbers with compact units", () => {
    expect(formatUsageNumber(null)).toBe("—");
    expect(formatUsageNumber(0)).toBe("0");
    expect(formatUsageNumber(1234)).toBe("1,234");
    expect(formatUsageNumber(12345)).toBe("12.3k");
    expect(formatUsageNumber(1_234_567)).toBe("1.2M");
  });

  it("formats ratios and durations", () => {
    expect(formatUsageRate(null)).toBe("—");
    expect(formatUsageRate(0.5)).toBe("50.0%");
    expect(formatUsageRate(0.004)).toBe("0.40%");
    expect(formatUsageRate(1.5)).toBe("100.0%");

    expect(formatUsageDuration(null)).toBe("—");
    expect(formatUsageDuration(950)).toBe("950 ms");
    expect(formatUsageDuration(1500)).toBe("1.5 s");
    expect(formatUsageDuration(120_000)).toBe("2.0 min");
  });

  it("builds the usage detail link and reload keys", () => {
    expect(sessionUsageHref(" session-1 ")).toBe("/usage/sessions/session-1");
    expect(sessionUsageHref("session/a b")).toBe("/usage/sessions/session%2Fa%20b");
    expect(sessionUsageHref("")).toBe("/usage");
  });
});

describe("isUsageRelevantRuntimeEvent", () => {
  it("triggers on the events the session runtime stream actually delivers", () => {
    // 会话事件流（/runtime/stream）里的类型来自会话事件存储，不含 llm_request_*，
    // 因此用这些代理事件判断「用量已落库」。
    expect(isUsageRelevantRuntimeEvent("assistant_message")).toBe(true);
    expect(isUsageRelevantRuntimeEvent("tool_started")).toBe(true);
    expect(isUsageRelevantRuntimeEvent("tool_finished")).toBe(true);
    expect(isUsageRelevantRuntimeEvent("tool.completed")).toBe(true);
    expect(isUsageRelevantRuntimeEvent("session_end")).toBe(true);
    expect(isUsageRelevantRuntimeEvent("session_interrupted")).toBe(true);

    // 兼容 chat SSE 面的 turn_start/turn_end，以及大小写/空白差异。
    expect(isUsageRelevantRuntimeEvent("turn_completed")).toBe(true);
    expect(isUsageRelevantRuntimeEvent(" Assistant_Message ")).toBe(true);

    // 与用量无关（或尚未落库）的事件不应触发重取。
    expect(isUsageRelevantRuntimeEvent("checkpoint_created")).toBe(false);
    expect(isUsageRelevantRuntimeEvent("assistant_delta")).toBe(false);
    expect(isUsageRelevantRuntimeEvent("session_start")).toBe(false);
    expect(isUsageRelevantRuntimeEvent("")).toBe(false);
    expect(isUsageRelevantRuntimeEvent(undefined)).toBe(false);
    expect(isUsageRelevantRuntimeEvent(null)).toBe(false);
  });

  it("builds reload keys only for usage relevant events", () => {
    expect(buildSessionUsageReloadKey("assistant_message", 4)).toBe(
      "assistant_message:4",
    );
    expect(buildSessionUsageReloadKey("tool_finished", 5)).toBe("tool_finished:5");
    expect(isUsageRelevantRuntimeEvent("turn_completed")).toBe(true);
    expect(buildSessionUsageReloadKey("turn_completed", 4)).toBe("turn_completed:4");
    expect(buildSessionUsageReloadKey("checkpoint_created", 4)).toBe("");
    expect(buildSessionUsageReloadKey(undefined, 4)).toBe("");
  });
});
