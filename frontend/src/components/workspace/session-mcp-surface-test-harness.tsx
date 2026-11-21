import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { vi } from "vitest";

import { SessionMcpSurface } from "./session-mcp-surface";
import {
  buildEntry,
  SESSION_ID,
} from "./session-mcp-surface-test-helpers";

const mocks = vi.hoisted(() => ({
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

export const {
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
} = mocks;

vi.mock("@/api/runtime/mcp", () => ({
  listRuntimeMcps: mocks.listRuntimeMcpsMock,
  listRuntimeSessionMcps: mocks.listRuntimeSessionMcpsMock,
  setRuntimeSessionMcpEnabled: mocks.setRuntimeSessionMcpEnabledMock,
  createRuntimeSessionMcp: mocks.createRuntimeSessionMcpMock,
  deleteRuntimeSessionMcp: mocks.deleteRuntimeSessionMcpMock,
  listRuntimeSessionMcpTools: mocks.listRuntimeSessionMcpToolsMock,
  listRuntimeWorkspaceMcps: mocks.listRuntimeWorkspaceMcpsMock,
  createRuntimeWorkspaceMcp: mocks.createRuntimeWorkspaceMcpMock,
  deleteRuntimeWorkspaceMcp: mocks.deleteRuntimeWorkspaceMcpMock,
  setRuntimeWorkspaceMcpEnabled: mocks.setRuntimeWorkspaceMcpEnabledMock,
}));

export const WORKSPACE_PATH = "E:/workspace";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

let container: HTMLDivElement;
let root: Root | null;

export function setupSessionMcpSurfaceHarness() {
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  // jsdom 未实现 scrollIntoView；MCP 表单使用自定义 Select，展开时会调用它。
  if (!Element.prototype.scrollIntoView) {
    Element.prototype.scrollIntoView = () => {};
  }
  (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
  resetSessionMcpMocks();
}

export function cleanupSessionMcpSurfaceHarness() {
  if (root) {
    act(() => root?.unmount());
  }
  container.remove();
  document.body.innerHTML = "";
  delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  root = null;
}

function resetSessionMcpMocks() {
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
}

export function sessionMcpContainer() {
  return container;
}

export async function flushSessionMcp() {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
}

export async function renderSessionMcpSurface(props: {
  sessionId?: string;
  workspacePath?: string;
  lastRuntimeEventType?: string;
  runtimeEventCount?: number;
} = {}) {
  await act(async () => {
    root?.render(<SessionMcpSurface sessionId={SESSION_ID} {...props} />);
  });
  await flushSessionMcp();
}

export function sessionMcpToggleButtons(): HTMLButtonElement[] {
  return Array.from(
    container.querySelectorAll<HTMLButtonElement>(
      '[data-testid="session-mcp-toggle"]',
    ),
  );
}

export function fillSessionMcpInput(
  input: HTMLInputElement | null,
  value: string,
) {
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

export function selectSessionMcpValue(
  select: HTMLButtonElement | null,
  value: string,
) {
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

export function buildWorkspacePayload(
  entries: ReturnType<typeof buildEntry>[] = [],
) {
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
