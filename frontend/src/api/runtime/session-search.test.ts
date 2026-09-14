// P2-1A：会话元数据检索客户端单测（归一化 / 请求体 / 降级分类）。

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  DEFAULT_SESSION_SEARCH_LIMIT,
  buildSessionSearchBody,
  isSessionSearchUnavailable,
  normalizeSessionSearchFilters,
  normalizeSessionSearchRecord,
  normalizeSessionSearchResponse,
  searchRuntimeSessions,
} from "@/api/runtime/session-search";
import { RuntimeApiError } from "@/api/runtime/shared";

describe("normalizeSessionSearchFilters", () => {
  it("标签去空白/去重/去空串，user 与 state 空值省略", () => {
    expect(
      normalizeSessionSearchFilters({
        userId: "  web-console ",
        state: " archived ",
        tags: [" support ", "", "support", "billing"],
        limit: 20.7,
        offset: -3,
      }),
    ).toEqual({
      userId: "web-console",
      state: "archived",
      tags: ["support", "billing"],
      limit: 20,
      offset: 0,
    });
  });

  it("缺省时补默认 limit 与 offset=0，且不产生空字段", () => {
    expect(normalizeSessionSearchFilters()).toEqual({
      limit: DEFAULT_SESSION_SEARCH_LIMIT,
      offset: 0,
    });
    expect(normalizeSessionSearchFilters({ tags: ["   "] })).toEqual({
      limit: DEFAULT_SESSION_SEARCH_LIMIT,
      offset: 0,
    });
  });
});

describe("buildSessionSearchBody", () => {
  it("请求体为 snake_case，仅带非空筛选 + limit/offset", () => {
    expect(
      buildSessionSearchBody({
        userId: "u1",
        tags: ["a"],
        state: "idle",
        limit: 10,
        offset: 5,
      }),
    ).toEqual({ user_id: "u1", tags: ["a"], state: "idle", limit: 10, offset: 5 });

    expect(buildSessionSearchBody({ limit: 10, offset: 0 })).toEqual({
      limit: 10,
      offset: 0,
    });
  });
});

describe("normalizeSessionSearchRecord", () => {
  it("解析后端 chat.Session（camelCase + metadata.tags）", () => {
    expect(
      normalizeSessionSearchRecord({
        id: "sess-1",
        userId: "u1",
        state: "active",
        createdAt: "2026-09-13T09:00:00Z",
        updatedAt: "2026-09-13T10:00:00Z",
        metadata: {
          title: "支持会话",
          summary: "…",
          tags: ["support", "billing"],
          totalTurns: 3,
          lastSkill: "execute",
        },
      }),
    ).toEqual({
      id: "sess-1",
      userId: "u1",
      state: "active",
      createdAt: "2026-09-13T09:00:00Z",
      updatedAt: "2026-09-13T10:00:00Z",
      metadata: {
        title: "支持会话",
        summary: "…",
        tags: ["support", "billing"],
        totalTurns: 3,
        lastSkill: "execute",
      },
    });
  });

  it("缺 id 或非对象记录返回 null（调用方丢弃，不造占位 id）", () => {
    expect(normalizeSessionSearchRecord({ state: "active" })).toBeNull();
    expect(normalizeSessionSearchRecord({ id: "   " })).toBeNull();
    expect(normalizeSessionSearchRecord("sess-1")).toBeNull();
    expect(normalizeSessionSearchRecord(null)).toBeNull();
  });
});

describe("normalizeSessionSearchResponse", () => {
  it("解析 sessions/count/filters（回显为 camelCase）", () => {
    const result = normalizeSessionSearchResponse({
      sessions: [
        { id: "sess-1", state: "archived", metadata: { tags: ["support"] } },
        { state: "active" },
      ],
      count: 9,
      filters: { userId: "u1", tags: ["support"], state: "archived", limit: 25, offset: 5 },
    });

    expect(result.sessions.map((session) => session.id)).toEqual(["sess-1"]);
    // count 采用后端回显值（含被丢弃的异常记录），避免与真实命中数混淆。
    expect(result.count).toBe(9);
    expect(result.filters).toEqual({
      userId: "u1",
      tags: ["support"],
      state: "archived",
      limit: 25,
      offset: 5,
    });
  });

  it("filters 缺失时回落请求条件，count 缺失时以有效记录数兜底", () => {
    const result = normalizeSessionSearchResponse(
      { sessions: [{ id: "sess-1" }] },
      { tags: ["support"], limit: 50, offset: 0 },
    );

    expect(result.count).toBe(1);
    expect(result.filters).toEqual({ tags: ["support"], limit: 50, offset: 0 });
  });

  it("sessions 不是数组时抛错（不把结构异常伪装成空结果）", () => {
    expect(() => normalizeSessionSearchResponse({ count: 0 })).toThrow();
    expect(() => normalizeSessionSearchResponse({ sessions: {} })).toThrow();
    expect(() => normalizeSessionSearchResponse(null)).toThrow();
  });
});

describe("isSessionSearchUnavailable", () => {
  it("404/405/501/503 视为服务端检索不可用", () => {
    for (const status of [404, 405, 501, 503]) {
      expect(isSessionSearchUnavailable(new RuntimeApiError(status, null))).toBe(true);
    }
  });

  it("其余状态与未知错误按真实失败处理", () => {
    expect(isSessionSearchUnavailable(new RuntimeApiError(500, null))).toBe(false);
    expect(isSessionSearchUnavailable(new RuntimeApiError(400, null))).toBe(false);
    expect(isSessionSearchUnavailable(new Error("boom"))).toBe(false);
    expect(isSessionSearchUnavailable(undefined)).toBe(false);
  });
});

describe("searchRuntimeSessions", () => {
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

  it("POST /api/runtime/sessions/search，请求体为 snake_case", async () => {
    respondWith({
      sessions: [{ id: "sess-1", state: "active", metadata: { tags: ["support"] } }],
      count: 1,
      filters: { userId: "u1", tags: ["support"], limit: 50, offset: 0 },
    });

    const result = await searchRuntimeSessions({
      userId: "u1",
      tags: ["support", " support "],
    });

    expect(calls).toHaveLength(1);
    const url = new URL(calls[0].url, "http://runtime.test");
    expect(url.pathname).toBe("/api/runtime/sessions/search");
    expect(calls[0].init?.method).toBe("POST");
    expect(JSON.parse(String(calls[0].init?.body))).toEqual({
      user_id: "u1",
      tags: ["support"],
      limit: DEFAULT_SESSION_SEARCH_LIMIT,
      offset: 0,
    });

    expect(result.sessions).toHaveLength(1);
    expect(result.sessions[0].metadata?.tags).toEqual(["support"]);
  });

  it("503 存储不可用抛出 RuntimeApiError 供降级判据使用", async () => {
    respondWith({ error: "session store unavailable", code: "STORE_UNAVAILABLE" }, 503);

    const error = await searchRuntimeSessions({ tags: ["support"] }).catch(
      (caught: unknown) => caught,
    );

    expect(error).toBeInstanceOf(RuntimeApiError);
    expect((error as RuntimeApiError).status).toBe(503);
    expect(isSessionSearchUnavailable(error)).toBe(true);
  });
});
