import { afterEach, describe, expect, it, vi } from "vitest";

import {
  LSP_OBSERVE_EVENT_TYPES,
  fetchLspObserveFeed,
  listLspObserveEvents,
} from "@/api/runtime/lsp";

function jsonResponse(payload: unknown, status = 200) {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function queryResult(events: Array<Record<string, unknown>>, latestSeq: number) {
  return {
    events,
    after_seq: 0,
    latest_seq: latestSeq,
    oldest_available_seq: 1,
    next_cursor: null,
    partial: false,
    count: events.length,
  };
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("listLspObserveEvents", () => {
  it("解析 envelope 的 data，并透传 event_type/limit 与 Bearer 令牌", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      expect(url).toContain("/api/runtime/observe/v1/events");
      expect(url).toContain("event_type=lsp.request.finished");
      expect(url).toContain("limit=50");
      const headers = init?.headers as Record<string, string> | undefined;
      expect(headers?.Authorization).toBe("Bearer token-1");
      return jsonResponse({
        ok: true,
        data: queryResult(
          [{ observation_seq: 2, timestamp: "2026-10-01T00:00:01Z", type: "lsp.request.finished" }],
          2,
        ),
      });
    });
    vi.stubGlobal("fetch", fetchMock);

    const result = await listLspObserveEvents({
      eventType: "lsp.request.finished",
      limit: 50,
      adminToken: "token-1",
    });

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(result.count).toBe(1);
    expect(result.events[0]?.observation_seq).toBe(2);
  });

  it("envelope 缺少 data 时如实报错（不把未采集渲染成空结果）", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({ ok: true })));

    await expect(listLspObserveEvents({ eventType: "lsp.server.state" })).rejects.toThrow(
      /missing data/,
    );
  });

  it("HTTP 403 由 fetchRuntimeJson 抛 RuntimeApiError（面板据此降级）", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse({ error: "runtime observation disabled" }, 403)),
    );

    await expect(listLspObserveEvents({ eventType: "lsp.server.state" })).rejects.toMatchObject({
      status: 403,
    });
  });
});

describe("fetchLspObserveFeed", () => {
  it("按类型并发拉取、合并去重并按 observation_seq 倒序", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = new URL(String(input), "http://localhost");
        const eventType = url.searchParams.get("event_type") ?? "";
        // request.finished 与 diagnostics.updated 都返回 seq=5，用于验证跨类型去重。
        const seq = eventType === "lsp.server.state" ? 7 : 5;
        return jsonResponse({
          ok: true,
          data: queryResult(
            [{ observation_seq: seq, timestamp: "2026-10-01T00:00:00Z", type: eventType }],
            seq,
          ),
        });
      }),
    );

    const feed = await fetchLspObserveFeed({ limit: 10 });

    expect(Object.keys(feed.byType).sort()).toEqual([...LSP_OBSERVE_EVENT_TYPES].sort());
    expect(feed.events.map((event) => event.observation_seq)).toEqual([7, 5]);
  });

  it("任一类型失败即整体失败（不拿部分数据冒充全量）", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = new URL(String(input), "http://localhost");
        if (url.searchParams.get("event_type") === "lsp.diagnostics.updated") {
          return jsonResponse({ error: "runtime observation disabled" }, 403);
        }
        return jsonResponse({ ok: true, data: queryResult([], 0) });
      }),
    );

    await expect(fetchLspObserveFeed()).rejects.toMatchObject({ status: 403 });
  });
});
