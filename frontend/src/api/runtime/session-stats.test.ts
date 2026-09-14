// P2-1A：会话统计客户端单测（归一化 / 请求口径 / 降级分类）。

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  SESSION_STATS_PATH,
  fetchRuntimeSessionStats,
  isSessionStatsUnavailable,
  normalizeSessionStats,
  normalizeSessionStatsResponse,
} from "@/api/runtime/session-stats";
import { RuntimeApiError } from "@/api/runtime/shared";

describe("normalizeSessionStats", () => {
  it("解析 chat.SessionStatistics（totalMessages 为 camelCase，tags 为计数表）", () => {
    expect(
      normalizeSessionStats({
        total: 7,
        active: 2,
        idle: 3,
        closed: 1,
        archived: 1,
        totalMessages: 42,
        tags: { support: 3, billing: 1 },
      }),
    ).toEqual({
      total: 7,
      active: 2,
      idle: 3,
      closed: 1,
      archived: 1,
      totalMessages: 42,
      tags: { support: 3, billing: 1 },
    });
  });

  it("缺失/非法计数按 0 计，负数与小数被收敛", () => {
    expect(
      normalizeSessionStats({
        total: -5,
        active: 2.9,
        idle: Number.NaN,
        closed: "3",
        archived: Number.POSITIVE_INFINITY,
      }),
    ).toEqual({
      total: 0,
      active: 2,
      idle: 0,
      closed: 0,
      archived: 0,
      totalMessages: 0,
      tags: {},
    });
  });

  it("tags 非对象按空表处理，非有限数条目被丢弃", () => {
    expect(normalizeSessionStats({ tags: ["support"] }).tags).toEqual({});
    expect(
      normalizeSessionStats({ tags: { support: 2, broken: "x", neg: -1 } }).tags,
    ).toEqual({ support: 2 });
  });

  it("结构不是对象即抛错（不伪装成 0 计数）", () => {
    expect(() => normalizeSessionStats(null)).toThrow();
    expect(() => normalizeSessionStats([])).toThrow();
    expect(() => normalizeSessionStats("stats")).toThrow();
  });
});

describe("normalizeSessionStatsResponse", () => {
  it("读取 user_id 与 stats", () => {
    const result = normalizeSessionStatsResponse({
      user_id: "alice",
      stats: { total: 1, active: 1 },
    });
    expect(result.user_id).toBe("alice");
    expect(result.stats.total).toBe(1);
  });

  it("stats 缺失或结构异常即抛错；user_id 非字符串回落空串", () => {
    expect(() => normalizeSessionStatsResponse({ user_id: "alice" })).toThrow();
    expect(() => normalizeSessionStatsResponse(null)).toThrow();
    expect(normalizeSessionStatsResponse({ stats: { total: 0 } }).user_id).toBe("");
  });
});

describe("isSessionStatsUnavailable", () => {
  it("404/405/501/503 视为统计不可用", () => {
    for (const status of [404, 405, 501, 503]) {
      expect(isSessionStatsUnavailable(new RuntimeApiError(status, null))).toBe(true);
    }
  });

  it("其余状态与未知错误按真实失败处理", () => {
    expect(isSessionStatsUnavailable(new RuntimeApiError(500, null))).toBe(false);
    expect(isSessionStatsUnavailable(new Error("boom"))).toBe(false);
    expect(isSessionStatsUnavailable(undefined)).toBe(false);
  });
});

describe("fetchRuntimeSessionStats", () => {
  const originalFetch = globalThis.fetch;
  let calls: Array<{ url: string; init?: RequestInit }> = [];

  function respondWith(body: unknown, status = 200) {
    globalThis.fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      calls.push({ url: String(input), init });
      return new Response(JSON.stringify(body), {
        status,
        headers: { "Content-Type": "application/json" },
      });
    }) as typeof fetch;
  }

  beforeEach(() => {
    calls = [];
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  it("GET 统计端点；有 userId 才带 user_id 查询参数", async () => {
    respondWith({ user_id: "alice", stats: { total: 2, archived: 1 } });

    const result = await fetchRuntimeSessionStats(" alice ");

    expect(calls).toHaveLength(1);
    const url = new URL(calls[0].url, "http://runtime.test");
    expect(url.pathname).toBe(SESSION_STATS_PATH);
    expect(url.searchParams.get("user_id")).toBe("alice");
    expect(result.stats).toEqual({
      total: 2,
      active: 0,
      idle: 0,
      closed: 0,
      archived: 1,
      totalMessages: 0,
      tags: {},
    });
  });

  it("空 userId 不带 user_id（交给服务端取默认用户）", async () => {
    respondWith({ user_id: "default", stats: { total: 0 } });

    await fetchRuntimeSessionStats("   ");

    const url = new URL(calls[0].url, "http://runtime.test");
    expect(url.searchParams.has("user_id")).toBe(false);
  });

  it("503 抛出 RuntimeApiError 供降级判据使用", async () => {
    respondWith({ error: "session manager not configured" }, 503);

    const error = await fetchRuntimeSessionStats().catch((caught: unknown) => caught);

    expect(error).toBeInstanceOf(RuntimeApiError);
    expect(isSessionStatsUnavailable(error)).toBe(true);
  });

  it("stats 结构异常（非对象）即抛错，不静默返回空统计", async () => {
    respondWith({ user_id: "alice", stats: "bad" });

    const error = await fetchRuntimeSessionStats().catch((caught: unknown) => caught);

    expect(error).toBeInstanceOf(Error);
    expect(isSessionStatsUnavailable(error)).toBe(false);
  });
});
