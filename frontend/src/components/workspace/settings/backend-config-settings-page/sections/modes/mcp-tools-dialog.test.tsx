// @vitest-environment jsdom

// MCP 工具对话框单测：mock 全局 fetch（走真实 API 客户端），验证
//   * 逐工具开关调用正确的 enable/disable URL（tool 名整段编码）；
//   * 批量「全部启用 / 全部禁用」请求体只带需要变更的 tools 列表，全目标态时为空数组；
//   * 失败态展示（role=alert）且不破坏已加载列表；
//   * 既有 a11y 约定（Esc 关闭）仍生效。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { RuntimeMcpTool } from "@/types/runtime";

import { McpToolsDialog } from "./mcp-tools-dialog";

type RecordedCall = { url: string; method: string; body: string | null };

const MCP_NAME = "chrome mcp";
const TOOLS_URL = `/api/runtime/mcps/${encodeURIComponent(MCP_NAME)}/tools`;

const originalFetch = globalThis.fetch;

let currentTools: RuntimeMcpTool[] = [];
let calls: RecordedCall[] = [];
let failWrites = false;

function jsonResponse(payload: unknown, status = 200) {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

/** 模拟后端：GET 返回当前清单；POST 变更配置态并回显本次目标。 */
function installFetchMock(tools: RuntimeMcpTool[]) {
  currentTools = tools.map((tool) => ({ ...tool }));
  calls = [];
  failWrites = false;
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = (init?.method ?? "GET").toUpperCase();
    calls.push({
      url,
      method,
      body: typeof init?.body === "string" ? init.body : null,
    });

    if (method === "GET" && url === TOOLS_URL) {
      return jsonResponse({
        name: MCP_NAME,
        count: currentTools.length,
        tools: currentTools,
      });
    }

    if (!url.startsWith(`${TOOLS_URL}/`) || method !== "POST") {
      return jsonResponse({ error: { code: "not_found", message: "unexpected" } }, 404);
    }
    if (failWrites) {
      return jsonResponse(
        { error: { code: "internal_error", message: "boom" } },
        500,
      );
    }

    const tail = url.slice(TOOLS_URL.length + 1);
    if (tail === "enable" || tail === "disable") {
      const enabled = tail === "enable";
      const parsed = JSON.parse(String(init?.body ?? "{}")) as {
        tools?: unknown;
      };
      const requested = Array.isArray(parsed.tools) ? (parsed.tools as string[]) : [];
      const targets =
        requested.length > 0 ? requested : currentTools.map((tool) => tool.name);
      currentTools = currentTools.map((tool) =>
        targets.includes(tool.name)
          ? { ...tool, enabled, configured_enabled: enabled }
          : tool,
      );
      return jsonResponse({ name: MCP_NAME, enabled, tools: targets });
    }

    const match = /^(.+)\/(enable|disable)$/.exec(tail);
    if (!match) {
      return jsonResponse({ error: { code: "not_found", message: "bad tool path" } }, 404);
    }
    const toolName = decodeURIComponent(match[1]);
    const enabled = match[2] === "enable";
    currentTools = currentTools.map((tool) =>
      tool.name === toolName
        ? { ...tool, enabled, configured_enabled: enabled }
        : tool,
    );
    return jsonResponse({ name: MCP_NAME, tool: toolName, enabled });
  });
  globalThis.fetch = fetchMock as unknown as typeof fetch;
  return fetchMock;
}

function writeCalls() {
  return calls.filter((call) => call.method === "POST");
}

function tool(name: string, overrides: Partial<RuntimeMcpTool> = {}): RuntimeMcpTool {
  return { name, enabled: true, ...overrides };
}

function flush() {
  let chain = Promise.resolve();
  for (let index = 0; index < 6; index += 1) {
    chain = chain.then(() => undefined);
  }
  return chain;
}

function toggleFor(name: string) {
  return document.body.querySelector<HTMLInputElement>(
    `[data-testid="mcp-tool-toggle-${name}"]`,
  );
}

function rowFor(name: string) {
  return document.body.querySelector(`[data-testid="mcp-tool-row-${name}"]`);
}

