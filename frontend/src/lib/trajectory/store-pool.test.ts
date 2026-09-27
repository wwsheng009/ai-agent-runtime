import { describe, expect, it, vi } from "vitest";

import { createEmptyTrajectory } from "@/lib/trajectory/types";
import {
  ANONYMOUS_TRAJECTORY_STORE_KEY,
  createTrajectoryStorePool,
  DEFAULT_TRAJECTORY_STORE_POOL_SIZE,
} from "./store-pool";
import type { TrajectoryStore } from "@/hooks/workspace/use-trajectory-snapshot";

type FakeStore = TrajectoryStore & {
  disposed: boolean;
  resetCalls: Array<{ hard?: boolean } | undefined>;
};

function createFakeStore(): FakeStore {
  const resetCalls: Array<{ hard?: boolean } | undefined> = [];
  const store = {
    disposed: false,
    resetCalls,
    getSnapshot: () => createEmptyTrajectory(),
    push: vi.fn(),
    flush: vi.fn(),
    advanceCursor: vi.fn(),
    reset: vi.fn((options?: { hard?: boolean }) => {
      resetCalls.push(options);
    }),
    subscribe: vi.fn(() => () => {}),
    dispose: vi.fn(() => {
      store.disposed = true;
    }),
    setBaselineSeq: vi.fn(),
    prependEarlier: vi.fn(),
    isReplayLogTruncated: () => false,
  } as unknown as FakeStore;
  return store;
}

function createFakeFactory() {
  const created: FakeStore[] = [];
  const createStore = () => {
    const store = createFakeStore();
    created.push(store);
    return store;
  };
  return { created, createStore };
}

