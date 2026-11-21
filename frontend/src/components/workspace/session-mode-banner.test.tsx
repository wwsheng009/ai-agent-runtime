// @vitest-environment jsdom

// 停靠状态行（§4.6 plan 上下文 + §6.8 / gap 3b 托管挂起与迟到唤醒）：
// 权限模式不再在此渲染（composer 底部控件是唯一入口，顶部重复模式已移除）；
// 这里守住「不占位条件、plan 上下文、托管段、迟到唤醒提示」四组契约。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { SessionModeBanner } from "./session-mode-banner";
import type { RuntimeSessionPlanMode } from "@/lib/runtime-api";

import type { ParkedTurnTaskCounts, ParkedTurnView } from "@/lib/parked-turn";
import type { ResumedTurnNotice } from "@/lib/parked-turn";
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
    return { turn: parked(), taskCounts, resumedNotice: null };
  }

  function notice(overrides: Partial<ResumedTurnNotice> = {}): ResumedTurnNotice {
    return {
      sessionId: "session-1",
      turnId: "turn-1",
      trigger: "terminal",
      terminal: true,
      wakeReasons: [],
      resumedAt: "2026-09-26T00:10:00Z",
      ...overrides,
    };
  }

  function noticeOnlyView(resumedNotice: ResumedTurnNotice): ParkedTurnView {
    return { turn: null, taskCounts: null, resumedNotice };
  }

  function parkedText() {
    return container.querySelector('[data-testid="session-parked-turn"]')?.textContent ?? null;
  }

  function resumedText() {
    return container.querySelector('[data-testid="session-resumed-notice"]')?.textContent ?? null;
  }

  it("没有会话或既无 plan 上下文也无挂起态时不占位", () => {
    renderBanner({ plan: plan({ active: true }), sessionId: undefined });
    expect(banner()).toBeNull();

    renderBanner({ plan: null, sessionId: "session-1" });
    expect(banner()).toBeNull();
  });

  it("不再重复渲染权限模式：仅有模式快照时顶部不占位", () => {
    renderBanner({ plan: plan({ permission_mode: "accept_edits" }), sessionId: "session-1" });
    expect(banner()).toBeNull();

    renderBanner({ plan: plan({ permission_mode: "bypass_permissions" }), sessionId: "session-1" });
    expect(banner()).toBeNull();

    renderBanner({ plan: plan({ permission_mode: "  " }), sessionId: "session-1" });
    expect(banner()).toBeNull();
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
    // 模式名不再出现在顶部状态行（由 composer 底部控件承担）。
    expect(container.textContent).not.toContain("计划模式");
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

  it("plan 非 active：不显示 plan 上下文，顶部不占位", () => {
    renderBanner({
      plan: plan({ active: false, plan_path: "docs/plan.md", permission_mode: "default" }),
      planStatusLabel: "未启用",
      sessionId: "session-1",
    });

    expect(banner()).toBeNull();
    expect(container.querySelector('[data-testid="session-mode-hint"]')).toBeNull();
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
    // 无挂起 + 非 plan active：状态行整体不占位。
    expect(banner()).toBeNull();
  });

  it("无会话时不显示托管挂起段", () => {
    renderBanner({ parkedTurn: parkedView(), plan: plan(), sessionId: undefined });

    expect(banner()).toBeNull();
  });

  it("迟到唤醒：无挂起 / 无模式快照时也单独渲染一行「已由监督自动恢复」", () => {
    renderBanner({ parkedTurn: noticeOnlyView(notice()), plan: null, sessionId: "session-1" });

    expect(banner()).not.toBeNull();
    expect(banner()?.getAttribute("data-parked")).toBe("false");
    expect(banner()?.getAttribute("data-resumed")).toBe("true");
    expect(parkedText()).toBeNull();
    expect(resumedText()).toContain("已由监督自动恢复");
    expect(resumedText()).toContain("trigger=terminal");
  });

  it("迟到唤醒：trigger 缺失时走通用文案，不渲染空括号", () => {
    renderBanner({
      parkedTurn: noticeOnlyView(notice({ trigger: "" })),
      plan: plan(),
      sessionId: "session-1",
    });

    expect(resumedText()).toContain("已由监督自动恢复");
    expect(resumedText()).not.toContain("trigger=");
  });

  it("迟到唤醒：通知被清除（归约层 TTL / 新一轮挂起）后提示消失", () => {
    renderBanner({ parkedTurn: noticeOnlyView(notice()), plan: plan(), sessionId: "session-1" });
    expect(container.querySelector('[data-testid="session-resumed-notice"]')).not.toBeNull();

    renderBanner({ parkedTurn: null, plan: plan(), sessionId: "session-1" });
    expect(container.querySelector('[data-testid="session-resumed-notice"]')).toBeNull();
    expect(banner()).toBeNull();
  });
});
