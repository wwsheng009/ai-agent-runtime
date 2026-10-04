// @vitest-environment jsdom
//
// 「会话 MCP」面单测：
//   * 行内徽标（全局启用/停用、本会话已停用）与按钮语义；
//   * 停用/恢复调用 setRuntimeSessionMcpEnabled 且只针对当前会话；
//   * 全局停用的 server 不提供会话级启用（按钮禁用 + 解释）；
//   * 读取失败 → 错误卡 + 重试；空配置 → 空态；
//   * `mcp.*` 运行时事件触发刷新，非 MCP 事件不触发。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { RuntimeMcpEntry } from "@/types/runtime";

import { SessionMcpSurface } from "./session-mcp-surface";

const {
  listRuntimeMcpsMock,
  listRuntimeSessionMcpsMock,
  setRuntimeSessionMcpEnabledMock,
  createRuntimeSessionMcpMock,
  deleteRuntimeSessionMcpMock,
} = vi.hoisted(() => ({
  listRuntimeMcpsMock: vi.fn(),
  listRuntimeSessionMcpsMock: vi.fn(),
  setRuntimeSessionMcpEnabledMock: vi.fn(),
  createRuntimeSessionMcpMock: vi.fn(),
  deleteRuntimeSessionMcpMock: vi.fn(),
}));

vi.mock("@/api/runtime/mcp", () => ({
  listRuntimeMcps: listRuntimeMcpsMock,
  listRuntimeSessionMcps: listRuntimeSessionMcpsMock,
  setRuntimeSessionMcpEnabled: setRuntimeSessionMcpEnabledMock,
  createRuntimeSessionMcp: createRuntimeSessionMcpMock,
  deleteRuntimeSessionMcp: deleteRuntimeSessionMcpMock,
}));

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

const SESSION_ID = "session-mcp-1";

function buildEntry(
  name: string,
  options: { globalEnabled?: boolean; connected?: boolean; toolCount?: number } = {},
): RuntimeMcpEntry {
  const globalEnabled = options.globalEnabled ?? true;
  return {
    config: {
      name,
      type: "stdio",
      command: "npx",
      args: [],
      enabled: globalEnabled,
      disabled: !globalEnabled,
    },
    status: {
      name,
      type: "stdio",
      enabled: globalEnabled,
      connected: options.connected ?? globalEnabled,
      toolCount: options.toolCount ?? 3,
    },
  };
}

