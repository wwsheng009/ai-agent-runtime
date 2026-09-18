// MCP 管理客户端单测：请求形状（URL / method / body）+ 错误信封分类。
//
// 覆盖纪律：
//   * name 必须整段 URL 编码（MCP 名称允许空格）；
//   * 非 2xx 必须抛 RuntimeApiError 并保留后端 code（400/404/403 由 UI 分支处理）；
//   * 列表结构异常必须抛错，不能降级成「空列表」。

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  buildRuntimeMcpDisableUrl,
  buildRuntimeMcpEnableUrl,
  buildRuntimeMcpReloadUrl,
  buildRuntimeMcpToolsUrl,
  buildRuntimeMcpUrl,
  createRuntimeMcp,
  deleteRuntimeMcp,
  listRuntimeMcps,
  listRuntimeMcpTools,
  reloadRuntimeMcps,
  setRuntimeMcpEnabled,
  updateRuntimeMcp,
} from "@/api/runtime/mcp";
import {
  isRuntimeApiErrorCode,
  RuntimeApiError,
} from "@/api/runtime/shared";

const MCP_NAME = "chrome mcp";

const MCP_ENTRY = {
  config: {
    name: MCP_NAME,
    description: "browser tools",
    type: "stdio",
    command: "npx",
    args: ["-y", "@chrome/mcp"],
    env: { TOKEN: "x" },
    enabled: true,
    disabled: false,
    timeout: "30s",
    maxRetry: 2,
  },
  status: {
    name: MCP_NAME,
    type: "stdio",
    trustLevel: "local",
    executionMode: "local_mcp",
    enabled: true,
    connected: true,
    toolCount: 27,
    lastError: "",
    lastConnect: "2026-09-18T08:00:00Z",
  },
};

