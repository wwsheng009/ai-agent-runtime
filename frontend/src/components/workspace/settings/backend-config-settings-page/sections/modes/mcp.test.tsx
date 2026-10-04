// @vitest-environment jsdom
//
// MCP 管理面板「配置来源」诊断卡：
//   * 渲染生效文件 + 来源徽标 + 候选存在性 + 汇总（解释"为什么某个 server 没加载"）；
//   * 旧后端省略 config 字段时不渲染该卡（向后兼容）。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { RuntimeMcpEntry } from "@/types/runtime";

import { McpModeSection } from "./mcp";

const { listRuntimeMcpsMock } = vi.hoisted(() => ({
  listRuntimeMcpsMock: vi.fn(),
}));

vi.mock("@/api/runtime/mcp", () => ({
  listRuntimeMcps: listRuntimeMcpsMock,
  createRuntimeMcp: vi.fn(),
  updateRuntimeMcp: vi.fn(),
  setRuntimeMcpEnabled: vi.fn(),
  deleteRuntimeMcp: vi.fn(),
  reloadRuntimeMcps: vi.fn(),
}));

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

function buildEntry(): RuntimeMcpEntry {
  return {
    config: { name: "docs", type: "stdio", command: "npx", args: [] },
    status: {
      name: "docs",
      type: "stdio",
      enabled: true,
      connected: true,
      toolCount: 2,
    },
  };
}

describe("McpModeSection diagnostics", () => {
  let container: HTMLDivElement;
  let root: Root | null;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    listRuntimeMcpsMock.mockReset();
  });

  afterEach(() => {
    if (root) {
      act(() => root?.unmount());
    }
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  async function renderSection() {
    await act(async () => {
      root?.render(<McpModeSection />);
    });
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
  }

  it("renders the resolved config path, source and candidate layers", async () => {
    listRuntimeMcpsMock.mockResolvedValue({
      count: 1,
      mcps: [buildEntry()],
      config: {
        path: "E:/ws/.aicli/mcp.yaml",
        source: "project",
        exists: true,
        manager_loaded: true,
        candidates: [
          {
            path: "E:/ws/.aicli/mcp.yaml",
            source: "project",
            exists: true,
          },
          {
            path: "C:/Users/demo/.aicli/mcp.yaml",
            source: "user",
            exists: false,
          },
        ],
      },
      summary: { total: 1, enabled: 1, disabled: 0, connected: 1, tools: 2 },
    });

    await renderSection();

    const card = container.querySelector('[data-testid="mcp-config-diagnostics"]');
    expect(card).not.toBeNull();
    expect(
      card?.querySelector('[data-testid="mcp-config-path"]')?.textContent,
    ).toContain("E:/ws/.aicli/mcp.yaml");
    expect(card?.textContent).toContain("配置来源");
    expect(card?.textContent).toContain("项目");
    expect(card?.textContent).toContain("存在");
    expect(card?.textContent).toContain("不存在");
    expect(
      card?.querySelector('[data-testid="mcp-config-summary"]')?.textContent,
    ).toContain("共 1 · 启用 1 · 连接 1 · 工具 2");
  });

  it("omits the diagnostics card when the backend does not report config", async () => {
    listRuntimeMcpsMock.mockResolvedValue({ count: 1, mcps: [buildEntry()] });

    await renderSection();

    expect(
      container.querySelector('[data-testid="mcp-config-diagnostics"]'),
    ).toBeNull();
    expect(container.textContent).toContain("docs");
  });
});
