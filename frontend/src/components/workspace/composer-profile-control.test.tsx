// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { SessionProfileSwitchReport } from "@/api/runtime/profiles";
import type { RuntimeProfileListResponse } from "@/types/runtime";

import { ComposerProfileControl } from "./composer-profile-control";

// Composer profile 切换器：验证「当前绑定 → 候选渲染 → 切换 → 本地采纳 + 失败」
// 的接线。切换动作走宿主传入的异步处理器（与 `/profile` 命令同一执行核心）。
function catalogEntry(
  ref: string,
  label: string,
  overrides: Record<string, unknown> = {},
) {
  return {
    ref,
    name: label,
    description: "",
    layer: "user",
    path: `profiles/${ref}`,
    valid: true,
    error: "",
    isDefault: false,
    defaultAgent: "",
    writable: true,
    promptSuppressed: false,
    promptSuppressionReason: "",
    isBound: false,
    ...overrides,
  };
}

const CATALOG: RuntimeProfileListResponse = {
  count: 3,
  defaultProfile: "coder",
  defaultRoot: "profiles",
  sessionSwitch: true,
  workspacePath: "",
  workspaceTrusted: false,
  workspaceTrustFeatureEnabled: false,
  projectBinding: null,
  profiles: [
    catalogEntry("coder", "Coder", { isDefault: true }),
    catalogEntry("reviewer", "Reviewer"),
    catalogEntry("broken", "Broken", {
      layer: "project",
      valid: false,
      error: "yaml parse failed",
    }),
  ],
};

function switchReport(to: string): SessionProfileSwitchReport {
  return {
    from: "coder",
    to,
    changed: {
      toolsAdded: [],
      toolsRemoved: [],
      skillsAdded: [],
      skillsRemoved: [],
      mcpAdded: [],
      mcpRemoved: [],
      promptChanged: true,
      providerChanged: false,
      modelChanged: false,
      permissionModeChanged: false,
    },
    effectiveAt: "next_turn",
    cacheNotice: "",
    warnings: [],
    inFlightTurn: false,
    anchorCleared: true,
    toolSurfaceInvalidated: true,
    toolSurfaceScope: "none",
    actorEvicted: false,
    contextTokenCountReset: false,
  };
}

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

describe("ComposerProfileControl", () => {
  let container: HTMLDivElement;
  let root: Root | null;
  let onSwitch: ReturnType<typeof vi.fn> & ((ref: string) => Promise<SessionProfileSwitchReport>);

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = null;
    onSwitch = Object.assign(
      vi.fn(() => Promise.resolve(switchReport("reviewer"))),
    ) as typeof onSwitch;
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

  async function render(
    props: Partial<Parameters<typeof ComposerProfileControl>[0]> = {},
  ) {
    root = createRoot(container);
    await act(async () => {
      root?.render(
        <ComposerProfileControl
          catalog={CATALOG}
          onSwitch={onSwitch}
          profileName="Coder"
          profileRef="coder"
          sessionId="session-1"
          {...props}
        />,
      );
    });
    await flush();
  }

  function currentRef() {
    return container
      .querySelector("[data-composer-profile-ref]")
      ?.getAttribute("data-composer-profile-ref");
  }

  function trigger() {
    return container.querySelector<HTMLButtonElement>(
      'button[aria-label="Profile"]',
    );
  }

  function options() {
    return Array.from(
      document.querySelectorAll<HTMLElement>('[role="option"]'),
    ).map((option) => option.textContent);
  }

  async function pickOption(text: string) {
    act(() => {
      trigger()?.click();
    });
    const option = Array.from(
      document.querySelectorAll<HTMLElement>('[role="option"]'),
    ).find((element) => element.textContent?.includes(text));
    expect(option).toBeDefined();
    await act(async () => {
      option?.click();
    });
    await flush();
  }

  it("renders the current binding from session metadata", async () => {
    await render();

    expect(currentRef()).toBe("coder");
    expect(container.textContent).toContain("Profile");
  });

  it("does not render without a registered session", async () => {
    await render({ sessionId: undefined });

    expect(container.textContent).toBe("");
  });

  it("marks the default candidate and hides invalid ones from selection", async () => {
    await render();

    act(() => {
      trigger()?.click();
    });
    expect(options()).toEqual(["Coder（默认）", "Reviewer"]);
  });

  it("switches profile through the host handler and adopts report.to", async () => {
    await render();

    await pickOption("Reviewer");

    expect(onSwitch).toHaveBeenCalledWith("reviewer");
    expect(currentRef()).toBe("reviewer");
  });

  it("surfaces switch failures instead of faking a switch", async () => {
    onSwitch.mockRejectedValue(new Error("runtime rejected"));
    await render();

    await pickOption("Reviewer");

    expect(
      container.querySelector("[data-composer-profile-error]"),
    ).not.toBeNull();
    expect(currentRef()).toBe("coder");
  });

  it("matches a path-shaped profile_ref to the catalog entry by name", async () => {
    // 实测（runtime-server e2e）：root/layer 来源的 set_profile 把解析后的绝对路径
    // 写进 profile_ref，而目录候选 ref 是名字；此时按 profile_name 兜底匹配。
    await render({
      profileRef: "C:\\Users\\vince\\.aicli\\profiles\\reviewer",
      profileName: "reviewer",
    });

    expect(currentRef()).toBe("reviewer");
    act(() => {
      trigger()?.click();
    });
    expect(options()).not.toContain("C:\\Users\\vince\\.aicli\\profiles\\reviewer（不在目录中）");
  });

  it("shows an out-of-catalog binding honestly with an unselectable marker", async () => {
    await render({ profileRef: "ghost", profileName: "Ghost" });

    expect(currentRef()).toBe("ghost");
    act(() => {
      trigger()?.click();
    });
    expect(options()[0]).toContain("Ghost（不在目录中）");
  });

  it("renders the unbound placeholder for sessions without a profile", async () => {
    await render({ profileName: undefined, profileRef: "" });

    expect(currentRef()).toBe("");
    expect(container.textContent).toContain("默认");
  });

  it("stays disabled when the backend does not advertise session switching", async () => {
    await render({ catalog: { ...CATALOG, sessionSwitch: false } });

    expect(trigger()?.disabled).toBe(true);
    expect(
      container.querySelector("[data-composer-profile-unsupported]"),
    ).not.toBeNull();
    expect(onSwitch).not.toHaveBeenCalled();
  });

  it("stays disabled while the profile catalog is loading", async () => {
    await render({ catalog: null, catalogError: null });

    expect(trigger()?.disabled).toBe(true);
    expect(
      container.querySelector("[data-composer-profile-busy]"),
    ).toBeNull();
    expect(
      container.querySelector("[data-composer-profile-unsupported]"),
    ).toBeNull();
  });

  it("reports catalog load failure without faking candidates", async () => {
    await render({ catalog: null, catalogError: "network down" });

    expect(
      container.querySelector("[data-composer-profile-error]"),
    ).not.toBeNull();
    expect(onSwitch).not.toHaveBeenCalled();
  });

  it("drops the previous session override when the session changes", async () => {
    await render();

    await pickOption("Reviewer");
    expect(currentRef()).toBe("reviewer");

    // 模拟宿主切到另一个会话快照（profileRef 仍为 coder）：override 不跨会话。
    await act(async () => {
      root?.render(
        <ComposerProfileControl
          catalog={CATALOG}
          onSwitch={onSwitch}
          profileName="Coder"
          profileRef="coder"
          sessionId="session-2"
        />,
      );
    });
    await flush();

    expect(currentRef()).toBe("coder");
  });
});