function jsonResponse(payload: unknown, status = 200) {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function mockFetch(
  handler: (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>,
) {
  const fetchMock = vi.fn(handler);
  globalThis.fetch = fetchMock as unknown as typeof fetch;
  return fetchMock;
}

describe("runtime MCP 客户端", () => {
  const originalFetch = globalThis.fetch;

  beforeEach(() => {
    globalThis.fetch = originalFetch;
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
    vi.restoreAllMocks();
  });

  it("GET 列表：保留 count 与 config/status，不丢字段", async () => {
    const fetchMock = mockFetch(async (input, init) => {
      expect(String(input)).toBe("/api/runtime/mcps");
      expect((init?.method ?? "GET").toUpperCase()).toBe("GET");
      return jsonResponse({ count: 1, mcps: [MCP_ENTRY] });
    });

    const result = await listRuntimeMcps();

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(result.count).toBe(1);
    expect(result.mcps).toEqual([MCP_ENTRY]);
    expect(result.mcps[0]?.status.toolCount).toBe(27);
  });

  it("GET 列表：mcps 不是数组时抛错（契约回归不能被当成空列表）", async () => {
    mockFetch(async () => jsonResponse({ count: 0 }));

    await expect(listRuntimeMcps()).rejects.toThrow(
      /mcps must be an array/,
    );
  });

  it("GET 工具清单：name 整段编码，tools 原样返回", async () => {
    const fetchMock = mockFetch(async (input, init) => {
      expect(String(input)).toBe(
        `/api/runtime/mcps/${encodeURIComponent(MCP_NAME)}/tools`,
      );
      expect((init?.method ?? "GET").toUpperCase()).toBe("GET");
      return jsonResponse({
        name: MCP_NAME,
        count: 2,
        tools: [
          {
            name: "navigate_page",
            description: "Navigate to a URL",
            enabled: true,
            inputSchema: { type: "object" },
          },
          { name: "take_snapshot", enabled: false },
        ],
      });
    });

    const result = await listRuntimeMcpTools(MCP_NAME);

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(buildRuntimeMcpToolsUrl(MCP_NAME)).toBe(
      `/api/runtime/mcps/${encodeURIComponent(MCP_NAME)}/tools`,
    );
    expect(result.count).toBe(2);
    expect(result.tools.map((tool) => tool.name)).toEqual([
      "navigate_page",
      "take_snapshot",
    ]);
    expect(result.tools[1]?.enabled).toBe(false);
  });

  it("GET 工具清单：空数组是合法结果（未启用/未连接）", async () => {
    mockFetch(async () => jsonResponse({ name: MCP_NAME, count: 0, tools: [] }));

    const result = await listRuntimeMcpTools(MCP_NAME);

    expect(result.count).toBe(0);
    expect(result.tools).toEqual([]);
  });

  it("GET 工具清单：tools 不是数组时抛错（不能伪装成无工具）", async () => {
    mockFetch(async () => jsonResponse({ name: MCP_NAME, count: 0 }));

    await expect(listRuntimeMcpTools(MCP_NAME)).rejects.toThrow(
      /tools must be an array/,
    );
  });

  it("POST 新增：body 为 UpsertRequest JSON，201 返回 config/status", async () => {
    const request = {
      name: MCP_NAME,
      type: "stdio" as const,
      command: "npx",
      args: ["-y", "@chrome/mcp"],
      env: { TOKEN: "x" },
      enabled: true,
      trustLevel: "local" as const,
      timeoutSeconds: 30,
      maxParallelCalls: 2,
    };
    const fetchMock = mockFetch(async (input, init) => {
      expect(String(input)).toBe("/api/runtime/mcps");
      expect((init?.method ?? "").toUpperCase()).toBe("POST");
      expect(JSON.parse(String(init?.body))).toEqual(request);
      return jsonResponse(MCP_ENTRY, 201);
    });

    const result = await createRuntimeMcp(request);

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(result.config.name).toBe(MCP_NAME);
    expect(result.status.connected).toBe(true);
  });

  it("PUT 更新：name 整段 URL 编码", async () => {
    const fetchMock = mockFetch(async (input) => {
      expect(String(input)).toBe(
        `/api/runtime/mcps/${encodeURIComponent(MCP_NAME)}`,
      );
      return jsonResponse(MCP_ENTRY);
    });

    await updateRuntimeMcp(MCP_NAME, { name: MCP_NAME, type: "stdio" });

    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("DELETE：返回 { name, removed }，removed 非 true 时归一为 false", async () => {
    mockFetch(async (input, init) => {
      expect(String(input)).toBe(buildRuntimeMcpUrl(MCP_NAME));
      expect((init?.method ?? "").toUpperCase()).toBe("DELETE");
      return jsonResponse({ name: MCP_NAME, removed: true });
    });

    await expect(deleteRuntimeMcp(MCP_NAME)).resolves.toEqual({
      name: MCP_NAME,
      removed: true,
    });

    mockFetch(async () => jsonResponse({}, 200));
    await expect(deleteRuntimeMcp(MCP_NAME)).resolves.toEqual({
      name: MCP_NAME,
      removed: false,
    });
  });

  it("enable / disable：POST 到对应子路径且不带 body", async () => {
    const calls: string[] = [];
    mockFetch(async (input, init) => {
      calls.push(`${String(input)} ${(init?.method ?? "").toUpperCase()}`);
      return jsonResponse(MCP_ENTRY);
    });

    await setRuntimeMcpEnabled(MCP_NAME, true);
    await setRuntimeMcpEnabled(MCP_NAME, false);

    expect(calls).toEqual([
      `${buildRuntimeMcpEnableUrl(MCP_NAME)} POST`,
      `${buildRuntimeMcpDisableUrl(MCP_NAME)} POST`,
    ]);
  });

  it("reload：POST /api/runtime/mcps/reload", async () => {
    mockFetch(async (input, init) => {
      expect(String(input)).toBe(buildRuntimeMcpReloadUrl());
      expect((init?.method ?? "").toUpperCase()).toBe("POST");
      return jsonResponse({ reloaded: true, count: 1 });
    });

    await expect(reloadRuntimeMcps()).resolves.toEqual({
      reloaded: true,
      count: 1,
    });
  });

  it("400/404/403：抛 RuntimeApiError 并保留后端 code 与 message", async () => {
    const cases = [
      { status: 400, code: "validation_failed", message: "name is required" },
      { status: 404, code: "not_found", message: "mcp not found" },
      { status: 403, code: "forbidden", message: "read-only policy" },
    ];

    for (const item of cases) {
      mockFetch(async () =>
        jsonResponse(
          { error: { code: item.code, message: item.message } },
          item.status,
        ),
      );

      const error = await deleteRuntimeMcp(MCP_NAME).catch((err) => err);

      expect(error).toBeInstanceOf(RuntimeApiError);
      expect((error as RuntimeApiError).status).toBe(item.status);
      expect(isRuntimeApiErrorCode(error, item.code)).toBe(true);
      expect((error as RuntimeApiError).message).toBe(item.message);
    }
  });
});
