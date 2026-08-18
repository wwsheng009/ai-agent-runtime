// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { TrajectoryView } from "@/components/workspace/trajectory/trajectory-view";
import { createTrajectoryStore, type TrajectoryStore } from "@/hooks/workspace/use-trajectory-snapshot";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

function pushEvent(
  store: TrajectoryStore,
  kind: Parameters<TrajectoryStore["push"]>[0],
  seq: number,
  payload: Record<string, unknown> = {},
) {
  act(() => {
    store.push(kind, { ...payload, _event: { sequence: seq } });
    store.flush();
  });
}

function click(element: Element | null | undefined) {
  expect(element).toBeInstanceOf(HTMLElement);
  act(() => {
    element?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
}

describe("TrajectoryView", () => {
  let container: HTMLDivElement;
  let root: Root;
  let store: TrajectoryStore;

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
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
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = false;
  });

  function renderView() {
    act(() => {
      root.render(<TrajectoryView store={store} />);
    });
  }

  it("空快照显示空状态提示", () => {
    renderView();
    expect(container.textContent).toContain("No trajectory events yet");
  });

  it("流式事件逐条出现在明细列表（seq + 摘要）", () => {
    renderView();
    pushEvent(store, "chunk", 1, { type: "text", content: "hello" });
    expect(container.textContent).toContain("#1");
    expect(container.textContent).toContain("hello");

    pushEvent(store, "reasoning", 2, { content: "thinking…" });
    expect(container.textContent).toContain("#2");
    expect(container.textContent).toContain("thinking…");
  });

  it("工具筛选只显示工具行", () => {
    renderView();
    pushEvent(store, "chunk", 1, { type: "text", content: "answer" });
    pushEvent(store, "tool_start", 2, {
      type: "tool_call",
      tool_call: { id: "call-1", name: "bash" },
    });
    pushEvent(store, "tool_end", 3, {
      type: "tool_call",
      tool_call: { id: "call-1", name: "bash" },
      tool: { output_summary: "src" },
    });

    const toolsButton = container.querySelector('[aria-pressed="false"]');
    click(toolsButton);
    expect(container.textContent).not.toContain("answer");
    expect(container.textContent).toContain("bash");
  });

  it("搜索输入框存在（过滤逻辑由纯函数单测覆盖；真实输入交互由 e2e 覆盖）", () => {
    renderView();
    pushEvent(store, "chunk", 1, { type: "text", content: "implement parser" });
    pushEvent(store, "reasoning", 2, { content: "unrelated note" });

    const input = container.querySelector('input[aria-label="Search trajectory"]');
    expect(input).toBeInstanceOf(HTMLInputElement);
    expect(container.textContent).toContain("#1");
    expect(container.textContent).toContain("#2");
  });

  it("点击明细行打开详情面板（共用同一 Item 对象）", () => {
    renderView();
    pushEvent(store, "chunk", 1, { type: "text", content: "detail body" });

    const rows = [...container.querySelectorAll("button")].filter((button) =>
      button.textContent?.includes("detail body"),
    );
    click(rows[0]);
    expect(container.querySelector('[aria-label="Close trajectory detail"]')).toBeInstanceOf(
      HTMLButtonElement,
    );
    expect(container.textContent).toContain("running");
  });

  it("时间线色块点击选中对应行", () => {
    renderView();
    pushEvent(store, "chunk", 1, { type: "text", content: "first" });
    pushEvent(store, "tool_start", 2, {
      type: "tool_call",
      tool_call: { id: "c1", name: "read_file" },
    });

    const timelineBlock = container.querySelector(
      'button[aria-label*="tool 2"], button[aria-label^="tool "]',
    );
    click(timelineBlock);
    expect(container.textContent).toContain("read_file");
    expect(container.querySelector('[aria-label="Close trajectory detail"]')).toBeInstanceOf(
      HTMLButtonElement,
    );
  });

  it("store.reset() 后回到空状态", () => {
    renderView();
    pushEvent(store, "chunk", 1, { type: "text", content: "hello" });
    expect(container.textContent).toContain("#1");

    act(() => store.reset());
    expect(container.textContent).not.toContain("#1");
    expect(container.textContent).toContain("No trajectory events yet");
  });

  it("软重置保留续传游标：下一个 turn 从 session 全局 seq 续传可渲染（回归：只有 system 行的问题）", () => {
    renderView();
    // 恢复路径回放既有事件（session 持久化 seq 从 1 开始）。
    pushEvent(store, "chunk", 1, { type: "text", content: "old turn" });
    pushEvent(store, "tool_start", 2, {
      type: "tool_call",
      tool_call: { id: "call-a", name: "bash" },
    });
    pushEvent(store, "tool_end", 3, {
      type: "tool_call",
      tool_call: { id: "call-a", name: "bash" },
    });
    expect(container.textContent).toContain("old turn");

    // 新 turn 开始：软重置清空旧行，但保留 lastEventSeq=3。
    act(() => store.reset());
    expect(container.textContent).not.toContain("old turn");

    // 新 turn 的实时事件 seq 继续（4、5），而不是从 1 重新开始。
    pushEvent(store, "reasoning", 4, { content: "planning…" });
    pushEvent(store, "chunk", 5, { type: "text", content: "new turn answer" });

    expect(container.textContent).toContain("#4");
    expect(container.textContent).toContain("planning…");
    expect(container.textContent).toContain("#5");
    expect(container.textContent).toContain("new turn answer");
  });

  it("硬重置清空游标：新会话回放可从 seq=1 重新建链", () => {
    renderView();
    pushEvent(store, "chunk", 1, { type: "text", content: "session a" });
    pushEvent(store, "chunk", 2, { type: "text", content: "session a 2" });

    act(() => store.reset({ hard: true }));
    expect(container.textContent).toContain("No trajectory events yet");

    // 切换到会话 B：恢复路径从 seq=1 重新推送。
    pushEvent(store, "chunk", 1, { type: "text", content: "session b" });
    expect(container.textContent).toContain("#1");
    expect(container.textContent).toContain("session b");
  });

  it("筛选无匹配时显示提示", () => {
    renderView();
    pushEvent(store, "chunk", 1, { type: "text", content: "answer" });

    const toolsButton = [...container.querySelectorAll("button")].find((button) =>
      button.textContent?.trim().startsWith("Tools"),
    );
    click(toolsButton);
    expect(container.textContent).toContain("No rows match");
  });

  it("恢复路径跳过被过滤事件空洞后，后续事件可渲染（回归：tool_started/tool_finished 占 seq 导致只剩 system 行）", () => {
    renderView();
    // 恢复回放：chat.sse 1、2 渲染；seq 3 是被过滤的 tool_started；
    // chat.sse 4 若无空洞处理将永久 pending → 只有 1、2 可见。
    pushEvent(store, "chunk", 1, { type: "text", content: "old turn" });
    pushEvent(store, "chunk", 2, { type: "text", content: "old turn 2" });
    pushEvent(store, "chunk", 4, { type: "text", content: "after gap" });
    expect(container.textContent).not.toContain("after gap"); // pending，未渲染
    expect(store.getSnapshot().lastEventSeq).toBe(2);
    expect(store.getSnapshot().pending[4]).toBeDefined();

    // 恢复链路对被过滤的 seq=3 事件调用 advanceCursor → 续接 4。
    act(() => store.advanceCursor(3));
    expect(store.getSnapshot().lastEventSeq).toBe(4);
    expect(store.getSnapshot().pending).toEqual({});
    expect(container.textContent).toContain("after gap");

    // 新 turn 软重置后，实时事件（全局续号 5、6）正常渲染。
    act(() => store.reset());
    pushEvent(store, "reasoning", 5, { content: "new plan" });
    pushEvent(store, "chunk", 6, { type: "text", content: "new answer" });
    expect(container.textContent).toContain("#5");
    expect(container.textContent).toContain("#6");
  });
});

// jsdom 无 ResizeObserver：stub 并提供容器高度，让虚拟滚动窗口化生效。
beforeEach(() => {
  vi.stubGlobal(
    "ResizeObserver",
    class {
      private callback: ResizeObserverCallback;
      constructor(callback: ResizeObserverCallback) {
        this.callback = callback;
      }
      observe(element: Element) {
        Object.defineProperty(element, "clientHeight", {
          configurable: true,
          value: 600,
        });
        this.callback([], this as unknown as ResizeObserver);
      }
      unobserve() {}
      disconnect() {}
    },
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});
