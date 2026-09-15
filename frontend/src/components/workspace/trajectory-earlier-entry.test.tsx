/**
 * 轨迹面板「加载更早的轨迹」入口的贴顶可见性：与对话面共用同一条判定——
 * 贴在底部读最新事件时不再常驻遮挡列表，滚回贴顶区立刻又能找到（手动兜底入口不丢），
 * 空列表（没有滚动容器）时仍然可见。覆盖「按钮一直浮在列表上」与
 * 「容器后挂载后判定失效（会话从空到有事件）」两类断点。
 */
import { act, type ComponentProps } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { TrajectoryStore } from "@/hooks/workspace/use-trajectory-snapshot";
import { createTrajectoryStore } from "@/hooks/workspace/use-trajectory-snapshot";

import { TrajectoryView } from "./trajectory/trajectory-view";
import { stubResizeObserver } from "./trajectory/subagent-session-dialog.test-helpers";
import {
  EARLIER_ENTRY_HIDE_TOP_THRESHOLD,
  EARLIER_ENTRY_SHOW_TOP_THRESHOLD,
} from "./earlier-entry-visibility";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

describe("TrajectoryView earlier-entry visibility", () => {
  let container: HTMLDivElement;
  let root: Root;
  let store: TrajectoryStore;

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    stubResizeObserver();
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    store = createTrajectoryStore();
  });

  afterEach(() => {
    act(() => {
      store.dispose();
      root.unmount();
    });
    container.remove();
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
    vi.unstubAllGlobals();
  });

  function renderView(props: Partial<ComponentProps<typeof TrajectoryView>> = {}) {
    act(() => {
      root.render(<TrajectoryView store={store} {...props} />);
    });
  }

  /** 推一条可渲染事件：列表非空 → 滚动容器（连同入口行）挂载。 */
  function pushRow(seq = 1) {
    act(() => {
      store.push("chunk", {
        type: "text",
        content: "最近的事件",
        _event: { sequence: seq },
      });
      store.flush();
    });
  }

  function listElement() {
    return container.querySelector("[data-trajectory-list]");
  }

  function earlierEntry() {
    return container.querySelector("[data-trajectory-load-earlier]");
  }

  /**
   * jsdom 不实现真实滚动：按用例覆写 `scrollTop` 再派发 scroll 事件，驱动可见性判定
   * 与（共用同一事件源的）触顶续页。
   */
  async function scrollTo(scroller: HTMLDivElement, scrollTop: number) {
    Object.defineProperty(scroller, "scrollTop", {
      configurable: true,
      value: scrollTop,
      writable: true,
    });
    await act(async () => {
      scroller.dispatchEvent(new Event("scroll"));
    });
  }

  it("贴顶时显示入口，且它是滚动口顶部的 sticky 行", () => {
    renderView({ hasEarlier: true });
    pushRow();

    const entry = earlierEntry() as HTMLElement | null;
    expect(entry).toBeInstanceOf(HTMLElement);
    expect(entry?.className).toContain("sticky");
    expect(entry?.className).toContain("top-0");
    expect(entry?.textContent).toContain("加载更早的轨迹");
  });

  it("贴在底部读最新事件时收起入口，滚回贴顶区又露面", async () => {
    renderView({ hasEarlier: true });
    pushRow();
    const list = listElement();
    expect(list).toBeInstanceOf(HTMLElement);
    if (!(list instanceof HTMLDivElement)) {
      return;
    }

    await scrollTo(list, 600);
    expect(earlierEntry()).toBeNull();

    await scrollTo(list, EARLIER_ENTRY_SHOW_TOP_THRESHOLD);
    expect(earlierEntry()).not.toBeNull();
  });

  it("滞回带内保持上一次结论（出现 120 / 消失 240）", async () => {
    renderView({ hasEarlier: true });
    pushRow();
    const list = listElement();
    if (!(list instanceof HTMLDivElement)) {
      throw new Error("轨迹列表未挂载");
    }

    await scrollTo(list, 600);
    expect(earlierEntry()).toBeNull();
    await scrollTo(list, EARLIER_ENTRY_HIDE_TOP_THRESHOLD - 1);
    expect(earlierEntry()).toBeNull();

    await scrollTo(list, EARLIER_ENTRY_SHOW_TOP_THRESHOLD);
    expect(earlierEntry()).not.toBeNull();
    await scrollTo(list, EARLIER_ENTRY_HIDE_TOP_THRESHOLD - 1);
    expect(earlierEntry()).not.toBeNull();

    await scrollTo(list, EARLIER_ENTRY_HIDE_TOP_THRESHOLD + 1);
    expect(earlierEntry()).toBeNull();
  });

  it("窗口内没有可渲染行（没有滚动容器）时入口仍然兜底可见", () => {
    renderView({ hasEarlier: true });
    expect(listElement()).toBeNull();
    expect(earlierEntry()).not.toBeNull();
  });

  it("容器后挂载（空 → 有事件）后判定仍生效：贴底滚动收起入口", async () => {
    // 容器挂载键回归：不补挂监听时，判定会停在「空列表 = 贴顶」上，入口永远收不起来。
    renderView({ hasEarlier: true });
    expect(listElement()).toBeNull();

    pushRow();
    const list = listElement();
    if (!(list instanceof HTMLDivElement)) {
      throw new Error("轨迹列表未挂载");
    }

    await scrollTo(list, 600);
    expect(earlierEntry()).toBeNull();
  });

  it("已到日志开头时不渲染入口", () => {
    renderView({ hasEarlier: false });
    pushRow();
    expect(earlierEntry()).toBeNull();
  });

  it("可见性收紧不改变交互：点入口仍走同一条加载回调", () => {
    const onLoadEarlier = vi.fn();
    renderView({ hasEarlier: true, onLoadEarlier });
    pushRow();

    const button = earlierEntry()?.querySelector("button");
    expect(button).toBeInstanceOf(HTMLButtonElement);
    act(() => {
      (button as HTMLButtonElement | null)?.click();
    });
    expect(onLoadEarlier).toHaveBeenCalledTimes(1);
  });
});
