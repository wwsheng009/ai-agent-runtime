// 连接器绑定客户端单测（docs/bots/09 §5.2）：请求形状 / 载荷归一化 / 降级判据。
// 用 mock fetch 驱动真实 API 客户端（与 connectors.test.ts 同范式）。

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  bindConnectorSession,
  listConnectorBindings,
  normalizeConnectorBindings,
  unbindConnectorSession,
} from "./connector-bindings";

interface RecordedCall {
  url: string;
  method: string;
  body: unknown;
}

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const BINDING = {
  connector_id: "wx-1",
  chat: "chat-1",
  session_id: "sess-1",
  node_id: "node-a",
  updated_at: "2026-10-14T02:00:00Z",
  connector: {
    id: "wx-1",
    name: "微信机器人",
    provider: "wechat",
    enabled: true,
    active_owner: "runtime-server",
  },
};

describe("connector bindings client", () => {
  const originalFetch = globalThis.fetch;
  let calls: RecordedCall[];

  beforeEach(() => {
    calls = [];
    globalThis.fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://localhost");
      calls.push({
        url: url.pathname + url.search,
        method: (init?.method ?? "GET").toUpperCase(),
        body: typeof init?.body === "string" ? JSON.parse(init.body) : undefined,
      });
      // 列表返回 {items}；单个绑定（PUT/DELETE）返回绑定本体。
      if (url.pathname.endsWith("/bindings")) {
        return jsonResponse({ items: [BINDING] });
      }
      return jsonResponse(BINDING);
    }) as typeof fetch;
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  it("listConnectorBindings：GET /bindings + 过滤参数 + 归一化", async () => {
    const items = await listConnectorBindings({
      connectorId: " wx-1 ",
      chat: " chat-1 ",
    });
    expect(calls[0].method).toBe("GET");
    expect(calls[0].url).toBe("/api/connectors/bindings?connector_id=wx-1&chat=chat-1");
    expect(items).toHaveLength(1);
    expect(items[0]).toMatchObject({
      connector_id: "wx-1",
      chat: "chat-1",
      session_id: "sess-1",
      node_id: "node-a",
      connector: { id: "wx-1", provider: "wechat", active_owner: "runtime-server" },
    });
  });

  it("bindConnectorSession：PUT + session_id/expected 透传（trim）", async () => {
    await bindConnectorSession("wx-1", " chat-1 ", {
      sessionId: " sess-2 ",
      expectedUpdatedAt: " 2026-10-14T01:00:00Z ",
    });
    expect(calls[0].method).toBe("PUT");
    expect(calls[0].url).toBe("/api/connectors/wx-1/bindings/chat-1");
    expect(calls[0].body).toEqual({
      session_id: "sess-2",
      expected_updated_at: "2026-10-14T01:00:00Z",
    });
  });

  it("unbindConnectorSession：DELETE 空 body", async () => {
    await unbindConnectorSession("wx-1", "chat-1");
    expect(calls[0].method).toBe("DELETE");
    expect(calls[0].body).toBeUndefined();
  });

  it("bindConnectorSession：空 chat 拒绝", async () => {
    await expect(bindConnectorSession("wx-1", "   ", { sessionId: "s" })).rejects.toThrow(
      /chat/,
    );
    expect(calls).toHaveLength(0);
  });

  it("normalizeConnectorBindings：items 缺失抛错；单条缺 id/chat 丢弃", () => {
    expect(() => normalizeConnectorBindings({})).toThrow(/items/);
    expect(
      normalizeConnectorBindings({
        items: [
          BINDING,
          { chat: "no-connector" },
          { connector_id: "no-chat" },
        ],
      }),
    ).toHaveLength(1);
  });
});
