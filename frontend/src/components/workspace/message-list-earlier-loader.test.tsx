/**
 * 对话面「加载更早」入口（用户诉求：入口必须看得见、点得动、滚到顶端能自动续页）。
 * 断言渲染 + 交互 + 触顶阈值三件事，覆盖「按钮存在但不工作」这类接线断点。
 */
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { ChatMessage } from "@/data/mock";

import { MessageList } from "./message-list";

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

  it("renders the load-earlier button when the backend reports an older page", async () => {
    await renderList(messages, true);
    const row = container.querySelector("[data-message-load-earlier]");
    expect(row).not.toBeNull();
    expect(row?.querySelector("button")?.textContent).toContain("加载更早");
  });

  it("pins the entry to the top of the scrollport so it stays visible while reading the tail", async () => {
    // 回归点：入口若只是「流内首行」，一页 100 条约 1.2 万 px，用户停在底部时它在
    // 视口上方 7,600+px（实测），表现为「页面上根本没有加载更早按钮」。
    await renderList(messages, true);
    const row = container.querySelector<HTMLElement>("[data-message-load-earlier]");
    expect(row?.className).toContain("sticky");
    expect(row?.className).toContain("top-0");
  });

  it("renders nothing when there is no older page left", async () => {
    await renderList(messages, false);
    expect(container.querySelector("[data-message-load-earlier]")).toBeNull();
  });

  it("keeps the entry visible even when the current page has no renderable rows", async () => {
    // 极端形态：最新一页解析后没有可见行，但更早还有内容——入口不能跟着消失。
    await renderList([], true);
    expect(container.querySelector("[data-message-load-earlier]")).not.toBeNull();
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
    // jsdom 不实现真实滚动：按用例覆写 scrollTop 模拟「在中部」与「在顶端」。
    Object.defineProperty(scroller, "scrollTop", { configurable: true, value: 320, writable: true });
    await act(async () => {
      scroller.dispatchEvent(new Event("scroll"));
    });
    expect(onLoadEarlier).not.toHaveBeenCalled();

    // 阈值内（64px）也算触顶：快速上滑不易整段跨过判定窗口。
    Object.defineProperty(scroller, "scrollTop", { configurable: true, value: 48, writable: true });
    await act(async () => {
      scroller.dispatchEvent(new Event("scroll"));
    });
    expect(onLoadEarlier).toHaveBeenCalledTimes(1);

    Object.defineProperty(scroller, "scrollTop", { configurable: true, value: 0, writable: true });
    await act(async () => {
      scroller.dispatchEvent(new Event("scroll"));
    });
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
    Object.defineProperty(scroller, "scrollTop", { configurable: true, value: 0, writable: true });
    await act(async () => {
      scroller.dispatchEvent(new Event("scroll"));
    });
    expect(onLoadEarlier).not.toHaveBeenCalled();
  });
});
