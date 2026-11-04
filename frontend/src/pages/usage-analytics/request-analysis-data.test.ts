import { describe, expect, it } from "vitest";

import type { CacheRequestRecord } from "@/types/runtime";

import {
  buildCacheStatusBreakdown,
  buildRequestBreakdown,
  clampRequestLedgerLimit,
  localDateValue,
  normalizeCachePercentInput,
  parseCachePercent,
  requestCacheRatio,
  requestSummaryFromAnalytics,
  summarizeRequestRecords,
} from "./request-analysis-data";

function record(
  overrides: Partial<CacheRequestRecord> = {},
): CacheRequestRecord {
  return {
    schema_version: "cache.analytics.v1",
    llm_request_id: "req-1",
    session_id: "session-a",
    provider: "openai",
    model: "gpt-5",
    status: "success",
    started_at: "2026-09-13T10:00:00Z",
    cache_status: "hit",
    cache_hit_ratio: 0.8,
    usage: {
      prompt_tokens: 100,
      completion_tokens: 20,
      total_tokens: 120,
      cached_tokens: 80,
      cache_read_tokens: 80,
      cache_creation_tokens: 0,
      input_total_tokens: 100,
      cache_read_reported: true,
      cache_creation_reported: false,
      reasoning_tokens: 0,
    },
    ...overrides,
  };
}

describe("request analysis data", () => {
  it("clamps the request window and formats a local today value", () => {
    expect(clampRequestLedgerLimit("50")).toBe(50);
    expect(clampRequestLedgerLimit("200")).toBe(200);
    expect(clampRequestLedgerLimit("999")).toBe(200);
    expect(clampRequestLedgerLimit("bad")).toBe(200);
    expect(localDateValue(new Date(2026, 8, 13, 23, 59))).toBe("2026-09-13");
  });

  it("normalizes cache-rate range percentages", () => {
    expect(parseCachePercent("50")).toBe(50);
    expect(parseCachePercent("0.5")).toBe(0.5);
    expect(parseCachePercent("")).toBeUndefined();
    expect(parseCachePercent("101")).toBeUndefined();
    expect(normalizeCachePercentInput("-5")).toBe("0");
    expect(normalizeCachePercentInput("120")).toBe("100");
    expect(normalizeCachePercentInput("")).toBe("");
  });

  it("never treats not-reported cache as a zero-percent hit rate", () => {
    expect(requestCacheRatio(record())).toBe(0.8);
    expect(
      requestCacheRatio(record({
        cache_status: "not_reported",
        cache_hit_ratio: 0,
      })),
    ).toBeNull();
    expect(
      requestCacheRatio(record({
        cache_status: "error",
        cache_hit_ratio: 0,
      })),
    ).toBeNull();
  });

  it("aggregates request metrics and provider/model/cache distributions", () => {
    const records = [
      record(),
      record({
        llm_request_id: "req-2",
        status: "error",
        cache_status: "reported_zero",
        cache_hit_ratio: 0,
        usage: {
          prompt_tokens: 60,
          completion_tokens: 20,
          total_tokens: 80,
          cached_tokens: 0,
          cache_read_tokens: 0,
          cache_creation_tokens: 0,
          input_total_tokens: 60,
          cache_read_reported: true,
          cache_creation_reported: false,
          reasoning_tokens: 0,
        },
      }),
      record({
        llm_request_id: "req-3",
        provider: "anthropic",
        model: "claude",
        session_id: "session-b",
        cache_status: "not_reported",
        cache_hit_ratio: 0,
      }),
    ];

    const summary = summarizeRequestRecords(records);
    expect(summary).toMatchObject({
      requests: 3,
      successes: 2,
      failures: 1,
      inputTokens: 260,
      outputTokens: 60,
      totalTokens: 320,
      averageTokens: 107,
      sessionCount: 2,
      providerCount: 2,
      modelCount: 2,
      cacheReported: 2,
    });
    expect(summary.successRate).toBeCloseTo(2 / 3);
    expect(summary.averageCacheHitRatio).toBeCloseTo(0.4);

    expect(buildRequestBreakdown(records, "provider")[0]).toMatchObject({
      key: "openai",
      requests: 2,
      failures: 1,
    });
    expect(
      buildCacheStatusBreakdown(summary).map((row) => [row.key, row.requests]),
    ).toEqual([
      ["hit", 1],
      ["reported_zero", 1],
      ["not_reported", 1],
    ]);
  });

  it("uses backend totals before pagination while preserving cache samples", () => {
    const summary = requestSummaryFromAnalytics(
      {
        requests: 9311,
        successes: 9000,
        failures: 311,
        input_tokens: 1_000,
        output_tokens: 200,
        total_tokens: 1_200,
        cache_read_tokens: 800,
        cache_creation_tokens: 0,
        sessions: 4,
        providers: 2,
        models: 3,
        cache_reported: 9163,
        average_cache_hit_ratio: 0.93,
        cache_status_distribution: {
          hit: 9000,
          write: 0,
          confirmed_miss: 0,
          reported_zero: 163,
          not_reported: 117,
          error: 31,
        },
      },
      [record()],
    );
    expect(summary.requests).toBe(9311);
    expect(summary.cacheReported).toBe(9163);
    expect(summary.averageCacheHitRatio).toBe(0.93);
  });
});
