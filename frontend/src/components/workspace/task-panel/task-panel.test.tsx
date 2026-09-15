// @vitest-environment jsdom

// composer 上沿「当前任务」面板渲染单测（方案 §8.2 前端侧）：
// 可见性策略、默认折叠、展开/收起、按会话持久化、超长列表提示。
//
// 断言只依赖 testid / aria 属性 / data-status，不绑定具体语言文案——
// 文案回归由 i18n 词典与 fallback 测试看护。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { i18n } from "@/i18n";
import type { TodoItem, TodoSnapshot } from "@/lib/thread-state/todos";

import { TodoPanel } from "./index";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

const SESSION_ID = "session-todos-1";
const COLLAPSED_KEY = `workspace.todoPanel.collapsed.${SESSION_ID}`;

function makeSnapshot(
  items: TodoItem[],
  overrides: Partial<TodoSnapshot> = {},
): TodoSnapshot {
  return {
    items,
    source: "runtime",
    sessionId: SESSION_ID,
    goalId: "",
    seq: 1,
    ...overrides,
  };
}

const TODO_ITEMS: TodoItem[] = [
  { content: "读计划", status: "completed", activeForm: "读计划" },
  { content: "写组件", status: "in_progress", activeForm: "正在写组件" },
  { content: "补测试", status: "pending", activeForm: "" },
];

describe("TodoPanel（composer 上沿任务面板）", () => {
  let container: HTMLDivElement;
  let root: Root;

  const panel = () => container.querySelector('[data-testid="todo-panel"]');
  const header = () => container.querySelector("button");
  const list = () => container.querySelector('[data-testid="todo-panel-list"]');
  const rows = () =>
    Array.from(container.querySelectorAll('[data-testid="todo-panel-item"]'));

  const renderPanel = (props: {
    sessionId?: string;
    snapshot?: TodoSnapshot | null;
  }) => {
    act(() => {
      root.render(<TodoPanel {...props} />);
    });
  };

  beforeEach(() => {
    void i18n;
    sessionStorage.clear();
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

  it("无快照 / 空列表 / 全部完成时不渲染（不占位）", () => {
    renderPanel({ sessionId: SESSION_ID });
    expect(panel()).toBeNull();

    renderPanel({ sessionId: SESSION_ID, snapshot: makeSnapshot([]) });
    expect(panel()).toBeNull();

    renderPanel({
      sessionId: SESSION_ID,
      snapshot: makeSnapshot([{ content: "收尾", status: "completed", activeForm: "" }]),
    });
    expect(panel()).toBeNull();
  });

  it("缺少会话 id 时不渲染（避免串会话数据）", () => {
    renderPanel({ snapshot: makeSnapshot(TODO_ITEMS) });
    expect(panel()).toBeNull();
  });

  it("默认折叠为单行条：头部含进度与当前进行项，列表不渲染", () => {
    renderPanel({ sessionId: SESSION_ID, snapshot: makeSnapshot(TODO_ITEMS) });

    expect(panel()).not.toBeNull();
    expect(header()?.getAttribute("aria-expanded")).toBe("false");
    expect(header()?.getAttribute("aria-controls")).toBeTruthy();
    expect(list()).toBeNull();
    // 当前进行项（active_form 优先）直接出现在折叠条上。
    expect(header()?.textContent).toContain("正在写组件");
    // 键未解析时会原样输出键名，这里顺便看护 i18n 接线。
    expect(panel()?.textContent).not.toContain("panels.todos.");
    expect(panel()?.getAttribute("aria-label")).toBeTruthy();
  });

  it("点击头部展开列表：逐条保留状态，再点收起", () => {
    renderPanel({ sessionId: SESSION_ID, snapshot: makeSnapshot(TODO_ITEMS) });

    act(() => {
      header()!.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });

    expect(header()?.getAttribute("aria-expanded")).toBe("true");
    expect(list()).not.toBeNull();
    expect(rows().map((row) => row.getAttribute("data-status"))).toEqual([
      "completed",
      "in_progress",
      "pending",
    ]);
    expect(rows()[1]?.textContent).toContain("写组件");
    expect(sessionStorage.getItem(COLLAPSED_KEY)).toBe("0");

    act(() => {
      header()!.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(list()).toBeNull();
    expect(sessionStorage.getItem(COLLAPSED_KEY)).toBe("1");
  });

  it("展开态按会话持久化：重新挂载仍是展开（会话隔离）", () => {
    sessionStorage.setItem(COLLAPSED_KEY, "0");
    renderPanel({ sessionId: SESSION_ID, snapshot: makeSnapshot(TODO_ITEMS) });
    expect(list()).not.toBeNull();

    // 另一个会话不受该偏好影响，仍按默认折叠渲染。
    act(() => {
      root.unmount();
    });
    root = createRoot(container);
    renderPanel({ sessionId: "session-todos-2", snapshot: makeSnapshot(TODO_ITEMS) });
    expect(list()).toBeNull();
  });

  it("超过 8 项时展开后提示还有 N 项", () => {
    const many = Array.from({ length: 11 }, (_, index) => ({
      content: `任务 ${index + 1}`,
      status: index === 0 ? ("in_progress" as const) : ("pending" as const),
      activeForm: "",
    }));
    renderPanel({ sessionId: SESSION_ID, snapshot: makeSnapshot(many) });

    act(() => {
      header()!.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });

    expect(rows()).toHaveLength(11);
    const more = container.querySelector('[data-testid="todo-panel-more"]');
    expect(more?.textContent).toContain("3");
  });

  it("进度摘要提供礼貌播报位，文本只在任务推进时变化（§8.1 D6）", () => {
    const status = () =>
      container.querySelector('[data-testid="todo-panel-status"]');

    renderPanel({ sessionId: SESSION_ID, snapshot: makeSnapshot(TODO_ITEMS) });

    expect(status()?.getAttribute("aria-live")).toBe("polite");
    expect(status()?.getAttribute("role")).toBe("status");
    // 播报位在折叠按钮之外：同一段文案不会两次进入可访问名。
    expect(header()?.contains(status())).toBe(false);
    const first = status()?.textContent ?? "";
    expect(first).toContain("正在写组件");
    expect(first).not.toContain("panels.todos.");

    // 内容未变的再渲染不改文本（相同字符串不触发 DOM 写入 → 不产生多余播报）。
    renderPanel({ sessionId: SESSION_ID, snapshot: makeSnapshot(TODO_ITEMS) });
    expect(status()?.textContent).toBe(first);

    // 任务推进（当前项与计数同时变化）后才更新。
    renderPanel({
      sessionId: SESSION_ID,
      snapshot: makeSnapshot([
        { content: "写组件", status: "completed", activeForm: "写组件" },
        { content: "补测试", status: "in_progress", activeForm: "正在补测试" },
      ]),
    });
    const next = status()?.textContent ?? "";
    expect(next).not.toBe(first);
    expect(next).toContain("正在补测试");
  });
});
