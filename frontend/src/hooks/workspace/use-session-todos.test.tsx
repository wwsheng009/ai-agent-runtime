// @vitest-environment jsdom

// useSessionTodos 单测（方案 §5.3 / §8.1）：
// 视图模型（可见性 / 计数 / 当前进行项）与「按会话隔离的折叠态」持久化。
//
// 不引入 testing-library：用 createRoot + act + 一个只读探针组件导出视图模型，
// 与仓库其它 hook 测试的写法保持一致。

import { act, useEffect } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { TodoItem, TodoSnapshot } from "@/lib/thread-state/todos";

import { useSessionTodos, type SessionTodosViewModel } from "./use-session-todos";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

const SESSION_ID = "session-hook-1";
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
    seq: 3,
    ...overrides,
  };
}

const ITEMS: TodoItem[] = [
  { content: "甲", status: "completed", activeForm: "完成了甲" },
  { content: "乙", status: "in_progress", activeForm: "正在做乙" },
  { content: "丙", status: "pending", activeForm: "" },
];

describe("useSessionTodos（composer 任务面板视图模型）", () => {
  let container: HTMLDivElement;
  let root: Root;
  let latest: SessionTodosViewModel | null;

  function Probe({
    sessionId,
    snapshot,
  }: {
    sessionId?: string;
    snapshot?: TodoSnapshot | null;
  }) {
    const viewModel = useSessionTodos({ sessionId, snapshot });
    // 探针不做渲染，只在每次提交后把最新视图模型交给测试；
    // 赋值放在 effect 里（不在渲染期写外部变量）。
    useEffect(() => {
      latest = viewModel;
    });
    return null;
  }

  const renderProbe = (props: {
    sessionId?: string;
    snapshot?: TodoSnapshot | null;
  }) => {
    act(() => {
      root.render(<Probe {...props} />);
    });
  };

  const view = () => {
    if (!latest) {
      throw new Error("探针尚未完成首次渲染");
    }
    return latest;
  };

  beforeEach(() => {
    sessionStorage.clear();
    latest = null;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    document.body.innerHTML = "";
    vi.restoreAllMocks();
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  it("无快照 / 空列表 / 全部完成 → 不可见（不臆造数据）", () => {
    renderProbe({ sessionId: SESSION_ID });
    expect(view().visible).toBe(false);
    expect(view().items).toEqual([]);
    expect(view().counts).toEqual({ total: 0, pending: 0, inProgress: 0, completed: 0 });
    expect(view().currentLabel).toBe("");

    renderProbe({ sessionId: SESSION_ID, snapshot: makeSnapshot([]) });
    expect(view().visible).toBe(false);

    renderProbe({
      sessionId: SESSION_ID,
      snapshot: makeSnapshot([{ content: "收尾", status: "completed", activeForm: "" }]),
    });
    expect(view().visible).toBe(false);
  });

  it("缺少会话 id → 不可见（避免把上一个会话的任务画到新会话上）", () => {
    renderProbe({ snapshot: makeSnapshot(ITEMS) });
    expect(view().visible).toBe(false);
  });

  it("快照换代整体替换：items / counts / 当前进行项同步更新", () => {
    renderProbe({ sessionId: SESSION_ID, snapshot: makeSnapshot(ITEMS) });
    expect(view().visible).toBe(true);
    expect(view().items.map((item) => item.content)).toEqual(["甲", "乙", "丙"]);
    expect(view().counts).toEqual({ total: 3, pending: 1, inProgress: 1, completed: 1 });
    expect(view().current?.content).toBe("乙");
    expect(view().currentLabel).toBe("正在做乙");

    renderProbe({
      sessionId: SESSION_ID,
      snapshot: makeSnapshot(
        [{ content: "新任务", status: "in_progress", activeForm: "" }],
        { seq: 4, source: "history" },
      ),
    });
    expect(view().items.map((item) => item.content)).toEqual(["新任务"]);
    expect(view().counts.total).toBe(1);
    // active_form 缺失时回退任务描述，折叠条不会只剩前缀。
    expect(view().currentLabel).toBe("新任务");
    expect(view().snapshot?.seq).toBe(4);
  });

  it("默认折叠；切换后写入 sessionStorage", () => {
    renderProbe({ sessionId: SESSION_ID, snapshot: makeSnapshot(ITEMS) });
    expect(view().collapsed).toBe(true);

    act(() => view().toggleCollapsed());
    expect(view().collapsed).toBe(false);
    expect(sessionStorage.getItem(COLLAPSED_KEY)).toBe("0");

    act(() => view().toggleCollapsed());
    expect(view().collapsed).toBe(true);
    expect(sessionStorage.getItem(COLLAPSED_KEY)).toBe("1");
  });

  it("展开偏好按会话隔离：读写各用各的键", () => {
    sessionStorage.setItem(COLLAPSED_KEY, "0");
    renderProbe({ sessionId: SESSION_ID, snapshot: makeSnapshot(ITEMS) });
    expect(view().collapsed).toBe(false);

    // 切到另一个会话：读它自己的偏好（无记录 → 默认折叠），且不写坏上一个会话的键。
    renderProbe({ sessionId: "session-hook-2", snapshot: makeSnapshot(ITEMS) });
    expect(view().collapsed).toBe(true);
    act(() => view().toggleCollapsed());
    expect(sessionStorage.getItem("workspace.todoPanel.collapsed.session-hook-2")).toBe("0");
    expect(sessionStorage.getItem(COLLAPSED_KEY)).toBe("0");

    // 切回原会话：仍是展开态。
    renderProbe({ sessionId: SESSION_ID, snapshot: makeSnapshot(ITEMS) });
    expect(view().collapsed).toBe(false);
  });

  it("sessionStorage 不可用时降级为默认折叠，不抛错", () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("storage denied");
    });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("storage denied");
    });

    renderProbe({ sessionId: SESSION_ID, snapshot: makeSnapshot(ITEMS) });
    expect(view().collapsed).toBe(true);
    expect(() => act(() => view().toggleCollapsed())).not.toThrow();
    expect(view().collapsed).toBe(false);
  });
});
