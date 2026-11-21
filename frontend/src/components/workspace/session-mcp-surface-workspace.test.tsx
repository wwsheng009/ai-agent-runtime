// @vitest-environment jsdom

import { act } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  buildEntry,
  buildScopedPayload,
} from "./session-mcp-surface-test-helpers";
import {
  buildWorkspacePayload,
  cleanupSessionMcpSurfaceHarness,
  createRuntimeSessionMcpMock,
  createRuntimeWorkspaceMcpMock,
  deleteRuntimeSessionMcpMock,
  deleteRuntimeWorkspaceMcpMock,
  fillSessionMcpInput as fillInput,
  flushSessionMcp as flush,
  listRuntimeMcpsMock,
  listRuntimeSessionMcpsMock,
  listRuntimeWorkspaceMcpsMock,
  renderSessionMcpSurface as renderSurface,
  selectSessionMcpValue as selectValue,
  sessionMcpContainer,
  setRuntimeSessionMcpEnabledMock,
  setRuntimeWorkspaceMcpEnabledMock,
  setupSessionMcpSurfaceHarness,
  WORKSPACE_PATH,
} from "./session-mcp-surface-test-harness";

describe("SessionMcpSurface workspace scope", () => {
  let container: HTMLDivElement;

  beforeEach(() => {
    setupSessionMcpSurfaceHarness();
    container = sessionMcpContainer();
  });

  afterEach(() => {
    cleanupSessionMcpSurfaceHarness();
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
        .querySelector<HTMLButtonElement>(
          '[data-testid="session-mcp-persist-toggle"]',
        )
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
        entries: [{ ...buildEntry("global-fallback"), source: "global" }],
      }),
    );

    await renderSurface({ workspacePath: WORKSPACE_PATH });

    const row = container.querySelector('[data-testid="session-mcp-row"]');
    expect(row?.getAttribute("data-source")).toBe("global");
    expect(
      row?.querySelector('[data-testid="session-mcp-persist-toggle"]'),
    ).toBeNull();
    expect(row?.querySelector('[data-testid="session-mcp-delete"]')).toBeNull();
    expect(
      row?.querySelector('[data-testid="session-mcp-toggle"]'),
    ).not.toBeNull();
  });

  it("explains that workspace management is unavailable without session or workspace", async () => {
    await renderSurface({ sessionId: "" });

    expect(
      container.querySelector('[data-testid="session-mcp-unavailable"]'),
    ).not.toBeNull();
    expect(container.querySelector('[data-testid="session-mcp-add"]')).toBeNull();
    expect(listRuntimeWorkspaceMcpsMock).not.toHaveBeenCalled();
    expect(listRuntimeSessionMcpsMock).not.toHaveBeenCalled();
  });
});
