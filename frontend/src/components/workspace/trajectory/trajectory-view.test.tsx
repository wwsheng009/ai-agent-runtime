// @vitest-environment jsdom

import { act, type ComponentProps } from "react";
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

  function renderView(props: Partial<ComponentProps<typeof TrajectoryView>> = {}) {
    act(() => {
      root.render(<TrajectoryView store={store} {...props} />);
    });
  }

  it("空快照显示空状态提示", () => {
    renderView();
    expect(container.textContent).toContain("暂无轨迹事件");
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

    const input = container.querySelector('input[aria-label="搜索轨迹"]');
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
    expect(container.querySelector('[aria-label="关闭轨迹详情"]')).toBeInstanceOf(
      HTMLButtonElement,
    );
    expect(container.textContent).toContain("运行中");
  });

  it("时间线色块点击选中对应行", () => {
    renderView();
    pushEvent(store, "chunk", 1, { type: "text", content: "first" });
    pushEvent(store, "tool_start", 2, {
      type: "tool_call",
      tool_call: { id: "c1", name: "read_file" },
    });

    const timelineBlock = container.querySelector('button[aria-label="工具 2"]');
    click(timelineBlock);
    expect(container.textContent).toContain("read_file");
    expect(container.querySelector('[aria-label="关闭轨迹详情"]')).toBeInstanceOf(
      HTMLButtonElement,
    );
  });

  it("时间线缩放后明细列表同步过滤，清除区间筛选后恢复", () => {
    renderView();
    pushEvent(store, "chunk", 1, { type: "text", content: "opening" });
    // 每次工具调用是独立条目（相邻流式分片会并成一行，这里需要足够的条目跨度）。
    for (let index = 2; index <= 9; index += 1) {
      pushEvent(store, "tool_start", index, {
        type: "tool_call",
        tool_call: { id: `call-${index}`, name: `tool-${index}` },
      });
    }
    const rowCount = () =>
      Number(
        container.querySelector<HTMLElement>('[data-trajectory-list="true"]')?.dataset
          .rowCount,
      );
    const before = rowCount();
    expect(before).toBeGreaterThanOrEqual(8);
    expect(container.querySelector('[data-testid="trajectory-timeline-filter"]')).toBeNull();

    click(container.querySelector('[data-testid="trajectory-timeline-zoom-in"]'));

    // 图上缩放 → 列表只剩窗口内的条目，并显示筛选摘要 + 清除入口。
    const zoomed = rowCount();
    expect(zoomed).toBeGreaterThan(0);
    expect(zoomed).toBeLessThan(before);
    expect(
      container.querySelector('[data-testid="trajectory-timeline-filter-summary"]')
        ?.textContent,
    ).toContain(`${zoomed}/${before}`);

    click(container.querySelector('[data-testid="trajectory-timeline-filter-window"]'));

    expect(rowCount()).toBe(before);
    expect(container.querySelector('[data-testid="trajectory-timeline-filter"]')).toBeNull();
  });

  it("图例类型筛选与列表联动，清除后恢复全部条目", () => {
    renderView();
    pushEvent(store, "chunk", 1, { type: "text", content: "answer" });
    pushEvent(store, "tool_start", 2, {
      type: "tool_call",
      tool_call: { id: "call-1", name: "bash" },
    });
    const rowCount = () =>
      Number(
        container.querySelector<HTMLElement>('[data-trajectory-list="true"]')?.dataset
          .rowCount,
      );
    expect(rowCount()).toBe(2);

    click(
      [...container.querySelectorAll<HTMLElement>(
        '[data-testid="trajectory-timeline-kind-legend-item"]',
      )].find((entry) => entry.dataset.kind === "tool"),
    );

    expect(rowCount()).toBe(1);
    expect(container.textContent).toContain("bash");
    expect(container.textContent).not.toContain("answer");
    expect(
      container.querySelector('[data-testid="trajectory-timeline-filter-kind"]')
        ?.textContent,
    ).toContain("工具");

    click(container.querySelector('[data-testid="trajectory-timeline-filter-kind"]'));

    expect(rowCount()).toBe(2);
    expect(container.textContent).toContain("answer");
    expect(container.querySelector('[data-testid="trajectory-timeline-filter"]')).toBeNull();
  });

  it("store.reset() 后回到空状态", () => {
    renderView();
    pushEvent(store, "chunk", 1, { type: "text", content: "hello" });
    expect(container.textContent).toContain("#1");

    act(() => store.reset());
    expect(container.textContent).not.toContain("#1");
    expect(container.textContent).toContain("暂无轨迹事件");
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
    expect(container.textContent).toContain("暂无轨迹事件");

    // 切换到会话 B：恢复路径从 seq=1 重新推送。
    pushEvent(store, "chunk", 1, { type: "text", content: "session b" });
    expect(container.textContent).toContain("#1");
    expect(container.textContent).toContain("session b");
  });

  it("筛选无匹配时显示提示", () => {
    renderView();
    pushEvent(store, "chunk", 1, { type: "text", content: "answer" });

    const toolsButton = [...container.querySelectorAll("button")].find((button) =>
      button.textContent?.trim().startsWith("工具"),
    );
    click(toolsButton);
    expect(container.textContent).toContain("没有匹配当前筛选条件的行。");
  });

  it("时间轴区域与消息列表分区：时间轴有高度上限且自身滚动，列表独占剩余高度", () => {
    renderView();
    pushEvent(store, "chunk", 1, { type: "text", content: "answer" });

    const region = container.querySelector<HTMLElement>(
      '[data-testid="trajectory-timeline-region"]',
    );
    const timeline = container.querySelector('[data-testid="trajectory-timeline"]');
    const listRegion = container.querySelector<HTMLElement>(
      '[data-testid="trajectory-list-region"]',
    );
    const list = container.querySelector("[data-trajectory-list]");

    expect(region).toBeInstanceOf(HTMLElement);
    expect(listRegion).toBeInstanceOf(HTMLElement);
    // 时间轴被包进独立区域（可与列表分栏/滚动隔离），不再是列表的兄弟节点。
    expect(region?.contains(timeline)).toBe(true);
    expect(region?.contains(list)).toBe(false);
    expect(listRegion?.contains(list)).toBe(true);
    // 关键不变量：时间轴区域不许长高到挤占列表 —— 高度封顶 + 自身滚动 + 不参与收缩。
    expect(region?.className).toContain("max-h-[40%]");
    expect(region?.className).toContain("shrink-0");
    expect(region?.className).toContain("overflow-y-auto");
    expect(region?.className).toContain("overscroll-contain");
    // 列表区域保持 flex-1 + min-h-0：唯一随窗口长高的滚动区。
    expect(listRegion?.className).toContain("flex-1");
    expect(listRegion?.className).toContain("min-h-0");
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

  it("尾部优先：还有更早内容时顶部显示「加载更早」入口，点击向前翻页", () => {
    const onLoadEarlier = vi.fn();
    renderView({ hasEarlier: true, onLoadEarlier });
    pushEvent(store, "chunk", 1, { type: "text", content: "最近的消息" });

    const entry = container.querySelector("[data-trajectory-load-earlier]");
    expect(entry).toBeInstanceOf(HTMLElement);
    expect(container.textContent).toContain("加载更早的轨迹");

    click(entry?.querySelector("button"));
    expect(onLoadEarlier).toHaveBeenCalledTimes(1);
  });

  it("尾部优先：已到日志开头不渲染入口；加载中禁用并提示", () => {
    renderView({ hasEarlier: false });
    pushEvent(store, "chunk", 1, { type: "text", content: "唯一一页" });
    expect(container.querySelector("[data-trajectory-load-earlier]")).toBeNull();

    renderView({ hasEarlier: true, loadingEarlier: true });
    const button = container.querySelector("[data-trajectory-load-earlier] button");
    expect(button).toBeInstanceOf(HTMLButtonElement);
    expect((button as HTMLButtonElement | null)?.disabled).toBe(true);
    expect(container.textContent).toContain("正在加载更早的轨迹");
  });

  it("尾部优先：滚到列表顶端自动续页（在途时幂等不重复触发）", () => {
    const onLoadEarlier = vi.fn();
    renderView({ hasEarlier: true, onLoadEarlier });
    pushEvent(store, "chunk", 1, { type: "text", content: "最近的消息" });

    const list = container.querySelector("[data-trajectory-list]");
    expect(list).toBeInstanceOf(HTMLElement);
    if (!(list instanceof HTMLElement)) {
      return;
    }
    list.scrollTop = 0;
    act(() => {
      list.dispatchEvent(new Event("scroll", { bubbles: true }));
    });
    expect(onLoadEarlier).toHaveBeenCalledTimes(1);

    // 在途（loadingEarlier）时同一条入口短路：不重复翻页。
    renderView({ hasEarlier: true, loadingEarlier: true, onLoadEarlier });
    list.scrollTop = 0;
    act(() => {
      list.dispatchEvent(new Event("scroll", { bubbles: true }));
    });
    expect(onLoadEarlier).toHaveBeenCalledTimes(1);
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
