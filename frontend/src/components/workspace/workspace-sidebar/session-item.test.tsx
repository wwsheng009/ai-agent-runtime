// 2026-09-16 样式优化：会话行「单行 + 时间 / 菜单图标同槽互换」的结构契约。
// jsdom 无真实 hover 与布局，因此这里只锁结构不变量：时间落在标题行内、让位类与菜单槽的
// 显示类成对出现、以及「无时间可让位 / 无菜单可显示」两个降级分支；真实的可见性互换与
// 单行几何由 e2e（sidebar-session-actions.spec.ts）验收。

import { LoaderCircleIcon } from "lucide-react";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { type RuntimeSessionRecord } from "@/types/runtime";

import {
  SidebarSessionItem,
  type SidebarSessionItemProps,
} from "./session-item";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

const SESSION: RuntimeSessionRecord = {
  id: "session-row-1",
  metadata: { title: "侧栏会话" },
  createdAt: "2026-09-15T12:00:00Z",
  updatedAt: "2026-09-16T01:00:00Z",
};

const ACTION_LABELS = {
  archivedBadge: "已归档",
  archive: "归档会话",
  delete: "删除会话",
  fork: "Fork 会话",
  menu: "会话操作",
  restore: "恢复会话",
};

function itemProps(
  overrides: Partial<SidebarSessionItemProps> = {},
): SidebarSessionItemProps {
  return {
    actionLabels: ACTION_LABELS,
    isActive: false,
    onArchive: vi.fn(),
    onCancelRename: vi.fn(),
    onDelete: vi.fn(),
    onRenameSubmit: vi.fn(),
    onSelect: vi.fn(),
    onStartRename: vi.fn(),
    renameLabels: { placeholder: "输入新标题", rename: "重命名会话" },
    renaming: false,
    session: SESSION,
    statusIcon: null,
    time: { relative: "8小时前", title: "创建于 2026/9/15 20:00:00" },
    title: "侧栏会话",
    ...overrides,
  };
}

describe("SidebarSessionItem 单行时间槽", () => {
  let container: HTMLDivElement;
  let root: Root | null;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
  });

  afterEach(() => {
    if (root) {
      act(() => root?.unmount());
    }
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  function renderRow(overrides: Partial<SidebarSessionItemProps> = {}) {
    act(() => {
      root?.render(<SidebarSessionItem {...itemProps(overrides)} />);
    });
    const row = container.querySelector<HTMLElement>('[role="treeitem"]');
    if (!row) {
      throw new Error("未渲染会话行");
    }
    return {
      row,
      button: row.querySelector<HTMLButtonElement>("button")!,
      time: row.querySelector<HTMLElement>('[data-testid="session-row-time"]'),
      actionsSlot: row.querySelector<HTMLElement>(
        '[data-testid="session-row-actions-slot"]',
      ),
    };
  }

  it("时间与标题同处一行：时间是行按钮的直接子元素，不再有第二个行块", () => {
    const { button, time } = renderRow();

    expect(time).not.toBeNull();
    expect(time?.textContent).toBe("8小时前");
    // 旧结构是「标题块 + 时间块」两个上下行块，时间嵌在标题块里；新结构时间与标题同级。
    expect(time?.parentElement).toBe(button);
    expect(button.querySelectorAll("div")).toHaveLength(1);
    expect(button.querySelector("div")?.textContent).toBe("侧栏会话");
    // 整行 tooltip 吸收原时间元素的完整时间戳（时间让位隐藏时不再挂在不可见元素上）。
    expect(button.getAttribute("title")).toBe(
      "侧栏会话 · 创建于 2026/9/15 20:00:00",
    );
  });

  it("行状态图标进入整行 tooltip，行容器仍是 hover / 聚焦的作用域", () => {
    const { button, row } = renderRow({
      statusIcon: {
        icon: LoaderCircleIcon,
        label: "运行中",
        toneClassName: "border-accent-primary-border",
      },
    });

    expect(button.getAttribute("title")).toBe(
      "侧栏会话 · 运行中 · 创建于 2026/9/15 20:00:00",
    );
    expect(row.className).toContain("group/session");
  });

  it("时间与菜单图标同槽互换：时间让位类与菜单槽显示类成对出现", () => {
    const { button, time, actionsSlot } = renderRow();

    expect(time?.className).toContain("group-hover/session:opacity-0");
    // 键盘路径按菜单槽自身的 focus-within 判定（不能用 group-focus-within：
    // 鼠标点击行按钮后按钮保留焦点，会让选中行的「平时显示时间」失效）。
    expect(time?.className).toContain(
      "group-has-[.session-row-actions:focus-within]/session:opacity-0",
    );
    expect(time?.className).not.toContain("group-focus-within");
    expect(actionsSlot?.className).toContain("opacity-0");
    expect(actionsSlot?.className).toContain("group-hover/session:opacity-100");
    expect(actionsSlot?.className).toContain("focus-within:opacity-100");
    expect(actionsSlot?.className).toContain("session-row-actions");
    // 菜单槽悬浮在行尾（right-1）——与流内顶到行尾的时间重合，因此时间只需 pr-2。
    expect(actionsSlot?.className).toContain("right-1");
    expect(actionsSlot?.className).toContain("top-1/2");
    expect(button.className).toContain("pr-2");
  });

  it("时间戳缺失时不渲染时间，且为悬浮菜单保留右侧预留位", () => {
    const { button, time } = renderRow({ time: undefined });

    expect(time).toBeNull();
    // 没有时间可让位时，菜单图标会落在状态图标 / 徽标上，保留固定右内边距避开。
    expect(button.className).toContain("pr-8");
  });

  it("没有可显示菜单时不隐藏时间", () => {
    const { button, time, actionsSlot } = renderRow({
      actionLabels: undefined,
      onArchive: undefined,
      onDelete: undefined,
      onFork: undefined,
    });

    expect(actionsSlot?.childElementCount).toBe(0);
    expect(time?.className).not.toContain("opacity-0");
    expect(button.className).toContain("pr-2");
  });
});
