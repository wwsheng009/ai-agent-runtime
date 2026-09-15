/**
 * 对话面「加载更早」入口：入口必须**在贴顶区**看得见、点得动、滚到顶端能自动续页，
 * 且贴在底部读最新消息时不常驻遮挡正文（本轮诉求）。
 * 断言可见性策略（贴顶 + 滞回）、渲染、交互、触顶阈值四件事，覆盖「按钮存在但不
 * 工作」与「按钮一直浮在正文上」两类断点。
 */
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { ChatMessage } from "@/data/mock";

import { MessageList } from "./message-list";
import {
  EARLIER_ENTRY_HIDE_TOP_THRESHOLD,
  EARLIER_ENTRY_SHOW_TOP_THRESHOLD,
  resolveEarlierEntryAtTop,
} from "./message-list/use-message-list-earlier-entry";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

const messages: ChatMessage[] = [
  {
    id: "assistant-1",
    role: "assistant",
    author: "Runtime",
    label: "history",
    segments: [{ type: "text", content: "最新一页回答" }],
  },
];

describe("MessageList earlier-history entry", () => {
  let container: HTMLDivElement;
  let root: Root | null;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = null;
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
  });

  afterEach(() => {
    if (root) {
      act(() => {
        root?.unmount();
      });
    }
    container.remove();
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  function renderList(listMessages: ChatMessage[], hasMore: boolean) {
    return act(async () => {
      root = createRoot(container);
      root.render(
        <MessageList
          artifacts={[]}
          earlierLoader={{ hasMore, loading: false, onLoadEarlier: () => {} }}
          isResponding={false}
          messages={listMessages}
          onSelectArtifact={() => {}}
        />,
      );
    });
  }

  function scrollContainer() {
    const log = container.querySelector('[role="log"]');
    return log?.parentElement as HTMLDivElement;
  }

  function earlierEntry() {
    return container.querySelector("[data-message-load-earlier]");
  }

  /**
   * jsdom 不实现真实滚动：按用例覆写 `scrollTop` 再派发 scroll 事件，驱动可见性判定
   * 与触顶续页两条路径（两者都只认滚动容器上的 scroll 事件）。
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

  it("renders the load-earlier button when the backend reports an older page", async () => {
    await renderList(messages, true);
    const row = earlierEntry();
    expect(row).not.toBeNull();
    expect(row?.querySelector("button")?.textContent).toContain("加载更早");
  });

  it("pins the entry to the top of the scrollport so it stays visible inside the top zone", async () => {
    // 回归点：入口若只是「流内首行」，一页 100 条约 1.2 万 px，用户停在底部时它在
    // 视口上方 7,600+px（实测），表现为「页面上根本没有加载更早按钮」。
    await renderList(messages, true);
    const row = earlierEntry() as HTMLElement | null;
    expect(row?.className).toContain("sticky");
    expect(row?.className).toContain("top-0");
  });

  it("renders nothing when there is no older page left", async () => {
    await renderList(messages, false);
    expect(earlierEntry()).toBeNull();
  });

  it("keeps the entry visible even when the current page has no renderable rows", async () => {
    // 极端形态：最新一页解析后没有可见行，但更早还有内容——入口不能跟着消失。
    await renderList([], true);
    expect(earlierEntry()).not.toBeNull();
  });

  it("keeps the entry as the fallback when the list has no scrollbar at all", async () => {
    // 兜底不变式：内容不足一屏时不会有 scroll 事件（scrollTop 恒为 0）——判定为贴顶，
    // 入口仍然可见，否则这种会话就会出现「翻不动」死角。
    await renderList([], true);
    const scroller = scrollContainer();
    expect(scroller.scrollTop).toBe(0);
    expect(earlierEntry()).not.toBeNull();
  });

  it("hides the entry while the reader stays down in the tail, and shows it again near the top", async () => {
    // 本轮诉求：贴在底部读最新消息时，入口不该一直浮在正文上方挡字。
    await renderList(messages, true);
    const scroller = scrollContainer();
    await scrollTo(scroller, 600);
    expect(earlierEntry()).toBeNull();

    // 滚回贴顶区：重新露面（没有它就没有「加载更早」的手动兜底入口）。
    await scrollTo(scroller, EARLIER_ENTRY_SHOW_TOP_THRESHOLD);
    expect(earlierEntry()).not.toBeNull();
  });

  it("keeps the previous state inside the hysteresis band", async () => {
    // 出现阈值 120 / 消失阈值 240：带内保持上一次结论。入口是流内 sticky 行，出现与
    // 收起都会改变内容高度，并被「阅读保顶」补偿回 scrollTop——单阈值会自激闪烁。
    await renderList(messages, true);
    const scroller = scrollContainer();

    await scrollTo(scroller, 600);
    expect(earlierEntry()).toBeNull();
    await scrollTo(scroller, EARLIER_ENTRY_HIDE_TOP_THRESHOLD - 1);
    expect(earlierEntry()).toBeNull();

    await scrollTo(scroller, EARLIER_ENTRY_SHOW_TOP_THRESHOLD);
    expect(earlierEntry()).not.toBeNull();
    await scrollTo(scroller, EARLIER_ENTRY_HIDE_TOP_THRESHOLD - 1);
    expect(earlierEntry()).not.toBeNull();

    await scrollTo(scroller, EARLIER_ENTRY_HIDE_TOP_THRESHOLD + 1);
    expect(earlierEntry()).toBeNull();
  });

  it("calls the shared idempotent entry when the button is clicked", async () => {
    const onLoadEarlier = vi.fn();
    await act(async () => {
      root = createRoot(container);
      root.render(
        <MessageList
          artifacts={[]}
          earlierLoader={{ hasMore: true, loading: false, onLoadEarlier }}
          isResponding={false}
          messages={messages}
          onSelectArtifact={() => {}}
        />,
      );
    });

    await act(async () => {
      (container.querySelector("[data-message-load-earlier] button") as HTMLButtonElement).click();
    });
    expect(onLoadEarlier).toHaveBeenCalledTimes(1);
  });

  it("auto-loads when the list is scrolled to the top, but not from the middle", async () => {
    const onLoadEarlier = vi.fn();
    await act(async () => {
      root = createRoot(container);
      root.render(
        <MessageList
          artifacts={[]}
          earlierLoader={{ hasMore: true, loading: false, onLoadEarlier }}
          isResponding={false}
          messages={messages}
          onSelectArtifact={() => {}}
        />,
      );
    });

    const scroller = scrollContainer();
    // 中部：既不续页，也不显示入口。
    await scrollTo(scroller, 320);
    expect(onLoadEarlier).not.toHaveBeenCalled();
    expect(earlierEntry()).toBeNull();

    // 阈值内（64px）也算触顶：快速上滑不易整段跨过判定窗口。
    await scrollTo(scroller, 48);
    expect(onLoadEarlier).toHaveBeenCalledTimes(1);

    await scrollTo(scroller, 0);
    expect(onLoadEarlier).toHaveBeenCalledTimes(2);
  });

  it("disables the entry and shows the loading label while a page is in flight", async () => {
    await act(async () => {
      root = createRoot(container);
      root.render(
        <MessageList
          artifacts={[]}
          earlierLoader={{ hasMore: false, loading: true, onLoadEarlier: () => {} }}
          isResponding={false}
          messages={messages}
          onSelectArtifact={() => {}}
        />,
      );
    });

    const button = container.querySelector(
      "[data-message-load-earlier] button",
    ) as HTMLButtonElement;
    expect(button.disabled).toBe(true);
    expect(button.textContent).toContain("正在加载更早的消息");
  });

  it("does not listen for scrolling without an older page", async () => {
    const onLoadEarlier = vi.fn();
    await act(async () => {
      root = createRoot(container);
      root.render(
        <MessageList
          artifacts={[]}
          earlierLoader={{ hasMore: false, loading: false, onLoadEarlier }}
          isResponding={false}
          messages={messages}
          onSelectArtifact={() => {}}
        />,
      );
    });

    const scroller = scrollContainer();
    await scrollTo(scroller, 0);
    expect(onLoadEarlier).not.toHaveBeenCalled();
  });
});

describe("earlier-entry top hysteresis", () => {
  it("flips only when the position crosses the threshold that matches the current state", () => {
    expect(resolveEarlierEntryAtTop(EARLIER_ENTRY_SHOW_TOP_THRESHOLD, false)).toBe(true);
    expect(resolveEarlierEntryAtTop(EARLIER_ENTRY_SHOW_TOP_THRESHOLD + 1, false)).toBe(false);
    expect(resolveEarlierEntryAtTop(EARLIER_ENTRY_HIDE_TOP_THRESHOLD, true)).toBe(true);
    expect(resolveEarlierEntryAtTop(EARLIER_ENTRY_HIDE_TOP_THRESHOLD + 1, true)).toBe(false);
  });

  it("keeps the hide band strictly outside the show band", () => {
    // 滞回带越宽越稳（入口高度约 32px，保顶补偿量与之同量级）。
    expect(EARLIER_ENTRY_HIDE_TOP_THRESHOLD).toBeGreaterThan(EARLIER_ENTRY_SHOW_TOP_THRESHOLD);
  });
});
