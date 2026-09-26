// @vitest-environment jsdom

// 停靠列（§4.6）：模式标识与待交互卡片是同一条宽度轴上的整体——两组用例守住「常驻」与
// 「同框」两个契约（从 main-section 抽出后最容易回归的两点）。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { SessionInteractionDock } from "./session-interaction-dock";
import type { PendingInteraction } from "@/lib/pending-interaction";
import type { RuntimeSessionPlanMode } from "@/lib/runtime-api";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

function plan(overrides: Partial<RuntimeSessionPlanMode> = {}): RuntimeSessionPlanMode {
  return {
    session_id: "session-1",
    active: false,
    status: "inactive",
    permission_mode: "default",
    plan_content: "",
    plan_content_available: false,
    ...overrides,
  };
}

describe("SessionInteractionDock", () => {
  let container: HTMLDivElement;
  let root: Root | null;

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = null;
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

  function renderDock(props: {
    interaction: PendingInteraction | null;
    onPlanDecision?: (decision: string) => void;
    plan: RuntimeSessionPlanMode;
    planStatusLabel?: string;
    sessionId?: string;
  }) {
    act(() => {
      root = createRoot(container);
      root.render(<SessionInteractionDock {...props} />);
    });
  }

  it("无待交互时仅常驻模式标识（卡片不为空占位）", () => {
    renderDock({
      interaction: null,
      plan: plan({ active: true, permission_mode: "plan", plan_content_available: true }),
      planStatusLabel: "待评审",
      sessionId: "session-1",
    });

    expect(container.querySelector('[data-testid="session-mode-banner"]')).not.toBeNull();
    expect(container.querySelector('[data-testid="pending-interaction"]')).toBeNull();
    expect(container.textContent).toContain("待评审");
  });

  it("计划评审待裁决：模式标识与裁决条同框，裁决动作仍由卡片回抛", () => {
    const onPlanDecision = vi.fn();
    renderDock({
      interaction: {
        id: "plan-review:session-1",
        kind: "plan_review",
        status: "pending",
      } as PendingInteraction,
      onPlanDecision,
      plan: plan({ active: true, permission_mode: "plan", pending_exit_request: true }),
      planStatusLabel: "待裁决",
      sessionId: "session-1",
    });

    expect(container.querySelector('[data-testid="session-mode-banner"]')).not.toBeNull();
    const bar = container.querySelector('[data-testid="pending-interaction"]');
    expect(bar).not.toBeNull();

    const approve = Array.from(bar?.querySelectorAll("button") ?? []).find((button) =>
      button.textContent?.includes("批准"),
    );
    expect(approve).toBeInstanceOf(HTMLButtonElement);
    act(() => {
      approve?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(onPlanDecision).toHaveBeenCalledWith("approve");
  });
});
