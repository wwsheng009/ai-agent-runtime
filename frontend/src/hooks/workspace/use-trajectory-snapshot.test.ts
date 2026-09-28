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

  it("回放日志被上限裁剪后标记 truncated，只有硬重置才清除", () => {
    const store = createTrajectoryStore({ fallbackDelayMs: 0 });
    expect(store.isReplayLogTruncated()).toBe(false);
    // 上限 8000：第 8001 条动作触发裁剪（丢弃最老动作，无法再忠实重建投影）。
    for (let index = 0; index < 8001; index += 1) {
      store.push("chunk", { type: "text", content: `t${index}` });
    }
    expect(store.isReplayLogTruncated()).toBe(true);
    // 软重置（保留续传游标）不清除：更老动作依然不在日志里。
    store.reset();
    expect(store.isReplayLogTruncated()).toBe(true);
    // 硬重置（会话/线程切换）清空日志与标记。
    store.reset({ hard: true });
    expect(store.isReplayLogTruncated()).toBe(false);
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

  it("双通道相同 provider delta 只进入轨迹一次", () => {
    const store = createTrajectoryStore();
    // The chat SSE and runtime stream have different durable EventStore seq
    // values, but share the provider stream identity.
    store.push("chunk", {
      type: "text",
      content: "a",
      stream_id: "stream-1",
      sequence: 1,
      _event: { sequence: 1 },
    });
    store.push("chunk", {
      type: "text",
      content: "a",
      stream_id: "stream-1",
      sequence: 1,
      _event: { sequence: 2 },
    });
    store.push("chunk", {
      type: "text",
      content: "b",
      stream_id: "stream-1",
      sequence: 2,
      _event: { sequence: 3 },
    });
    store.flush();
    const text = store.getSnapshot().items.find((item) => item.id === "assistant");
    expect(text?.head.kind).toBe("text");
    if (text?.head.kind === "text") {
      expect(text.head.content).toBe("ab");
    }
    expect(store.getSnapshot().lastEventSeq).toBe(3);
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

  it("读路径兜底：getSnapshot 同步兑现挂起批次，flush 时补发一次通知（P0-2）", () => {
    const store = createTrajectoryStore();
    const listener = vi.fn();
    store.subscribe(listener);
    store.push("chunk", { type: "text", content: "a", _event: { sequence: 1 } });
    expect(listener).not.toHaveBeenCalled();

    // 读路径（ensureFresh）：挂起批次同步应用，读到最新投影；渲染期不发布。
    expect(store.getSnapshot().items.length).toBe(1);
    expect(listener).not.toHaveBeenCalled();

    // 下一次 flush 补发一次通知（不重复应用）。
    store.flush();
    expect(listener).toHaveBeenCalledTimes(1);
    store.dispose();
  });

  it("无落点批次不发布：过期 seq 事件不改引用、不通知（P0-2）", () => {
    const store = createTrajectoryStore();
    const listener = vi.fn();
    store.subscribe(listener);
    store.push("chunk", { type: "text", content: "a", _event: { sequence: 1 } });
    store.flush();
    expect(listener).toHaveBeenCalledTimes(1);
    const stable = store.getSnapshot();

    // 过期 seq（重复投递）：reducer 幂等跳过 → 无落点 → 不发布、引用保持稳定。
    store.push("chunk", { type: "text", content: "b", _event: { sequence: 1 } });
    store.flush();
    expect(listener).toHaveBeenCalledTimes(1);
    expect(store.getSnapshot()).toBe(stable);
    store.dispose();
  });

  it("无订阅者时 advanceCursor 仍先兑现挂起批次（强制同步点，P0-2）", () => {
    const store = createTrajectoryStore();
    // 无订阅者：非强制冲刷被惰性闸门拦截，挂起批次保留（不 rebuild）。
    store.push("chunk", { type: "text", content: "a", _event: { sequence: 1 } });
    store.advanceCursor(5);
    // 强制同步点先应用 seq=1，再推进游标——事件不会被推进后的游标判为过期。
    const snapshot = store.getSnapshot();
    expect(snapshot.items.length).toBe(1);
    expect(snapshot.lastEventSeq).toBe(5);
    store.dispose();
  });
});
