// @vitest-environment jsdom

// 2026-09-15 优化（方案 §15）：派生目录组头「注册并新建会话」动作的单测。
// 覆盖：单一入口 + 无障碍名（与纯注册动作区分）/ 点击回调 / 忙碌态禁用与旋转图标 /
// 忙碌时点击不触发（与「注册」按钮共用 in-flight 守卫）。

import { type TFunction } from "i18next";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { WorkspaceSidebarDirectoryDerivedActions } from "./directory-derived-actions";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

const REGISTER_AND_NEW_CHAT_LABEL = "注册并在该目录下新建会话";

const LABELS: Record<string, string> = {
  "sidebar.directories.registerAndNewChat": REGISTER_AND_NEW_CHAT_LABEL,
};

const t = ((key: string) => LABELS[key] ?? key) as unknown as TFunction<"workspace">;

describe("WorkspaceSidebarDirectoryDerivedActions", () => {
  let container: HTMLDivElement;
  let root: Root | null;
  const onRegisterAndCreate = vi.fn();

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    onRegisterAndCreate.mockReset();
  });

  afterEach(() => {
    if (root) {
      act(() => root?.unmount());
      root = null;
    }
    container.remove();
  });

  function render(busy = false) {
    act(() => {
      root?.render(
        <WorkspaceSidebarDirectoryDerivedActions
          busy={busy}
          onRegisterAndCreate={onRegisterAndCreate}
          t={t}
        />,
      );
    });
  }

  function action(): HTMLButtonElement | null {
    return container.querySelector<HTMLButtonElement>(
      '[data-testid="sidebar-directory-register-and-create"]',
    );
  }

  function click(element: HTMLElement | null) {
    act(() => {
      element?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
  }

  it("派生目录组头只有这一个动作入口，无障碍名点明「注册并建会话」", () => {
    render();

    expect(container.querySelectorAll("button")).toHaveLength(1);
    expect(action()?.getAttribute("aria-label")).toBe(
      REGISTER_AND_NEW_CHAT_LABEL,
    );
    expect(action()?.getAttribute("title")).toBe(REGISTER_AND_NEW_CHAT_LABEL);
    expect(action()?.disabled).toBe(false);
    // 空闲态没有旋转图标。
    expect(container.querySelector("svg.animate-spin")).toBeNull();
  });

  it("点击触发合并动作（注册 + 在该目录下新建会话）", () => {
    render();
    click(action());

    expect(onRegisterAndCreate).toHaveBeenCalledTimes(1);
  });

  it("忙碌态禁用入口并换成旋转图标", () => {
    render(true);

    expect(action()?.disabled).toBe(true);
    expect(container.querySelector("svg.animate-spin")).not.toBeNull();
  });

  it("忙碌态点击不重复触发（in-flight 守卫）", () => {
    render(true);
    click(action());

    expect(onRegisterAndCreate).not.toHaveBeenCalled();
  });
});
