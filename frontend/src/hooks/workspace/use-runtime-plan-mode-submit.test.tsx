// @vitest-environment jsdom

// §4.4 自动修订回合（前端半程）：带意见的「请求修改」必须带 trigger_revision，
// 其余裁决不带；触发失败时把原因说明挂到 planError，成功交付则清空草稿。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { RuntimeSessionPlanMode } from "@/lib/runtime-api";

const apiMocks = vi.hoisted(() => ({
  getSessionPlanMode: vi.fn(),
  updateSessionPlanMode: vi.fn(),
}));

vi.mock("@/lib/runtime-api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/runtime-api")>();
  return {
    ...actual,
    getSessionPlanMode: apiMocks.getSessionPlanMode,
    updateSessionPlanMode: apiMocks.updateSessionPlanMode,
  };
});

import { useRuntimePlanMode } from "@/hooks/workspace/use-runtime-plan-mode";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

type HookSnapshot = ReturnType<typeof useRuntimePlanMode>;

function activePlan(overrides: Partial<RuntimeSessionPlanMode> = {}): RuntimeSessionPlanMode {
  return {
    session_id: "session-1",
    active: true,
    status: "active",
    permission_mode: "plan",
    plan_content: "# Plan",
    plan_content_available: true,
    ...overrides,
  };
}

function Harness({ onSnapshot }: { onSnapshot: (snapshot: HookSnapshot) => void }) {
  const snapshot = useRuntimePlanMode({ sessionId: "session-1" });
  onSnapshot(snapshot);
  return null;
}

describe("useRuntimePlanMode submitDecision", () => {
  let container: HTMLDivElement;
  let root: Root | null;

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = null;
    apiMocks.getSessionPlanMode.mockReset();
    apiMocks.updateSessionPlanMode.mockReset();
    apiMocks.getSessionPlanMode.mockResolvedValue(activePlan());
  });

  afterEach(() => {
    act(() => {
      root?.unmount();
    });
    root = null;
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  async function renderHook() {
    let latest: HookSnapshot | null = null;
    await act(async () => {
      root = createRoot(container);
      root.render(
        <Harness
          onSnapshot={(snapshot) => {
            latest = snapshot;
          }}
        />,
      );
    });
    return {
      get current(): HookSnapshot {
        if (!latest) {
          throw new Error("hook not rendered");
        }
        return latest;
      },
    };
  }

  it("带意见的「请求修改」带 trigger_revision，交付成功后清空草稿", async () => {
    apiMocks.updateSessionPlanMode.mockResolvedValue(
      activePlan({ notes: "add rollback risks", revision_triggered: true }),
    );
    const hook = await renderHook();

    act(() => {
      hook.current.onNotesDraftChange("  add rollback risks  ");
    });
    await act(async () => {
      await hook.current.submitDecision("request_changes");
    });

    expect(apiMocks.updateSessionPlanMode).toHaveBeenCalledWith("session-1", {
      action: "request_changes",
      notes: "add rollback risks",
      trigger_revision: true,
    });
    expect(hook.current.notesDraft).toBe("");
    expect(hook.current.planError).toBeNull();
  });

  it("触发失败时保留草稿并把原因挂到 planError（决策已落地）", async () => {
    apiMocks.updateSessionPlanMode.mockResolvedValue(
      activePlan({
        notes: "add rollback risks",
        revision_triggered: false,
        revision_error: "session is not attached to the live runtime",
      }),
    );
    const hook = await renderHook();

    act(() => {
      hook.current.onNotesDraftChange("add rollback risks");
    });
    await act(async () => {
      await hook.current.submitDecision("request_changes");
    });

    expect(hook.current.planError).toBe("session is not attached to the live runtime");
    expect(hook.current.notesDraft).toBe("add rollback risks");
  });

  it("空意见或其它裁决不带 trigger_revision", async () => {
    apiMocks.updateSessionPlanMode.mockResolvedValue(activePlan());
    const hook = await renderHook();

    await act(async () => {
      await hook.current.submitDecision("request_changes");
    });
    expect(apiMocks.updateSessionPlanMode).toHaveBeenLastCalledWith("session-1", {
      action: "request_changes",
      notes: undefined,
      trigger_revision: undefined,
    });

    act(() => {
      hook.current.onNotesDraftChange("looks good");
    });
    await act(async () => {
      await hook.current.submitDecision("approve");
    });
    expect(apiMocks.updateSessionPlanMode).toHaveBeenLastCalledWith("session-1", {
      action: "approve",
      notes: "looks good",
      trigger_revision: undefined,
    });
  });
});
