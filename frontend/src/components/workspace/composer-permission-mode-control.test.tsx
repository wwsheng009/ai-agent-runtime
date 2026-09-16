// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ComposerPermissionModeControl } from "./composer-permission-mode-control";

// P1-x：权限选择器只验证「后端清单 → 选项渲染 → 切换调用接口」的接线。
const apiMocks = vi.hoisted(() => ({
  getSessionPermissionMode: vi.fn(),
  updateSessionPermissionMode: vi.fn(),
  updateSessionPlanMode: vi.fn(),
}));

vi.mock("@/lib/runtime-api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/runtime-api")>();
  return {
    ...actual,
    getSessionPermissionMode: apiMocks.getSessionPermissionMode,
    updateSessionPermissionMode: apiMocks.updateSessionPermissionMode,
    updateSessionPlanMode: apiMocks.updateSessionPlanMode,
  };
});

const SUPPORTED_MODES = [
  { value: "default", label: "backend default" },
  { value: "accept_edits", label: "backend accept edits" },
  {
    value: "plan",
    label: "backend plan",
    requires_plan_entry: true,
  },
  {
    value: "bypass_permissions",
    label: "backend bypass",
    dangerous: true,
  },
];

function permissionModeResponse(
  mode: string,
  overrides: Record<string, unknown> = {},
) {
  return {
    session_id: "session-1",
    mode,
    supported_modes: SUPPORTED_MODES,
    ...overrides,
  };
}

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

describe("ComposerPermissionModeControl", () => {
  let container: HTMLDivElement;
  let root: Root | null;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = null;
    apiMocks.getSessionPermissionMode.mockResolvedValue(
      permissionModeResponse("default"),
    );
    apiMocks.updateSessionPermissionMode.mockResolvedValue(
      permissionModeResponse("accept_edits"),
    );
    apiMocks.updateSessionPlanMode.mockResolvedValue({
      session_id: "session-1",
      active: true,
      status: "active",
      permission_mode: "plan",
      plan_content: "",
      plan_content_available: false,
    });
    // jsdom 未实现 scrollIntoView，Select 打开菜单时会调用它。
    if (!Element.prototype.scrollIntoView) {
      Element.prototype.scrollIntoView = () => {};
    }
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
  });

  afterEach(() => {
    if (root) {
      act(() => {
        root?.unmount();
      });
    }
    container.remove();
    document.body.innerHTML = "";
    vi.clearAllMocks();
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  async function flush() {
    await act(async () => {
      await Promise.resolve();
    });
  }

  // 注意：不能用 `string | undefined = "session-1"` 的默认参数写法，
  // 显式传 undefined 会触发默认值，导致「无会话」用例实际渲染出会话控件。
  async function render(sessionId: string | null = "session-1") {
    root = createRoot(container);
    await act(async () => {
      root?.render(
        <ComposerPermissionModeControl sessionId={sessionId ?? undefined} />,
      );
    });
    await flush();
  }

  function currentMode() {
    return container
      .querySelector("[data-composer-permission-mode]")
      ?.getAttribute("data-composer-permission-mode");
  }

  it("renders the backend-supported modes and the current mode label", async () => {
    await render();

    expect(apiMocks.getSessionPermissionMode).toHaveBeenCalledWith("session-1");
    expect(currentMode()).toBe("default");
    expect(container.textContent).toContain("权限");
    expect(container.textContent).toContain("默认");
  });

  it("does not render without a session", async () => {
    await render(null);

    expect(container.textContent).toBe("");
    expect(apiMocks.getSessionPermissionMode).not.toHaveBeenCalled();
  });

  it("switches to a non-plan mode through the permission-mode endpoint", async () => {
    await render();

    const trigger = container.querySelector<HTMLButtonElement>(
      'button[aria-label="权限"]',
    );
    expect(trigger).not.toBeNull();

    act(() => {
      trigger?.click();
    });

    const option = Array.from(
      document.querySelectorAll<HTMLElement>('[role="option"]'),
    ).find((element) => element.textContent?.includes("自动接受编辑"));
    expect(option).toBeDefined();

    await act(async () => {
      option?.click();
    });
    await flush();

    expect(apiMocks.updateSessionPermissionMode).toHaveBeenCalledWith(
      "session-1",
      { mode: "accept_edits" },
    );
    expect(currentMode()).toBe("accept_edits");
  });

  it("enters plan mode through the plan endpoint and reloads the mode", async () => {
    await render();
    apiMocks.getSessionPermissionMode.mockResolvedValue(
      permissionModeResponse("plan", { plan_active: true }),
    );

    const trigger = container.querySelector<HTMLButtonElement>(
      'button[aria-label="权限"]',
    );
    act(() => {
      trigger?.click();
    });

    const option = Array.from(
      document.querySelectorAll<HTMLElement>('[role="option"]'),
    ).find((element) => element.textContent?.includes("计划模式"));
    expect(option).toBeDefined();

    await act(async () => {
      option?.click();
    });
    await flush();

    expect(apiMocks.updateSessionPlanMode).toHaveBeenCalledWith("session-1", {
      action: "enter",
    });
    expect(apiMocks.updateSessionPermissionMode).not.toHaveBeenCalled();
    expect(currentMode()).toBe("plan");
  });

  it("surfaces update failures instead of silently keeping the old mode", async () => {
    await render();
    apiMocks.updateSessionPermissionMode.mockRejectedValueOnce(
      new Error("backend rejected"),
    );

    const trigger = container.querySelector<HTMLButtonElement>(
      'button[aria-label="权限"]',
    );
    act(() => {
      trigger?.click();
    });
    const option = Array.from(
      document.querySelectorAll<HTMLElement>('[role="option"]'),
    ).find((element) => element.textContent?.includes("跳过权限校验"));
    await act(async () => {
      option?.click();
    });
    await flush();

    expect(
      container.querySelector("[data-composer-permission-error]"),
    ).not.toBeNull();
    expect(currentMode()).toBe("default");
  });
});
