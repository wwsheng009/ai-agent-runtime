// @vitest-environment jsdom
//
// 「会话 MCP 工具列表」只读弹窗单测：
//   * 正常负载：列表 + scope 徽标 + 输入参数折叠；
//   * 空清单（未启用/未连接）→ 空态而不是报错；
//   * 契约异常 → 错误态 + 重试恢复；
//   * 关闭按钮回调。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { SessionMcpToolsDialog } from "./session-mcp-tools-dialog";

const { listRuntimeSessionMcpToolsMock } = vi.hoisted(() => ({
  listRuntimeSessionMcpToolsMock: vi.fn(),
}));

vi.mock("@/api/runtime/mcp", () => ({
  listRuntimeSessionMcpTools: listRuntimeSessionMcpToolsMock,
}));

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

const SESSION_ID = "session-tools-1";
const MCP_NAME = "chrome-devtools";

const TOOL = {
  name: "take_snapshot",
  description: "Capture the DOM snapshot",
  enabled: true,
  configured_enabled: true,
  healthy: true,
  inputSchema: { type: "object", properties: { verbose: { type: "boolean" } } },
};

describe("SessionMcpToolsDialog", () => {
  let container: HTMLDivElement;
  let root: Root | null;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    listRuntimeSessionMcpToolsMock.mockReset();
  });

  afterEach(() => {
    if (root) {
      act(() => root?.unmount());
    }
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  async function flush() {
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
  }

  async function renderDialog(onClose: () => void = () => {}) {
    await act(async () => {
      root?.render(
        <SessionMcpToolsDialog
          name={MCP_NAME}
          onClose={onClose}
          sessionId={SESSION_ID}
        />,
      );
    });
    await flush();
  }

  it("renders the tool list with scope badge and schema toggle", async () => {
    listRuntimeSessionMcpToolsMock.mockResolvedValue({
      session_id: SESSION_ID,
      name: MCP_NAME,
      scope: "workspace",
      count: 1,
      tools: [TOOL],
    });

    await renderDialog();

    expect(listRuntimeSessionMcpToolsMock).toHaveBeenCalledWith(
      SESSION_ID,
      MCP_NAME,
    );
    const dialog = document.body.querySelector(
      '[data-testid="session-mcp-tools-dialog"]',
    );
    expect(dialog).not.toBeNull();
    expect(dialog?.textContent).toContain("take_snapshot");
    expect(
      document.body.querySelector('[data-testid="session-mcp-tools-scope"]')
        ?.textContent,
    ).toContain("工作区配置");
    expect(
      document.body.querySelector('[data-testid="session-mcp-tool-take_snapshot"]')
        ?.textContent,
    ).toContain("Capture the DOM snapshot");
    expect(dialog?.textContent).toContain("输入参数");
    expect(dialog?.textContent).toContain("1 个工具");
  });

  it("renders the empty state for disabled or disconnected servers", async () => {
    listRuntimeSessionMcpToolsMock.mockResolvedValue({
      session_id: SESSION_ID,
      name: MCP_NAME,
      scope: "workspace",
      count: 0,
      tools: [],
    });

    await renderDialog();

    expect(
      document.body.querySelector('[data-testid="session-mcp-tools-empty"]')
        ?.textContent,
    ).toContain("未启用或未连接");
  });

  it("shows load failures with a working retry", async () => {
    listRuntimeSessionMcpToolsMock.mockRejectedValueOnce(
      new Error("tools unavailable"),
    );

    await renderDialog();

    expect(document.body.textContent).toContain("tools unavailable");

    listRuntimeSessionMcpToolsMock.mockResolvedValueOnce({
      session_id: SESSION_ID,
      name: MCP_NAME,
      scope: "global",
      count: 1,
      tools: [TOOL],
    });
    await act(async () => {
      document.body
        .querySelector<HTMLButtonElement>('[data-testid="session-mcp-tools-retry"]')
        ?.click();
    });
    await flush();

    expect(
      document.body.querySelector('[data-testid="session-mcp-tools-list"]'),
    ).not.toBeNull();
  });

  it("closes through the close button", async () => {
    listRuntimeSessionMcpToolsMock.mockResolvedValue({
      session_id: SESSION_ID,
      name: MCP_NAME,
      scope: "global",
      count: 0,
      tools: [],
    });
    const onClose = vi.fn();

    await renderDialog(onClose);
    await act(async () => {
      document.body
        .querySelector<HTMLButtonElement>('[data-testid="session-mcp-tools-close"]')
        ?.click();
    });

    expect(onClose).toHaveBeenCalledTimes(1);
  });
});
