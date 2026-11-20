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

import { SessionMcpSurface } from "./session-mcp-surface";
import {
  buildEntry,
  buildScopedPayload,
  SESSION_ID,
} from "./session-mcp-surface-test-helpers";

const {
  listRuntimeMcpsMock,
  listRuntimeSessionMcpsMock,
  setRuntimeSessionMcpEnabledMock,
  createRuntimeSessionMcpMock,
  deleteRuntimeSessionMcpMock,
  listRuntimeSessionMcpToolsMock,
  listRuntimeWorkspaceMcpsMock,
  createRuntimeWorkspaceMcpMock,
  deleteRuntimeWorkspaceMcpMock,
  setRuntimeWorkspaceMcpEnabledMock,
} = vi.hoisted(() => ({
  listRuntimeMcpsMock: vi.fn(),
  listRuntimeSessionMcpsMock: vi.fn(),
  setRuntimeSessionMcpEnabledMock: vi.fn(),
  createRuntimeSessionMcpMock: vi.fn(),
  deleteRuntimeSessionMcpMock: vi.fn(),
  listRuntimeSessionMcpToolsMock: vi.fn(),
  listRuntimeWorkspaceMcpsMock: vi.fn(),
  createRuntimeWorkspaceMcpMock: vi.fn(),
  deleteRuntimeWorkspaceMcpMock: vi.fn(),
  setRuntimeWorkspaceMcpEnabledMock: vi.fn(),
}));

const WORKSPACE_PATH = "E:/workspace";

function buildWorkspacePayload(entries: ReturnType<typeof buildEntry>[] = []) {
  const configPath = `${WORKSPACE_PATH}/.aicli/mcp.yaml`;
  return {
    scope: "workspace" as const,
    workspace_path: WORKSPACE_PATH,
    config_path: configPath,
    workspace_fallback: true,
    read: { path: configPath, source: "project", exists: false },
    write: { path: configPath, source: "project", exists: false },
    count: entries.length,
    mcps: entries.map((entry) => ({ ...entry, source: "workspace" as const })),
  };
}

vi.mock("@/api/runtime/mcp", () => ({
  listRuntimeMcps: listRuntimeMcpsMock,
  listRuntimeSessionMcps: listRuntimeSessionMcpsMock,
  setRuntimeSessionMcpEnabled: setRuntimeSessionMcpEnabledMock,
  createRuntimeSessionMcp: createRuntimeSessionMcpMock,
  deleteRuntimeSessionMcp: deleteRuntimeSessionMcpMock,
  listRuntimeSessionMcpTools: listRuntimeSessionMcpToolsMock,
  listRuntimeWorkspaceMcps: listRuntimeWorkspaceMcpsMock,
  createRuntimeWorkspaceMcp: createRuntimeWorkspaceMcpMock,
  deleteRuntimeWorkspaceMcp: deleteRuntimeWorkspaceMcpMock,
  setRuntimeWorkspaceMcpEnabled: setRuntimeWorkspaceMcpEnabledMock,
}));

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