describe("SessionMcpSurface", () => {
  let container: HTMLDivElement;
  let root: Root | null;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    listRuntimeMcpsMock.mockReset();
    listRuntimeSessionMcpsMock.mockReset();
    setRuntimeSessionMcpEnabledMock.mockReset();
    createRuntimeSessionMcpMock.mockReset();
    deleteRuntimeSessionMcpMock.mockReset();
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

  async function renderSurface(props: {
    sessionId?: string;
    lastRuntimeEventType?: string;
    runtimeEventCount?: number;
  } = {}) {
    await act(async () => {
      root?.render(<SessionMcpSurface sessionId={SESSION_ID} {...props} />);
    });
    await flush();
  }

  function toggleButtons(): HTMLButtonElement[] {
    return Array.from(
      container.querySelectorAll<HTMLButtonElement>(
        '[data-testid="session-mcp-toggle"]',
      ),
    );
  }

  it("renders rows, disables globally-off servers, and round-trips the session toggle", async () => {
    listRuntimeMcpsMock.mockResolvedValue({
      count: 2,
      mcps: [buildEntry("chrome-mcp"), buildEntry("off-mcp", { globalEnabled: false })],
    });
    listRuntimeSessionMcpsMock.mockResolvedValue({
      session_id: SESSION_ID,
      disabled: [],
      count: 0,
    });
    setRuntimeSessionMcpEnabledMock
      .mockResolvedValueOnce({
        session_id: SESSION_ID,
        name: "chrome-mcp",
        enabled: false,
        scope: "session",
        session_state: "disabled",
        changed: true,
        message: "已在当前会话停用「chrome-mcp」（全局连接与其他会话不受影响）",
      })
      .mockResolvedValueOnce({
        session_id: SESSION_ID,
        name: "chrome-mcp",
        enabled: true,
        scope: "session",
        session_state: "",
        changed: true,
        message: "已恢复 MCP 「chrome-mcp」的会话级启用",
      });

    await renderSurface();

    expect(listRuntimeSessionMcpsMock).toHaveBeenCalledWith(SESSION_ID);
    const rows = container.querySelectorAll('[data-testid="session-mcp-row"]');
    expect(rows).toHaveLength(2);
    expect(rows[0]?.getAttribute("data-global-enabled")).toBe("true");
    expect(rows[1]?.getAttribute("data-global-enabled")).toBe("false");

    const buttons = toggleButtons();
    expect(buttons[0]?.textContent).toContain("本会话停用");
    expect(buttons[0]?.disabled).toBe(false);
    // 全局停用：不提供会话级启用入口，避免必然失败的调用。
    expect(buttons[1]?.disabled).toBe(true);

    await act(async () => {
      buttons[0]?.click();
    });
    await flush();

    expect(setRuntimeSessionMcpEnabledMock).toHaveBeenLastCalledWith(
      SESSION_ID,
      "chrome-mcp",
      false,
    );
    expect(rows[0]?.getAttribute("data-session-state")).toBe("disabled");
    expect(
      container.querySelector('[data-testid="session-mcp-session-badge"]')
        ?.textContent,
    ).toContain("本会话已停用");
    expect(container.textContent).toContain("已在当前会话停用");

    const restoreButton = toggleButtons()[0];
    expect(restoreButton?.textContent).toContain("恢复");

    await act(async () => {
      restoreButton?.click();
    });
    await flush();

    expect(setRuntimeSessionMcpEnabledMock).toHaveBeenLastCalledWith(
      SESSION_ID,
      "chrome-mcp",
      true,
    );
    expect(rows[0]?.getAttribute("data-session-state")).toBe("default");
    expect(
      container.querySelector('[data-testid="session-mcp-session-badge"]'),
    ).toBeNull();
  });

  it("surfaces session-toggle failures (e.g. 409) as an inline alert", async () => {
    listRuntimeMcpsMock.mockResolvedValue({
      count: 1,
      mcps: [buildEntry("chrome-mcp")],
    });
    listRuntimeSessionMcpsMock.mockResolvedValue({
      session_id: SESSION_ID,
      disabled: [],
      count: 0,
    });
    setRuntimeSessionMcpEnabledMock.mockRejectedValue(
      new Error("runtime-server 暂不支持会话级临时连接"),
    );

    await renderSurface();
    await act(async () => {
      toggleButtons()[0]?.click();
    });
    await flush();

    const alert = container.querySelector(
      '[data-testid="session-mcp-toggle-error"]',
    );
    expect(alert?.getAttribute("role")).toBe("alert");
    expect(alert?.textContent).toContain("暂不支持会话级临时连接");
  });

  it("shows load failure with a working retry", async () => {
    listRuntimeMcpsMock.mockRejectedValueOnce(new Error("mcp unavailable"));
    listRuntimeSessionMcpsMock.mockResolvedValue({
      session_id: SESSION_ID,
      disabled: [],
      count: 0,
    });

    await renderSurface();

    const errorCard = container.querySelector('[data-testid="session-mcp-error"]');
    expect(errorCard?.textContent).toContain("MCP 读取失败");
    expect(errorCard?.textContent).toContain("mcp unavailable");

    listRuntimeMcpsMock.mockResolvedValueOnce({
      count: 1,
      mcps: [buildEntry("chrome-mcp")],
    });
    await act(async () => {
      const retry = Array.from(container.querySelectorAll("button")).find(
        (button) => button.textContent?.includes("重试"),
      );
      retry?.click();
    });
    await flush();

    expect(container.querySelector('[data-testid="session-mcp-error"]')).toBeNull();
    expect(
      container.querySelectorAll('[data-testid="session-mcp-row"]'),
    ).toHaveLength(1);
  });

  it("renders the empty state when no MCP is configured", async () => {
    listRuntimeMcpsMock.mockResolvedValue({ count: 0, mcps: [] });
    listRuntimeSessionMcpsMock.mockResolvedValue({
      session_id: SESSION_ID,
      disabled: [],
      count: 0,
    });

    await renderSurface();

    expect(
      container.querySelector('[data-testid="session-mcp-empty"]')?.textContent,
    ).toContain("尚未配置 MCP 服务");
  });

  it("refreshes on mcp.* runtime events only", async () => {
    listRuntimeMcpsMock.mockResolvedValue({ count: 0, mcps: [] });
    listRuntimeSessionMcpsMock.mockResolvedValue({
      session_id: SESSION_ID,
      disabled: [],
      count: 0,
    });

    await renderSurface();
    expect(listRuntimeMcpsMock).toHaveBeenCalledTimes(1);

    // 非 mcp 事件：不刷新。
    await renderSurface({ runtimeEventCount: 1, lastRuntimeEventType: "turn.started" });
    expect(listRuntimeMcpsMock).toHaveBeenCalledTimes(1);

    // mcp 事件：刷新。
    await renderSurface({
      runtimeEventCount: 2,
      lastRuntimeEventType: "mcp.tools.loaded",
    });
    expect(listRuntimeMcpsMock).toHaveBeenCalledTimes(2);
  });

  function buildScopedPayload(options: {
    entries?: Array<
      ReturnType<typeof buildEntry> & { source?: "workspace" | "global" }
    >;
    fallback?: boolean;
  } = {}) {
    const entries = options.entries ?? [];
    const workspaceFile = "E:/ws/.aicli/mcp.yaml";
    return {
      session_id: SESSION_ID,
      disabled: [] as string[],
      count: 0,
      scope: {
        workspace: "E:/ws",
        workspace_scoped: true,
        workspace_fallback: options.fallback ?? false,
        read: { path: workspaceFile, source: "project", exists: true },
        write: { path: workspaceFile, source: "project", exists: true },
      },
      mcps: entries,
      summary: {
        total: entries.length,
        enabled: entries.length,
        disabled: 0,
        connected: entries.length,
        tools: entries.length * 3,
      },
    };
  }

  it("renders the workspace scope card and workspace-sourced rows", async () => {
    listRuntimeSessionMcpsMock.mockResolvedValue(
      buildScopedPayload({
        entries: [
          {
            ...buildEntry("ws-mcp", { globalEnabled: false }),
            source: "workspace",
          },
        ],
      }),
    );

    await renderSurface();

    const scopeCard = container.querySelector('[data-testid="session-mcp-scope"]');
    expect(scopeCard).not.toBeNull();
    expect(scopeCard?.getAttribute("data-workspace-scoped")).toBe("true");
    expect(
      scopeCard?.querySelector('[data-testid="session-mcp-scope-badge"]')
        ?.textContent,
    ).toContain("工作区配置");
    expect(
      scopeCard?.querySelector('[data-testid="session-mcp-scope-path"]')
        ?.textContent,
    ).toContain("E:/ws/.aicli/mcp.yaml");
    // 条目来自 scope.mcps：不再请求全局列表。
    expect(listRuntimeMcpsMock).not.toHaveBeenCalled();
    expect(
      container.querySelector('[data-testid="session-mcp-source-badge"]')
        ?.textContent,
    ).toContain("工作区");
    expect(
      container.querySelector('[data-testid="session-mcp-persist-toggle"]'),
    ).not.toBeNull();
  });

  it("persists enable/disable with scope=workspace and refreshes", async () => {
    listRuntimeSessionMcpsMock.mockResolvedValue(
      buildScopedPayload({ entries: [buildEntry("chrome-mcp")] }),
    );
    setRuntimeSessionMcpEnabledMock.mockResolvedValue({
      session_id: SESSION_ID,
      name: "chrome-mcp",
      enabled: false,
      scope: "workspace",
      changed: true,
      message: "已停用 MCP chrome-mcp（写入会话配置）",
    });

    await renderSurface();
    await act(async () => {
      container
        .querySelector<HTMLButtonElement>(
          '[data-testid="session-mcp-persist-toggle"]',
        )
        ?.click();
    });
    await flush();

    expect(setRuntimeSessionMcpEnabledMock).toHaveBeenLastCalledWith(
      SESSION_ID,
      "chrome-mcp",
      false,
      "workspace",
    );
    expect(listRuntimeSessionMcpsMock).toHaveBeenCalledTimes(2);
    expect(container.textContent).toContain("已停用 MCP chrome-mcp");
  });

  it("deletes a workspace server after confirmation", async () => {
    listRuntimeSessionMcpsMock.mockResolvedValue(
      buildScopedPayload({ entries: [buildEntry("chrome-mcp")] }),
    );
    deleteRuntimeSessionMcpMock.mockResolvedValue({
      name: "chrome-mcp",
      removed: true,
    });
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);

    await renderSurface();
    await act(async () => {
      container
        .querySelector<HTMLButtonElement>('[data-testid="session-mcp-delete"]')
        ?.click();
    });
    await flush();

    expect(confirmSpy).toHaveBeenCalled();
    expect(deleteRuntimeSessionMcpMock).toHaveBeenCalledWith(
      SESSION_ID,
      "chrome-mcp",
    );
    confirmSpy.mockRestore();
  });

  it("opens the add form and blocks empty submissions with validation", async () => {
    listRuntimeSessionMcpsMock.mockResolvedValue(buildScopedPayload());

    await renderSurface();
    await act(async () => {
      container
        .querySelector<HTMLButtonElement>('[data-testid="session-mcp-add"]')
        ?.click();
    });
    await flush();

    const form = container.querySelector('[data-testid="session-mcp-add-form"]');
    expect(form).not.toBeNull();

    await act(async () => {
      const submit = Array.from(form?.querySelectorAll("button") ?? []).find(
        (button) => button.textContent?.includes("保存"),
      );
      submit?.click();
    });
    await flush();

    expect(createRuntimeSessionMcpMock).not.toHaveBeenCalled();
    expect(container.textContent).toContain("名称不能为空");
  });
});
