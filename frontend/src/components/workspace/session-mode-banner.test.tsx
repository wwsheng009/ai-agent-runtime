// @vitest-environment jsdom

// §4.6 常驻模式标识（渲染层）：常显/隐藏条件、tone 档位、plan 上下文与未知模式回落。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { SessionModeBanner } from "./session-mode-banner";
import type { RuntimeSessionPlanMode } from "@/lib/runtime-api";

import type { ParkedTurnTaskCounts, ParkedTurnView } from "@/lib/parked-turn";
import type { ComponentProps } from "react";

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

describe("SessionModeBanner", () => {
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

  function renderBanner(props: ComponentProps<typeof SessionModeBanner>) {
    act(() => {
      // 同一用例内多次渲染：复用 root，避免 createRoot 重复挂载的告警。
      root = root ?? createRoot(container);
      root.render(<SessionModeBanner {...props} />);
    });
  }

  function banner() {
    return container.querySelector('[data-testid="session-mode-banner"]');
  }

  function parked(): ParkedTurnView["turn"] {
    return {
      sessionId: "session-1",
      turnId: "turn-1",
      batchId: "batch-1",
      obligationCount: 3,
      resumeQueueCount: 0,
      parkedAt: "2026-09-26T00:00:00Z",
    };
  }

  function counts(overrides: Partial<ParkedTurnTaskCounts> = {}): ParkedTurnTaskCounts {
    return { running: 0, completed: 0, failed: 0, ...overrides };
  }

  function parkedView(taskCounts: ParkedTurnTaskCounts | null = null): ParkedTurnView {
    return { turn: parked(), taskCounts };
  }

  function parkedText() {
    return container.querySelector('[data-testid="session-parked-turn"]')?.textContent ?? null;
  }

  it("没有会话或没有模式快照时不占位", () => {
    renderBanner({ plan: plan(), sessionId: undefined });
    expect(banner()).toBeNull();

    renderBanner({ plan: null, sessionId: "session-1" });
    expect(banner()).toBeNull();

    renderBanner({ plan: plan({ permission_mode: "  " }), sessionId: "session-1" });
    expect(banner()).toBeNull();
  });

  it("常显当前模式：默认档中性、plan 档强调、跳权档告警", () => {
    renderBanner({ plan: plan({ permission_mode: "accept_edits" }), sessionId: "session-1" });
    expect(banner()?.getAttribute("data-mode")).toBe("accept_edits");
    expect(banner()?.getAttribute("data-tone")).toBe("neutral");

    renderBanner({ plan: plan({ permission_mode: "plan" }), sessionId: "session-1" });
    expect(banner()?.getAttribute("data-tone")).toBe("plan");

    renderBanner({ plan: plan({ permission_mode: "bypass_permissions" }), sessionId: "session-1" });
    expect(banner()?.getAttribute("data-tone")).toBe("danger");
  });

  it("未知模式回落后端原文，不出现空徽标", () => {
    renderBanner({ plan: plan({ permission_mode: "future_mode" }), sessionId: "session-1" });

    // Badge 不转发额外 props（共用组件），这里按文案断言：未知值必须原样呈现。
    expect(banner()?.textContent).toContain("future_mode");
  });

  it("plan active：补状态徽标、计划路径与状态读法（模型已请求裁决优先）", () => {
    renderBanner({
      plan: plan({
        active: true,
        pending_exit_request: true,
        plan_content_available: true,
        plan_path: "docs/plan.md",
        permission_mode: "plan",
      }),
      planStatusLabel: "待裁决",
      sessionId: "session-1",
    });

    expect(container.textContent).toContain("待裁决");
    expect(container.textContent).toContain("docs/plan.md");
    expect(container.querySelector('[data-testid="session-mode-hint"]')?.textContent).toContain(
      "模型已请求裁决",
    );
  });

  it("plan active 但正文未就绪：提示等待产出，不显示路径", () => {
    renderBanner({
      plan: plan({ active: true, plan_content_available: false, permission_mode: "plan" }),
      planStatusLabel: "进行中",
      sessionId: "session-1",
    });

    expect(container.querySelector('[data-testid="session-mode-hint"]')?.textContent).toContain(
      "计划尚未写就",
    );
    expect(container.textContent).toContain("进行中");
  });

  it("plan 非 active：不显示计划上下文，只留模式标识", () => {
    renderBanner({
      plan: plan({ active: false, plan_path: "docs/plan.md", permission_mode: "default" }),
      planStatusLabel: "未启用",
      sessionId: "session-1",
    });

    expect(container.querySelector('[data-testid="session-mode-hint"]')).toBeNull();
    expect(container.textContent).not.toContain("docs/plan.md");
    expect(container.textContent).not.toContain("未启用");
  });

  it("托管挂起：无任务投影时显示等待的义务数", () => {
    renderBanner({ parkedTurn: parkedView(), plan: plan(), sessionId: "session-1" });

    expect(parkedText()).toContain("托管中：等待 3 个义务");
    expect(banner()?.getAttribute("data-parked")).toBe("true");
  });

  it("托管挂起：有任务投影时显示运行中 / 完成 / 异常计数", () => {
    renderBanner({
      parkedTurn: parkedView(counts({ running: 2, completed: 1, failed: 1 })),
      plan: plan(),
      sessionId: "session-1",
    });

    expect(parkedText()).toContain("托管中：2 个任务运行中（1 完成 / 1 异常）");
  });

  it("托管挂起：投影为空（目录未加载）时降级为义务数，不编造计数", () => {
    renderBanner({
      parkedTurn: parkedView(counts()),
      plan: plan(),
      sessionId: "session-1",
    });

    expect(parkedText()).toContain("托管中：等待 3 个义务");
  });

  it("托管挂起：模式快照缺失时仍单独显示（挂起表达不依赖 /plan）", () => {
    renderBanner({ parkedTurn: parkedView(), plan: null, sessionId: "session-1" });

    expect(banner()).not.toBeNull();
    expect(parkedText()).toContain("托管中");
    expect(container.querySelector('[data-testid="session-mode-hint"]')).toBeNull();
  });

  it("托管段随快照清空消失（turn.resumed 由归约层清成 null）", () => {
    renderBanner({ parkedTurn: parkedView(), plan: plan(), sessionId: "session-1" });
    expect(parkedText()).toContain("托管中");

    renderBanner({ parkedTurn: null, plan: plan(), sessionId: "session-1" });
    expect(container.querySelector('[data-testid="session-parked-turn"]')).toBeNull();
    expect(banner()).not.toBeNull();
  });

  it("无会话时不显示托管挂起段", () => {
    renderBanner({ parkedTurn: parkedView(), plan: plan(), sessionId: undefined });

    expect(banner()).toBeNull();
  });
});
