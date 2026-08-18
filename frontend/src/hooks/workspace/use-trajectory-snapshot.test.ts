import { describe, expect, it, vi } from "vitest";

import { createTrajectoryStore } from "./use-trajectory-snapshot";

describe("createTrajectoryStore（快照订阅 store）", () => {
  it("初始快照为空轨迹", () => {
    const store = createTrajectoryStore();
    expect(store.getSnapshot().items).toEqual([]);
    store.dispose();
  });

  it("push + flush 应用事件到快照（事件入批处理，flush 后可见）", () => {
    const store = createTrajectoryStore();
    store.push("reasoning", { content: "thinking" });
    store.push("chunk", { type: "text", content: "hi" });
    store.flush();
    const items = store.getSnapshot().items;
    expect(items.map((item) => item.head.kind)).toEqual(["reasoning", "text"]);
    store.dispose();
  });

  it("subscribe 在 flush 时收到通知", () => {
    const store = createTrajectoryStore();
    const listener = vi.fn();
    store.subscribe(listener);
    store.push("chunk", { type: "text", content: "a" });
    expect(listener).not.toHaveBeenCalled();
    store.flush();
    expect(listener).toHaveBeenCalledTimes(1);
    store.dispose();
  });

  it("unsubscribe 后不再通知", () => {
    const store = createTrajectoryStore();
    const listener = vi.fn();
    const unsubscribe = store.subscribe(listener);
    unsubscribe();
    store.push("chunk", { type: "text", content: "a" });
    store.flush();
    expect(listener).not.toHaveBeenCalled();
    store.dispose();
  });

  it("payload 为 null/undefined 时安全（seq 降级为到达序）", () => {
    const store = createTrajectoryStore();
    store.push("planning", undefined);
    store.push("orchestration", null);
    store.flush();
    expect(store.getSnapshot().items.map((item) => item.head.kind)).toEqual([
      "structured",
      "structured",
    ]);
    store.dispose();
  });

  it("乱序事件经 reducer 缓冲后仍按 seq 排序", () => {
    const store = createTrajectoryStore();
    store.push("chunk", { type: "text", content: "c", _event: { sequence: 3 } });
    store.push("chunk", { type: "text", content: "a", _event: { sequence: 1 } });
    store.flush();
    store.push("chunk", { type: "text", content: "b", _event: { sequence: 2 } });
    store.flush();
    const items = store.getSnapshot().items;
    expect(items[0].head.kind).toBe("text");
    if (items[0].head.kind === "text") {
      expect(items[0].head.content).toBe("abc");
    }
    store.dispose();
  });

  it("dispose 幂等（重复调用不抛错，之后 push 仍安全）", () => {
    const store = createTrajectoryStore();
    store.dispose();
    store.dispose();
    store.push("chunk", { type: "text", content: "x" });
    store.flush();
    expect(store.getSnapshot().items.length).toBe(1);
  });
});