describe("SessionMcpSurface", () => {
  let container: HTMLDivElement;
  let root: Root | null;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    // jsdom 未实现 scrollIntoView；MCP 表单使用自定义 Select，展开时会调用它。
    if (!Element.prototype.scrollIntoView) {
      Element.prototype.scrollIntoView = () => {};
    }
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    listRuntimeMcpsMock.mockReset();
    listRuntimeSessionMcpsMock.mockReset();
    setRuntimeSessionMcpEnabledMock.mockReset();
    createRuntimeSessionMcpMock.mockReset();
    deleteRuntimeSessionMcpMock.mockReset();
    listRuntimeSessionMcpToolsMock.mockReset();
    listRuntimeWorkspaceMcpsMock.mockReset();
    createRuntimeWorkspaceMcpMock.mockReset();
    deleteRuntimeWorkspaceMcpMock.mockReset();
    setRuntimeWorkspaceMcpEnabledMock.mockReset();
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
    workspacePath?: string;
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

  function fillInput(input: HTMLInputElement | null, value: string) {
    if (!input) {
      throw new Error("expected form input");
    }
    const setter = Object.getOwnPropertyDescriptor(
      HTMLInputElement.prototype,
      "value",
    )?.set;
    setter?.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  }

  function selectValue(select: HTMLButtonElement | null, value: string) {
    if (!select) {
      throw new Error("expected form select");
    }
    act(() => {
      select.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    const option = Array.from(
      document.querySelectorAll<HTMLElement>('[role="listbox"] [role="option"]'),
    ).find((candidate) => candidate.textContent?.includes(value));
    if (!option) {
      throw new Error(`expected form option ${value}`);
    }
    act(() => {
      option.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
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
    // 配置停用：提供「本会话启用」（内存临时连接，不写配置文件）。
    expect(buttons[1]?.disabled).toBe(false);
    expect(buttons[1]?.textContent).toContain("本会话启用");

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

  it("opens the session tool list dialog from a row", async () => {
    listRuntimeSessionMcpsMock.mockResolvedValue(
      buildScopedPayload({ entries: [buildEntry("chrome-mcp")] }),
    );
    listRuntimeSessionMcpToolsMock.mockResolvedValue({
      session_id: SESSION_ID,
      name: "chrome-mcp",
      scope: "workspace",
      count: 1,
      tools: [
        {
          name: "take_snapshot",
          description: "snapshot",
          enabled: true,
          configured_enabled: true,
          healthy: true,
        },
      ],
    });

    await renderSurface();
    await act(async () => {
      container
        .querySelector<HTMLButtonElement>('[data-testid="session-mcp-tools"]')
        ?.click();
    });
    await flush();

    expect(listRuntimeSessionMcpToolsMock).toHaveBeenCalledWith(
      SESSION_ID,
      "chrome-mcp",
    );
    const dialog = document.body.querySelector(
      '[data-testid="session-mcp-tools-dialog"]',
    );
    expect(dialog).not.toBeNull();
    expect(dialog?.textContent).toContain("take_snapshot");
  });

  it("temp-enables a configured-off server for this session only", async () => {
    listRuntimeSessionMcpsMock.mockResolvedValueOnce(
      buildScopedPayload({
        entries: [
          {
            ...buildEntry("off-mcp", { globalEnabled: false }),
            source: "workspace",
          },
        ],
      }),
    );
    setRuntimeSessionMcpEnabledMock.mockResolvedValueOnce({
      session_id: SESSION_ID,
      name: "off-mcp",
      enabled: true,
      scope: "session",
      session_state: "enabled",
      changed: true,
      message:
        "已在本会话临时启用 MCP off-mcp（内存连接，不写配置文件；会话结束或本会话停用后回收）",
    });
    // 刷新（第二次读取）：后端回显临时启用名单与条目标记。
    listRuntimeSessionMcpsMock.mockResolvedValueOnce(
      buildScopedPayload({
        entries: [
          {
            ...buildEntry("off-mcp", { globalEnabled: false }),
            source: "workspace",
            session_enabled: true,
          },
        ],
        enabled: ["off-mcp"],
      }),
    );

    await renderSurface();

    const button = container.querySelector<HTMLButtonElement>(
      '[data-testid="session-mcp-toggle"]',
    );
    expect(button?.textContent).toContain("本会话启用");
    expect(button?.disabled).toBe(false);

    await act(async () => {
      button?.click();
    });
    await flush();

    expect(setRuntimeSessionMcpEnabledMock).toHaveBeenLastCalledWith(
      SESSION_ID,
      "off-mcp",
      true,
    );
    expect(container.textContent).toContain("已在本会话临时启用");
    expect(
      container.querySelector('[data-testid="session-mcp-row"]')?.getAttribute(
        "data-session-state",
      ),
    ).toBe("temp_enabled");
    expect(
      container.querySelector('[data-testid="session-mcp-temp-badge"]')
        ?.textContent,
    ).toContain("本会话临时启用");
    const afterEnable = container.querySelector<HTMLButtonElement>(
      '[data-testid="session-mcp-toggle"]',
    );
    expect(afterEnable?.textContent).toContain("本会话停用");
  });

  it("manages a draft workspace without creating or calling session APIs", async () => {
    listRuntimeWorkspaceMcpsMock.mockResolvedValue(buildWorkspacePayload());
    createRuntimeWorkspaceMcpMock.mockResolvedValue({
      scope: "workspace",
      workspace_path: WORKSPACE_PATH,
      config_path: `${WORKSPACE_PATH}/.aicli/mcp.yaml`,
      config: { name: "draft-mcp", type: "stdio", command: "node" },
    });

    await renderSurface({ sessionId: "", workspacePath: WORKSPACE_PATH });

    expect(listRuntimeWorkspaceMcpsMock).toHaveBeenCalledWith(WORKSPACE_PATH);
    expect(listRuntimeSessionMcpsMock).not.toHaveBeenCalled();
    expect(listRuntimeMcpsMock).not.toHaveBeenCalled();
    expect(container.querySelector('[data-testid="session-mcp-toggle"]')).toBeNull();

    await act(async () => {
      container
        .querySelector<HTMLButtonElement>('[data-testid="session-mcp-add"]')
        ?.click();
    });
    const form = container.querySelector<HTMLFormElement>(
      '[data-testid="session-mcp-add-form"] form',
    );
    expect(form).not.toBeNull();
    selectValue(
      form?.querySelector<HTMLButtonElement>(
        'button[aria-haspopup="listbox"]',
      ) ?? null,
      "stdio",
    );
    const inputs = form?.querySelectorAll<HTMLInputElement>("input") ?? [];
    await act(async () => {
      fillInput(inputs[0] ?? null, "draft-mcp");
      // name, description, command（type is a select）
      fillInput(inputs[2] ?? null, "node");
    });

    await act(async () => {
      const save = Array.from(form?.querySelectorAll("button") ?? []).find(
        (button) => button.textContent?.includes("保存"),
      );
      save?.click();
    });
    await flush();

    expect(createRuntimeWorkspaceMcpMock).toHaveBeenCalledWith(
      WORKSPACE_PATH,
      expect.objectContaining({
        name: "draft-mcp",
        type: "stdio",
        command: "node",
      }),
    );
    expect(createRuntimeSessionMcpMock).not.toHaveBeenCalled();
    expect(listRuntimeSessionMcpsMock).not.toHaveBeenCalled();
  });

  it("uses workspace persistence controls for draft rows, but not session overrides", async () => {
    listRuntimeWorkspaceMcpsMock.mockResolvedValue(
      buildWorkspacePayload([buildEntry("draft-mcp")]),
    );
    setRuntimeWorkspaceMcpEnabledMock.mockResolvedValue({
      scope: "workspace",
      workspace_path: WORKSPACE_PATH,
      config_path: `${WORKSPACE_PATH}/.aicli/mcp.yaml`,
      name: "draft-mcp",
      enabled: false,
      changed: true,
    });
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);
    deleteRuntimeWorkspaceMcpMock.mockResolvedValue({
      scope: "workspace",
      workspace_path: WORKSPACE_PATH,
      config_path: `${WORKSPACE_PATH}/.aicli/mcp.yaml`,
      name: "draft-mcp",
      removed: true,
    });

    await renderSurface({ sessionId: "", workspacePath: WORKSPACE_PATH });
    await act(async () => {
      container
        .querySelector<HTMLButtonElement>('[data-testid="session-mcp-persist-toggle"]')
        ?.click();
    });
    await flush();

    expect(setRuntimeWorkspaceMcpEnabledMock).toHaveBeenCalledWith(
      WORKSPACE_PATH,
      "draft-mcp",
      false,
    );
    expect(setRuntimeSessionMcpEnabledMock).not.toHaveBeenCalled();

    await act(async () => {
      container
        .querySelector<HTMLButtonElement>('[data-testid="session-mcp-delete"]')
        ?.click();
    });
    await flush();

    expect(deleteRuntimeWorkspaceMcpMock).toHaveBeenCalledWith(
      WORKSPACE_PATH,
      "draft-mcp",
    );
    expect(deleteRuntimeSessionMcpMock).not.toHaveBeenCalled();
    confirmSpy.mockRestore();
  });

  it("does not expose workspace mutation buttons for global fallback entries", async () => {
    listRuntimeWorkspaceMcpsMock.mockResolvedValue(buildWorkspacePayload());
    listRuntimeSessionMcpsMock.mockResolvedValue(
      buildScopedPayload({
        fallback: true,
        entries: [
          { ...buildEntry("global-fallback"), source: "global" },
        ],
      }),
    );

    await renderSurface({ workspacePath: WORKSPACE_PATH });

    const row = container.querySelector('[data-testid="session-mcp-row"]');
    expect(row?.getAttribute("data-source")).toBe("global");
    expect(row?.querySelector('[data-testid="session-mcp-persist-toggle"]')).toBeNull();
    expect(row?.querySelector('[data-testid="session-mcp-delete"]')).toBeNull();
    expect(row?.querySelector('[data-testid="session-mcp-toggle"]')).not.toBeNull();
  });

  it("explains that workspace management is unavailable without session or workspace", async () => {
    await renderSurface({ sessionId: "" });

    expect(container.querySelector('[data-testid="session-mcp-unavailable"]')).not.toBeNull();
    expect(container.querySelector('[data-testid="session-mcp-add"]')).toBeNull();
    expect(listRuntimeWorkspaceMcpsMock).not.toHaveBeenCalled();
    expect(listRuntimeSessionMcpsMock).not.toHaveBeenCalled();
  });
});