describe("McpToolsDialog", () => {
  let container: HTMLDivElement;
  let root: Root | null = null;
  const onClose = vi.fn();

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (
      globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
    ).IS_REACT_ACT_ENVIRONMENT = true;
    onClose.mockReset();
  });

  afterEach(() => {
    if (root) {
      act(() => root?.unmount());
    }
    root = null;
    container.remove();
    document.body.innerHTML = "";
    delete (
      globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
    ).IS_REACT_ACT_ENVIRONMENT;
    globalThis.fetch = originalFetch;
    vi.restoreAllMocks();
  });

  async function renderDialog() {
    await act(async () => {
      root?.render(<McpToolsDialog name={MCP_NAME} onClose={onClose} />);
    });
    await act(flush);
  }

  async function click(element: Element | null) {
    await act(async () => {
      (element as HTMLElement | null)?.click();
    });
    await act(flush);
  }

  it("渲染工具行：configured_enabled=false 显示已禁用，healthy=false 显示健康异常，未生效单独标注", async () => {
    installFetchMock([
      tool("alpha", { configured_enabled: true, healthy: true }),
      tool("beta", { enabled: false, configured_enabled: false, healthy: true }),
      tool("gamma", { configured_enabled: true, healthy: false }),
      tool("delta", { enabled: false, configured_enabled: true, healthy: true }),
    ]);

    await renderDialog();

    expect(calls[0]).toEqual({ url: TOOLS_URL, method: "GET", body: null });
    expect(document.body.querySelectorAll('[data-testid^="mcp-tool-row-"]')).toHaveLength(4);
    expect(rowFor("beta")?.textContent).toContain("已禁用");
    expect(rowFor("gamma")?.textContent).toContain("健康异常");
    expect(rowFor("gamma")?.textContent).toContain("运行时健康检查异常");
    expect(rowFor("delta")?.textContent).toContain("当前未生效");

    expect(toggleFor("alpha")?.checked).toBe(true);
    expect(toggleFor("beta")?.checked).toBe(false);
    expect(toggleFor("gamma")?.checked).toBe(true);
    expect(document.body.querySelector('[data-testid="mcp-tools-enable-all"]')).not.toBeNull();
    expect(document.body.querySelector('[data-testid="mcp-tools-disable-all"]')).not.toBeNull();
  });

  it("逐工具开关：关闭调用 disable URL、再次开启调用 enable URL，并局部刷新列表", async () => {
    installFetchMock([
      tool("alpha", { configured_enabled: true, healthy: true }),
    ]);

    await renderDialog();

    await click(toggleFor("alpha"));

    expect(writeCalls()).toEqual([
      {
        url: `${TOOLS_URL}/alpha/disable`,
        method: "POST",
        body: null,
      },
    ]);
    expect(currentTools[0]?.configured_enabled).toBe(false);
    expect(toggleFor("alpha")?.checked).toBe(false);
    expect(rowFor("alpha")?.textContent).toContain("已禁用");
    // 操作后必须重新拉取清单（局部刷新），而不是只改本地开关。
    expect(calls.filter((call) => call.method === "GET")).toHaveLength(2);

    await click(toggleFor("alpha"));

    expect(writeCalls()[1]).toEqual({
      url: `${TOOLS_URL}/alpha/enable`,
      method: "POST",
      body: null,
    });
    expect(toggleFor("alpha")?.checked).toBe(true);
  });

  it("批量全部启用：只提交缺失启用的工具列表", async () => {
    installFetchMock([
      tool("alpha", { enabled: false, configured_enabled: false }),
      tool("beta", { configured_enabled: true }),
      tool("gamma", { enabled: false, configured_enabled: undefined }),
    ]);

    await renderDialog();
    await click(document.body.querySelector('[data-testid="mcp-tools-enable-all"]'));

    expect(writeCalls()).toEqual([
      {
        url: `${TOOLS_URL}/enable`,
        method: "POST",
        body: JSON.stringify({ tools: ["alpha", "gamma"] }),
      },
    ]);
    expect(toggleFor("alpha")?.checked).toBe(true);
    expect(toggleFor("gamma")?.checked).toBe(true);
  });

  it("批量全部禁用：提交当前启用的工具列表", async () => {
    installFetchMock([
      tool("alpha", { configured_enabled: true }),
      tool("beta", { configured_enabled: true }),
    ]);

    await renderDialog();
    await click(document.body.querySelector('[data-testid="mcp-tools-disable-all"]'));

    expect(writeCalls()).toEqual([
      {
        url: `${TOOLS_URL}/disable`,
        method: "POST",
        body: JSON.stringify({ tools: ["alpha", "beta"] }),
      },
    ]);
    expect(toggleFor("alpha")?.checked).toBe(false);
  });

  it("批量全部启用：所有工具已启用时提交空数组（后端语义 = 全部工具）", async () => {
    installFetchMock([
      tool("alpha", { configured_enabled: true }),
      tool("beta", { configured_enabled: true }),
    ]);

    await renderDialog();
    await click(document.body.querySelector('[data-testid="mcp-tools-enable-all"]'));

    expect(writeCalls()).toEqual([
      {
        url: `${TOOLS_URL}/enable`,
        method: "POST",
        body: JSON.stringify({ tools: [] }),
      },
    ]);
  });

  it("操作失败：展示 role=alert 的错误提示并保留已加载列表", async () => {
    installFetchMock([tool("alpha", { configured_enabled: true })]);
    failWrites = true;

    await renderDialog();
    await click(toggleFor("alpha"));

    const errorBox = document.body.querySelector('[data-testid="mcp-tools-action-error"]');
    expect(errorBox?.getAttribute("role")).toBe("alert");
    expect(errorBox?.textContent).toContain("工具操作失败");
    expect(errorBox?.textContent).toContain("boom");
    expect(document.body.querySelector('[data-testid="mcp-tools-list"]')).not.toBeNull();
    expect(rowFor("alpha")).not.toBeNull();
  });

  it("Esc 关闭：保留对话框既有键盘约定", async () => {
    installFetchMock([tool("alpha")]);

    await renderDialog();

    await act(async () => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    });

    expect(onClose).toHaveBeenCalledTimes(1);
  });
});
