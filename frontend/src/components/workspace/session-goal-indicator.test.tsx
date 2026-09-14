// @vitest-environment jsdom

// P2-9 子片 3：目标指示条渲染单测——四相 data-phase、目标文本、token 展示、
// 无数据不渲染、goal_missing 后消失。断言只依赖结构（testid / data-phase）与
// 后端原始字段（objective / 原始 status），不绑定具体语言文案。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { i18n } from "@/i18n";
import { recordGoalToolEnd, resetSessionGoalStore } from "@/lib/session-goal/store";

import { SessionGoalIndicator } from "./session-goal-indicator";

const activeGoal = {
  goal_id: "goal_1",
  session_id: "session-1",
  objective: "完成批次 11 目标指示",
  status: "active",
  token_budget: 20000,
  tokens_used: 1200,
  updated_at: "2026-09-13T10:30:00+08:00",
};

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

describe("SessionGoalIndicator（P2-9 目标四相指示）", () => {
  let container: HTMLDivElement;
  let root: Root;

  const chip = () => container.querySelector('[data-testid="topbar-session-goal"]');

  const record = (content: string, name = "get_goal") =>
    recordGoalToolEnd("session-1", { tool: { name, content } });

  beforeEach(() => {
    void i18n;
    resetSessionGoalStore();
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  it("无捕获数据时不渲染（不编造状态）", () => {
    act(() => {
      root.render(<SessionGoalIndicator sessionId="session-1" />);
    });
    expect(chip()).toBeNull();
  });

  it("active：四相标记 + 目标文本 + used/budget token", () => {
    record(JSON.stringify({ goal: activeGoal, remaining_tokens: 18800 }));
    act(() => {
      root.render(<SessionGoalIndicator sessionId="session-1" />);
    });
    const node = chip();
    expect(node?.getAttribute("data-phase")).toBe("active");
    expect(
      container.querySelector('[data-testid="topbar-session-goal-objective"]')
        ?.textContent,
    ).toBe("完成批次 11 目标指示");
    const usage = container.querySelector(
      '[data-testid="topbar-session-goal-usage"]',
    )?.textContent;
    expect(usage).toContain("1.2k");
    expect(usage).toContain("20k");
    expect(node?.getAttribute("title")).toContain("完成批次 11 目标指示");
  });

  it("budget_limited / paused / complete 都按四相渲染", () => {
    for (const status of ["budget_limited", "paused", "complete"]) {
      resetSessionGoalStore();
      record(JSON.stringify({ goal: { ...activeGoal, status } }));
      act(() => {
        root.render(<SessionGoalIndicator sessionId="session-1" />);
      });
      expect(chip()?.getAttribute("data-phase")).toBe(status);
    }
  });

  it("未知状态显示 unknown 并在 title 透出后端原始 status", () => {
    record(JSON.stringify({ goal: { ...activeGoal, status: "quota_exhausted" } }));
    act(() => {
      root.render(<SessionGoalIndicator sessionId="session-1" />);
    });
    expect(chip()?.getAttribute("data-phase")).toBe("unknown");
    expect(chip()?.getAttribute("title")).toContain("quota_exhausted");
  });

  it("缺 token 字段时不显示用量（不补零）", () => {
    record(
      JSON.stringify({
        goal: { goal_id: "goal_2", objective: "无预算目标", status: "active" },
      }),
    );
    act(() => {
      root.render(<SessionGoalIndicator sessionId="session-1" />);
    });
    expect(chip()?.getAttribute("data-phase")).toBe("active");
    expect(
      container.querySelector('[data-testid="topbar-session-goal-usage"]'),
    ).toBeNull();
  });

  it("update_goal 的 goal_missing 结果让指示条消失", () => {
    record(JSON.stringify({ goal: activeGoal }));
    act(() => {
      root.render(<SessionGoalIndicator sessionId="session-1" />);
    });
    expect(chip()).not.toBeNull();
    act(() => {
      record(
        JSON.stringify({ updated: false, goal: null, reason: "goal_missing" }),
        "update_goal",
      );
    });
    expect(chip()).toBeNull();
  });

  it("非 goal 工具结果不产生指示条", () => {
    act(() => {
      recordGoalToolEnd("session-1", {
        tool: { name: "bash", content: JSON.stringify({ goal: activeGoal }) },
      });
    });
    act(() => {
      root.render(<SessionGoalIndicator sessionId="session-1" />);
    });
    expect(chip()).toBeNull();
  });
});
