// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { TrajectoryTimeline } from "@/components/workspace/trajectory/trajectory-timeline";
import type {
  TrajectoryItem,
  TrajectoryItemKind,
  TrajectoryItemStatus,
} from "@/lib/trajectory/types";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

function item(index: number, overrides: Partial<TrajectoryItem> = {}): TrajectoryItem {
  return {
    id: `item-${index}`,
    seq: index + 1,
    kind: "assistant",
    causeId: "",
    status: "completed",
    head: { kind: "text", content: `content ${index}` },
    createdAt: index + 1,
    updatedAt: index + 1,
    ...overrides,
  };
}

function click(element: Element | null | undefined) {
  expect(element).toBeInstanceOf(HTMLElement);
  act(() => {
    element?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
}

describe("TrajectoryTimeline", () => {
  let container: HTMLDivElement;
  let root: Root;
  let onJumpToItem: ReturnType<typeof vi.fn<(itemId: string) => void>>;

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    onJumpToItem = vi.fn<(itemId: string) => void>();
  });

  afterEach(() => {
    act(() => {
      root.unmount();
    });
    container.remove();
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = false;
  });

  function render(items: TrajectoryItem[]) {
    act(() => {
      root.render(<TrajectoryTimeline items={items} onJumpToItem={onJumpToItem} />);
    });
  }

  function lane(): HTMLElement {
    const element = container.querySelector<HTMLElement>("[data-axis-kind]");
    expect(element).toBeInstanceOf(HTMLElement);
    return element as HTMLElement;
  }

  function bars(): HTMLElement[] {
    return [...container.querySelectorAll<HTMLElement>('[data-testid="trajectory-timeline-bar"]')];
  }

  function windowOf() {
    const element = lane();
    return {
      start: Number(element.dataset.windowStart),
      end: Number(element.dataset.windowEnd),
      zoomed: element.dataset.zoomed === "true",
      visible: Number(element.dataset.visibleCount),
      total: Number(element.dataset.totalCount),
      axisKind: element.dataset.axisKind,
    };
  }

  function stubRect(element: HTMLElement, width: number, left = 0) {
    element.getBoundingClientRect = () =>
      ({
        left,
        top: 0,
        right: left + width,
        bottom: 36,
        width,
        height: 36,
        x: left,
        y: 0,
        toJSON: () => ({}),
      }) as DOMRect;
  }

  function pointer(type: string, clientX: number) {
    act(() => {
      lane().dispatchEvent(
        new MouseEvent(type, { bubbles: true, cancelable: true, clientX, button: 0 }),
      );
    });
  }

  it("首轨按消息类型着色并输出类型图例计数", () => {
    const kinds: TrajectoryItemKind[] = [
      "user",
      "assistant",
      "tool",
      "reasoning",
      "system",
    ];
    render(
      kinds.map((kind, index) =>
        item(index, { kind, at: 1_700_000_000_000 + index * 1000 }),
      ),
    );

    const rendered = bars();
    expect(rendered.map((bar) => bar.dataset.kind).sort()).toEqual([...kinds].sort());
    // 用户绿 / 助手蓝 / 工具金 / 推理青 / 系统灰：颜色随消息类型，不看状态。
    const colorOf = (kind: TrajectoryItemKind) =>
      rendered.find((bar) => bar.dataset.kind === kind)?.className ?? "";
    expect(colorOf("user")).toContain("bg-[#4ade80]");
    expect(colorOf("assistant")).toContain("bg-[#6ea8fe]");
    expect(colorOf("tool")).toContain("bg-[#f0b429]");
    expect(colorOf("reasoning")).toContain("bg-[#2dd4bf]");
    expect(colorOf("system")).toContain("bg-[#8a8f98]");

    const legend = [
      ...container.querySelectorAll<HTMLElement>(
        '[data-testid="trajectory-timeline-kind-legend-item"]',
      ),
    ];
    expect(
      Object.fromEntries(legend.map((entry) => [entry.dataset.kind, entry.dataset.count])),
    ).toEqual({
      user: "1",
      assistant: "1",
      tool: "1",
      reasoning: "1",
      system: "1",
    });
  });

  it("类型图例可点选：只渲染该类色块并把激活态回报给父级", () => {
    const items = [
      item(0, { kind: "assistant", at: 1_700_000_000_000 }),
      item(1, { kind: "tool", at: 1_700_000_001_000 }),
      item(2, { kind: "assistant", at: 1_700_000_002_000 }),
    ];
    const onToggleKind = vi.fn<(kind: TrajectoryItemKind) => void>();
    act(() => {
      root.render(
        <TrajectoryTimeline
          activeKind="tool"
          items={items}
          onJumpToItem={onJumpToItem}
          onToggleKind={onToggleKind}
        />,
      );
    });

    expect(bars().map((bar) => bar.dataset.kind)).toEqual(["tool"]);
    expect(lane().dataset.activeKind).toBe("tool");

    const entries = [
      ...container.querySelectorAll<HTMLElement>(
        '[data-testid="trajectory-timeline-kind-legend-item"]',
      ),
    ];
    // 图例计数始终覆盖全量条目，便于切回其它类型。
    expect(
      Object.fromEntries(entries.map((entry) => [entry.dataset.kind, entry.dataset.count])),
    ).toEqual({ assistant: "2", tool: "1" });
    expect(
      entries.find((entry) => entry.dataset.kind === "tool")?.getAttribute("aria-pressed"),
    ).toBe("true");

    click(entries.find((entry) => entry.dataset.kind === "assistant"));
    expect(onToggleKind).toHaveBeenCalledWith("assistant");
  });

  it("状态泳道按运行状态着色并输出状态图例计数", () => {
    const statuses: TrajectoryItemStatus[] = [
      "completed",
      "running",
      "failed",
      "canceled",
      "pending",
    ];
    render(
      statuses.map((status, index) =>
        item(index, { status, at: 1_700_000_000_000 + index * 1000 }),
      ),
    );

    // 状态信息落在第二个时间轴（状态泳道），首个时间轴只表达消息类型。
    const marks = [
      ...container.querySelectorAll<HTMLElement>(
        '[data-testid="trajectory-timeline-status-mark"]',
      ),
    ];
    expect(marks.map((mark) => mark.dataset.status).sort()).toEqual(
      [...statuses].sort(),
    );
    expect(
      marks.find((mark) => mark.dataset.status === "failed")?.className,
    ).toContain("bg-[#f87171]");
    expect(
      marks.find((mark) => mark.dataset.status === "running")?.className,
    ).toContain("bg-[#6ea8fe]");
    // 首轨仍是消息类型色（默认 assistant），运行中只加脉冲，不改成状态色。
    const runningBar = bars().find((bar) => bar.dataset.status === "running");
    expect(runningBar?.className).toContain("bg-[#6ea8fe]");
    expect(runningBar?.className).toContain("animate-pulse");

    const legend = [
      ...container.querySelectorAll<HTMLElement>(
        '[data-testid="trajectory-timeline-legend-item"]',
      ),
    ];
    expect(legend.map((entry) => entry.dataset.status)).toEqual([
      "failed",
      "running",
      "canceled",
      "pending",
      "completed",
    ]);
    expect(legend.every((entry) => entry.dataset.count === "1")).toBe(true);
  });

  it("有时间戳的会话用时间轴，缺失时退化为序号轴", () => {
    render([
      item(0, { at: 1_700_000_000_000 }),
      item(1, { at: 1_700_000_002_000 }),
    ]);
    expect(windowOf().axisKind).toBe("time");

    render([item(0), item(1)]);
    expect(windowOf().axisKind).toBe("ordinal");
  });

  it("按钮缩放：放大后窗口变窄、可见条数下降，重置后回到全览", () => {
    const items = Array.from({ length: 40 }, (_, index) =>
      item(index, { at: 1_700_000_000_000 + index * 1000 }),
    );
    render(items);

    const full = windowOf();
    expect(full.zoomed).toBe(false);
    expect(full.visible).toBe(40);

    click(container.querySelector('[data-testid="trajectory-timeline-zoom-in"]'));

    const zoomed = windowOf();
    expect(zoomed.zoomed).toBe(true);
    expect(zoomed.end - zoomed.start).toBeCloseTo((full.end - full.start) / 1.6, 6);
    expect(zoomed.visible).toBeLessThan(40);

    click(container.querySelector('[data-testid="trajectory-timeline-zoom-reset"]'));

    const reset = windowOf();
    expect(reset.zoomed).toBe(false);
    expect(reset.start).toBeCloseTo(full.start, 6);
    expect(reset.end).toBeCloseTo(full.end, 6);
    expect(reset.visible).toBe(40);
  });

  it("时间间隔预设：点选缩放到最近 N（时间轴才露出，序号轴隐藏）", () => {
    const start = 1_700_000_000_000;
    const items = Array.from({ length: 61 }, (_, index) =>
      item(index, { at: start + index * 60_000 }),
    );
    render(items);

    const presets = [
      ...container.querySelectorAll<HTMLElement>(
        '[data-testid="trajectory-timeline-preset"]',
      ),
    ];
    // 全轴 1h：1m/5m/15m 有意义；1h 与全览等价，不渲染（避免空操作）。
    expect(presets.map((preset) => preset.dataset.spanMs)).toEqual([
      "60000",
      "300000",
      "900000",
    ]);
    expect(presets[0]?.getAttribute("aria-pressed")).toBe("false");

    click(presets[1]);

    const zoomed = windowOf();
    expect(zoomed.zoomed).toBe(true);
    expect(zoomed.end).toBeCloseTo(start + 60 * 60_000, 3);
    expect(zoomed.end - zoomed.start).toBeCloseTo(300_000, 3);
    expect(
      container.querySelector<HTMLElement>(
        '[data-testid="trajectory-timeline-preset"][aria-pressed="true"]',
      )?.dataset.spanMs,
    ).toBe("300000");

    // 序号轴（无墙钟时间）没有「时间间隔」语义，预设整行不渲染。
    render([item(0), item(1)]);
    expect(
      container.querySelector('[data-testid="trajectory-timeline-presets"]'),
    ).toBeNull();
  });

  it("滚轮缩放以指针位置为锚点", () => {
    const items = Array.from({ length: 20 }, (_, index) =>
      item(index, { at: 1_700_000_000_000 + index * 1000 }),
    );
    render(items);
    stubRect(lane(), 400);

    const before = windowOf();
    act(() => {
      lane().dispatchEvent(
        new WheelEvent("wheel", {
          deltaY: -300,
          clientX: 400,
          bubbles: true,
          cancelable: true,
        }),
      );
    });

    const after = windowOf();
    expect(after.zoomed).toBe(true);
    expect(after.end).toBeCloseTo(before.end, 3);
    expect(after.start).toBeGreaterThan(before.start);

    // Shift+滚轮：平移而不改变跨度（向左平移，为右端留出空间）。
    const span = after.end - after.start;
    act(() => {
      lane().dispatchEvent(
        new WheelEvent("wheel", {
          deltaY: -120,
          shiftKey: true,
          bubbles: true,
          cancelable: true,
        }),
      );
    });
    const panned = windowOf();
    expect(panned.end - panned.start).toBeCloseTo(span, 6);
    expect(panned.start).toBeLessThan(after.start);
    expect(panned.end).toBeLessThan(after.end);
  });

  it("拖拽框选缩放：两点确定时间区间；轻点不改变缩放", () => {
    const items = Array.from({ length: 10 }, (_, index) =>
      item(index, { at: 1_700_000_000_000 + index * 1000 }),
    );
    render(items);
    const element = lane();
    stubRect(element, 400);
    const before = windowOf();

    pointer("pointerdown", 100);
    pointer("pointermove", 300);
    expect(
      container.querySelector('[data-testid="trajectory-timeline-selection"]'),
    ).not.toBeNull();
    pointer("pointerup", 300);

    const boxed = windowOf();
    const fullSpan = before.end - before.start;
    expect(boxed.start).toBeCloseTo(before.start + fullSpan * 0.25, 3);
    expect(boxed.end).toBeCloseTo(before.start + fullSpan * 0.75, 3);
    expect(
      container.querySelector('[data-testid="trajectory-timeline-selection"]'),
    ).toBeNull();

    // 轻点（无位移）不产生缩放。
    pointer("pointerdown", 150);
    pointer("pointerup", 151);
    const after = windowOf();
    expect(after.start).toBeCloseTo(boxed.start, 6);
    expect(after.end).toBeCloseTo(boxed.end, 6);
  });

  it("点击色块跳转到对应条目（聚合桶跳桶内首条）", () => {
    const items = Array.from({ length: 651 }, (_, index) =>
      item(index, {
        at: 1_700_000_000_000 + index * 100,
        status: index === 400 ? "failed" : "completed",
      }),
    );
    render(items);

    const rendered = bars();
    expect(rendered.length).toBeGreaterThan(0);
    expect(rendered.length).toBeLessThanOrEqual(160);
    // 651 条：全部可见且不产生 651 个 DOM 节点。
    expect(windowOf().visible).toBe(651);
    expect(rendered.some((bar) => bar.dataset.status === "failed")).toBe(true);

    const first = rendered[0];
    click(first);
    expect(onJumpToItem).toHaveBeenCalledTimes(1);
    const target = onJumpToItem.mock.calls[0]?.[0];
    expect(target).toMatch(/^item-\d+$/);
  });

  it("同一时刻到达的多条消息各占一个色块（7 条明细 = 7 块，可分别点选）", () => {
    // 复刻真实会话：一轮收尾时 orchestration / route / tool / observation / result
    // 只相隔几毫秒，按等宽桶聚合会并成一块（明细 7 条、时间轴 3 块）。
    const base = 1_700_000_000_000;
    const items = [
      item(0, {
        id: "msg-system",
        at: base,
        kind: "system",
        head: { kind: "system", note: "session start" },
      }),
      item(1, { id: "msg-assistant", at: base + 7211, kind: "assistant" }),
      item(2, {
        id: "msg-orchestration",
        at: base + 7288,
        kind: "orchestration",
        head: { kind: "structured", payload: {} },
      }),
      item(3, {
        id: "msg-route",
        at: base + 7290,
        kind: "route",
        head: { kind: "structured", payload: {} },
      }),
      item(4, {
        id: "msg-tool",
        at: base + 7293,
        kind: "tool",
        head: { kind: "tool", name: "shell", phase: "finished" },
      }),
      item(5, {
        id: "msg-observation",
        at: base + 7296,
        kind: "observation",
        head: { kind: "structured", payload: {} },
      }),
      item(6, {
        id: "msg-result",
        at: base + 7297,
        kind: "result",
        head: { kind: "structured", payload: {} },
      }),
    ];
    render(items);

    const rendered = bars();
    expect(rendered).toHaveLength(items.length);
    expect(rendered.map((bar) => bar.dataset.kind)).toEqual([
      "system",
      "assistant",
      "orchestration",
      "route",
      "tool",
      "observation",
      "result",
    ]);

    // 堆叠块各自可点：点最后一块跳到最后一条明细，而不是桶内首条。
    click(rendered[rendered.length - 1]);
    expect(onJumpToItem).toHaveBeenCalledWith("msg-result");
  });

  it("工具泳道按状态着色并限制在窗口内", () => {
    const items = Array.from({ length: 30 }, (_, index) =>
      item(index, {
        at: 1_700_000_000_000 + index * 1000,
        kind: index % 2 === 0 ? "tool" : "assistant",
        head:
          index % 2 === 0
            ? { kind: "tool", name: `tool-${index}`, phase: "finished" }
            : { kind: "text", content: `content ${index}` },
        status: index === 10 ? "failed" : "completed",
      }),
    );
    render(items);

    const toolLane = container.querySelector('[data-testid="trajectory-timeline-tool-lane"]');
    expect(toolLane).not.toBeNull();
    const marks = [
      ...(toolLane as HTMLElement).querySelectorAll<HTMLElement>("button"),
    ];
    expect(marks).toHaveLength(15);
    expect(marks.filter((mark) => mark.dataset.status === "failed")).toHaveLength(1);

    click(container.querySelector('[data-testid="trajectory-timeline-zoom-in"]'));
    const visibleMarks = [
      ...(toolLane as HTMLElement).querySelectorAll<HTMLElement>("button"),
    ];
    expect(visibleMarks.length).toBeLessThan(15);
  });

  it("三条泳道按组分隔：主泳道 / 状态泳道 / 工具泳道之间有发丝线，无工具时整组隐藏", () => {
    render([
      item(0, { at: 1_700_000_000_000, kind: "assistant" }),
      item(1, {
        at: 1_700_000_001_000,
        kind: "tool",
        head: { kind: "tool", name: "bash", phase: "finished" },
      }),
    ]);

    // 时间轴根的直接子节点顺序 = 三条泳道的分区顺序（主泳道 → 状态组 → 工具组）。
    const root = container.querySelector<HTMLElement>('[data-testid="trajectory-timeline"]');
    expect(root).toBeInstanceOf(HTMLElement);
    expect(
      [...(root as HTMLElement).children]
        .map((child) => child.getAttribute("data-testid"))
        .filter((testId): testId is string => Boolean(testId)),
    ).toEqual([
      "trajectory-timeline-ticks",
      "trajectory-timeline-kind-legend",
      "trajectory-timeline-divider",
      "trajectory-timeline-status-lane",
      "trajectory-timeline-legend",
      "trajectory-timeline-divider",
      "trajectory-timeline-tool-lane",
    ]);
    const dividers = container.querySelectorAll('[data-testid="trajectory-timeline-divider"]');
    expect(dividers).toHaveLength(2);
    // 分隔线是纯装饰：不进入无障碍树。
    expect(
      [...dividers].every((divider) => divider.getAttribute("aria-hidden") === "true"),
    ).toBe(true);

    render([item(0, { at: 1_700_000_000_000, kind: "assistant" })]);
    expect(container.querySelectorAll('[data-testid="trajectory-timeline-divider"]')).toHaveLength(1);
    expect(container.querySelector('[data-testid="trajectory-timeline-tool-lane"]')).toBeNull();
  });

  it("无条目时不渲染", () => {
    render([]);
    expect(container.querySelector('[data-testid="trajectory-timeline"]')).toBeNull();
  });
});