describe("createTrajectoryStorePool（轨迹 store 池）", () => {
  it("同键复用同一实例，异键隔离", () => {
    const { created, createStore } = createFakeFactory();
    const pool = createTrajectoryStorePool({ createStore });

    const first = pool.acquire("session-a");
    expect(pool.acquire("session-a")).toBe(first);
    const second = pool.acquire("session-b");
    expect(second).not.toBe(first);
    expect(created).toHaveLength(2);
    expect(pool.residentKeys()).toEqual(["session-a", "session-b"]);
  });

  it("超过容量按 LRU 驱逐并 dispose 最久未用者", () => {
    const { createStore } = createFakeFactory();
    const pool = createTrajectoryStorePool({ maxStores: 2, createStore });

    const a = pool.acquire("a") as FakeStore;
    const b = pool.acquire("b") as FakeStore;
    expect(pool.residentKeys()).toEqual(["a", "b"]);

    const c = pool.acquire("c") as FakeStore;
    expect(a.disposed).toBe(true);
    expect(b.disposed).toBe(false);
    expect(pool.peek("a")).toBeUndefined();
    expect(pool.residentKeys()).toEqual(["b", "c"]);

    // 被驱逐的键再次 acquire → 新实例（冷启动），并把自己变成 MRU，
    // 于是此刻最久未用的 b 被驱逐（LRU 顺序变为 [c, a2]）。
    const a2 = pool.acquire("a");
    expect(a2).not.toBe(a);
    expect(b.disposed).toBe(true);
    expect(c.disposed).toBe(false);
    expect(pool.residentKeys()).toEqual(["c", "a"]);
  });

  it("acquire 命中也算一次使用（touch 改变驱逐对象）", () => {
    const { createStore } = createFakeFactory();
    const pool = createTrajectoryStorePool({ maxStores: 2, createStore });

    const a = pool.acquire("a") as FakeStore;
    const b = pool.acquire("b") as FakeStore;
    // 重新访问 a：LRU 顺序应变为 [b, a]。
    expect(pool.acquire("a")).toBe(a);
    pool.acquire("c");
    expect(b.disposed).toBe(true);
    expect(a.disposed).toBe(false);
  });

  it("maxStores = 1 时退化为旧单实例行为", () => {
    const { createStore } = createFakeFactory();
    const pool = createTrajectoryStorePool({ maxStores: 1, createStore });

    expect(pool.acquire("a")).toBe(pool.acquire("a"));
    pool.acquire("b");
    expect(pool.residentKeys()).toEqual(["b"]);
  });

  it("adopt 迁移键并保留快照；目标键已占用时不迁移", () => {
    const pool = createTrajectoryStorePool({ maxStores: 3 });
    const store = pool.acquire("draft-thread");
    store.advanceCursor(7);
    store.flush();

    expect(pool.adopt("draft-thread", "session-1")).toBe(true);
    expect(pool.peek("draft-thread")).toBeUndefined();
    const moved = pool.peek("session-1");
    expect(moved).toBe(store);
    // 快照（含续传游标）原样保留：切回/迁移后不走全量重放。
    expect(moved?.getSnapshot().lastEventSeq).toBe(7);

    const other = pool.acquire("session-2");
    expect(pool.adopt("session-1", "session-2")).toBe(false);
    expect(pool.peek("session-1")).toBe(store);
    expect(pool.peek("session-2")).toBe(other);
    // 同键迁移是幂等空操作。
    expect(pool.adopt("session-1", "session-1")).toBe(true);
  });

  it("acquireForThread：同线程键升级先迁移后取，保同一实例", () => {
    const pool = createTrajectoryStorePool({ maxStores: 3 });
    const draftStore = pool.acquireForThread("thread-draft", "thread-draft");
    draftStore.advanceCursor(5);
    draftStore.flush();

    // 草稿线程落库：threadId 不变、键升级为服务端 sessionId。
    const upgraded = pool.acquireForThread("thread-draft", "session-1");
    expect(upgraded).toBe(draftStore);
    expect(upgraded.getSnapshot().lastEventSeq).toBe(5);
    expect(pool.peek("thread-draft")).toBeUndefined();
    expect(pool.peek("session-1")).toBe(draftStore);
  });

  it("acquireForThread：不同线程各用各的 store，目标键被占用时不迁移", () => {
    const pool = createTrajectoryStorePool({ maxStores: 3 });
    const first = pool.acquireForThread("thread-a", "session-a");
    const second = pool.acquireForThread("thread-b", "session-b");
    expect(second).not.toBe(first);

    // 目标键已被占用（真切换/重放）：adopt 被拒，取到目标键自己的 store。
    expect(pool.acquireForThread("thread-b", "session-a")).toBe(first);
    expect(pool.peek("session-b")).toBe(second);
  });

  it("acquireForThread：新会话首轮——回合已按线程键建 store 时补一次迁移", () => {
    const pool = createTrajectoryStorePool({ maxStores: 3 });
    // 回合 bootstrap：服务端 sessionId 尚未落库，只能按当时的线程 id 解析并登记
    // 线程身份（`acquireForThread`）；store 从这里开始收 SSE 增量。
    const turnStore = pool.acquireForThread("thread-new-chat", "thread-new-chat");
    turnStore.advanceCursor(3);
    turnStore.flush();

    // 页面随后解析同一线程（此时已带 sessionId）：按登记的身份先迁移再取，
    // 否则视图切到空快照、回合增量成孤儿（轨迹只剩流早期的一两行）。
    const resolved = pool.acquireForThread("thread-new-chat", "session-1");
    expect(resolved).toBe(turnStore);
    expect(resolved.getSnapshot().lastEventSeq).toBe(3);
    expect(pool.peek("thread-new-chat")).toBeUndefined();
    expect(pool.peek("session-1")).toBe(turnStore);

    // 之后再解析（同线程、同键）仍是同一实例。
    expect(pool.acquireForThread("thread-new-chat", "session-1")).toBe(turnStore);
  });

  it("acquireForThread：首次解析时目标键已占用则不迁移（真切换语义不变）", () => {
    const pool = createTrajectoryStorePool({ maxStores: 3 });
    const occupied = pool.acquire("session-1");
    const turnStore = pool.acquire("thread-other");

    expect(pool.acquireForThread("thread-other", "session-1")).toBe(occupied);
    expect(pool.peek("thread-other")).toBe(turnStore);
    expect(pool.peek("session-1")).toBe(occupied);
  });

  it("reset 只作用于驻留 store，不创建新条目", () => {
    const { createStore } = createFakeFactory();
    const pool = createTrajectoryStorePool({ createStore });

    const store = pool.acquire("a") as FakeStore;
    expect(pool.reset("missing", { hard: true })).toBe(false);
    expect(pool.residentKeys()).toEqual(["a"]);
    expect(pool.reset("a", { hard: true })).toBe(true);
    expect(store.resetCalls).toEqual([{ hard: true }]);
  });

  it("空键落到匿名桶，disposeAll 释放全部条目", () => {
    const { createStore } = createFakeFactory();
    const pool = createTrajectoryStorePool({ createStore });

    const anonymous = pool.acquire("") as FakeStore;
    expect(pool.acquire("   ")).toBe(anonymous);
    expect(pool.residentKeys()).toEqual([ANONYMOUS_TRAJECTORY_STORE_KEY]);

    pool.disposeAll();
    expect(anonymous.disposed).toBe(true);
    expect(pool.residentKeys()).toEqual([]);
  });

  it("默认容量与方案口径一致", () => {
    expect(DEFAULT_TRAJECTORY_STORE_POOL_SIZE).toBe(3);
    const { created, createStore } = createFakeFactory();
    const pool = createTrajectoryStorePool({ createStore });
    pool.acquire("a");
    pool.acquire("b");
    pool.acquire("c");
    pool.acquire("d");
    expect(created).toHaveLength(4);
    expect(pool.residentKeys()).toEqual(["b", "c", "d"]);
  });
});
