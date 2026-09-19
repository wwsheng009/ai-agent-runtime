// @vitest-environment jsdom

// 2026-09-15 侧栏样式优化（合并方案 §11）：目录组头动作收敛为单一菜单入口的单测。
// 覆盖：默认只有入口、点击开菜单与三项文案、动作回调与关闭、忙碌态禁用、
// Escape 关闭并把焦点还给入口、ArrowDown 打开并聚焦首个可用项。

import { type TFunction } from "i18next";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { WorkspaceSidebarDirectoryGroupActions } from "./directory-group-actions";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

const ACTIONS_LABEL = "目录操作";
// 文案于 2c05eaac 精简（"在该目录下新建会话"→"新建会话" 等），并新增「刷新」菜单项。
const NEW_CHAT_LABEL = "新建会话";
const REFRESH_LABEL = "刷新";
const RENAME_LABEL = "重命名";
const REMOVE_LABEL = "移除";

const LABELS: Record<string, string> = {
  "sidebar.directories.actions": ACTIONS_LABEL,
  "sidebar.directories.deleteTitle": REMOVE_LABEL,
  "sidebar.directories.newChat": NEW_CHAT_LABEL,
  "sidebar.directories.refresh": REFRESH_LABEL,
  "sidebar.directories.rename": RENAME_LABEL,
};

const t = ((key: string) => LABELS[key] ?? key) as unknown as TFunction<"workspace">;

describe("WorkspaceSidebarDirectoryGroupActions", () => {
  let container: HTMLDivElement;
  let root: Root | null;
  const onCreate = vi.fn();
  const onRename = vi.fn();
  const onRemove = vi.fn();

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    onCreate.mockReset();
    onRename.mockReset();
    onRemove.mockReset();
  });

  afterEach(() => {
    if (root) {
      act(() => root?.unmount());
      root = null;
    }
    container.remove();
  });

  function render(creating = false) {
    act(() => {
      root?.render(
        <WorkspaceSidebarDirectoryGroupActions
          creating={creating}
          onCreate={onCreate}
          onRemove={onRemove}
          onRename={onRename}
          t={t}
        />,
      );
    });
  }

  function trigger(): HTMLButtonElement | null {
    return container.querySelector<HTMLButtonElement>(
      `button[aria-label="${ACTIONS_LABEL}"]`,
    );
  }

  function menuItems(): HTMLButtonElement[] {
    return Array.from(
      container.querySelectorAll<HTMLButtonElement>('[role="menuitem"]'),
    );
  }

  function menu(): HTMLElement | null {
    return container.querySelector<HTMLElement>('[role="menu"]');
  }

  function click(element: HTMLElement | null) {
    act(() => {
      element?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
  }

  function keyDown(element: HTMLElement | null, key: string) {
    act(() => {
      element?.dispatchEvent(
        new KeyboardEvent("keydown", { key, bubbles: true }),
      );
    });
  }

  it("默认只渲染一个菜单入口，菜单未展开", () => {
    render();

    const entry = trigger();
    expect(entry).not.toBeNull();
    // 三个平铺图标已收敛：目录组头只剩一个动作按钮。
    expect(container.querySelectorAll("button")).toHaveLength(1);
    expect(entry?.getAttribute("aria-haspopup")).toBe("menu");
    expect(entry?.getAttribute("aria-expanded")).toBe("false");
    expect(menu()).toBeNull();
  });

  it("点击入口展开菜单，四个动作按「新建 / 刷新 / 重命名 / 移除」顺序排列", () => {
    render();
    click(trigger());

    expect(trigger()?.getAttribute("aria-expanded")).toBe("true");
    expect(menu()).not.toBeNull();
    expect(menuItems().map((item) => item.textContent)).toEqual([
      NEW_CHAT_LABEL,
      REFRESH_LABEL,
      RENAME_LABEL,
      REMOVE_LABEL,
    ]);
  });

  it("点击菜单项触发回调并关闭菜单", () => {
    render();
    click(trigger());

    const [, , rename] = menuItems();
    click(rename ?? null);
    expect(onRename).toHaveBeenCalledTimes(1);
    expect(menu()).toBeNull();

    click(trigger());
    click(menuItems()[0] ?? null);
    expect(onCreate).toHaveBeenCalledTimes(1);

    click(trigger());
    click(menuItems()[3] ?? null);
    expect(onRemove).toHaveBeenCalledTimes(1);
    expect(menu()).toBeNull();
  });

  it("目录内正在新建会话：新建项禁用且不可触发，其余项照常可用", () => {
    render(true);
    click(trigger());

    const [newChat, , rename] = menuItems();
    expect(newChat?.disabled).toBe(true);
    expect(
      newChat?.querySelector("svg.animate-spin"),
    ).not.toBeNull();

    click(newChat ?? null);
    expect(onCreate).not.toHaveBeenCalled();

    click(rename ?? null);
    expect(onRename).toHaveBeenCalledTimes(1);
  });

  it("Escape 关闭菜单并把焦点还给入口", () => {
    render();
    click(trigger());
    keyDown(container.querySelector('[role="menu"]'), "Escape");

    expect(menu()).toBeNull();
    expect(document.activeElement).toBe(trigger());
  });

  it("入口上 ArrowDown 展开菜单并聚焦首个可用项", () => {
    render(true);
    keyDown(trigger(), "ArrowDown");

    expect(menu()).not.toBeNull();
    // 忙碌时新建项被禁用，焦点落到下一个可用项「刷新」。
    expect(document.activeElement).toBe(menuItems()[1]);
  });
});
