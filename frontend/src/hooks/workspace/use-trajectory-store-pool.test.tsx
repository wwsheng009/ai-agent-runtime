// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { TrajectoryStore } from "@/hooks/workspace/use-trajectory-snapshot";
import { createTrajectoryStorePool } from "@/lib/trajectory/store-pool";

import {
  resolveTrajectoryStoreKey,
  useSelectedTrajectoryStore,
  useTrajectoryStorePool,
} from "./use-trajectory-store-pool";

type Selection = { sessionId?: string; threadId?: string };
type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

function SelectionHarness({
  pool,
  selection,
  onStore,
}: {
  pool: ReturnType<typeof createTrajectoryStorePool>;
  selection: Selection;
  onStore: (store: TrajectoryStore) => void;
}) {
  onStore(useSelectedTrajectoryStore(pool, selection));
  return null;
}

function PoolHarness({
  onStore,
}: {
  onStore: (store: TrajectoryStore) => void;
}) {
  const pool = useTrajectoryStorePool({ maxStores: 2 });
  onStore(useSelectedTrajectoryStore(pool, { threadId: "thread-1" }));
  return null;
}

describe("useSelectedTrajectoryStore / resolveTrajectoryStoreKey", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => {
      root.unmount();
    });
    container.remove();
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  it("键口径：sessionId 优先，草稿线程 id 兜底，空身份落匿名桶", () => {
    expect(resolveTrajectoryStoreKey("session-1", "thread-1")).toBe("session-1");
    expect(resolveTrajectoryStoreKey("", "thread-1")).toBe("thread-1");
    expect(resolveTrajectoryStoreKey(undefined, undefined)).toBe("__anonymous__");
    expect(resolveTrajectoryStoreKey("  ", "  ")).toBe("__anonymous__");
  });

  it("切换会话不 reset：切回仍是同一实例，游标保留", () => {
    const pool = createTrajectoryStorePool({ maxStores: 3 });
    const stores: TrajectoryStore[] = [];
    const onStore = (store: TrajectoryStore) => stores.push(store);

    act(() => {
      root.render(
        <SelectionHarness
          pool={pool}
          selection={{ sessionId: "session-a" }}
          onStore={onStore}
        />,
      );
    });
    const storeA = stores.at(-1) as TrajectoryStore;
    act(() => {
      storeA.advanceCursor(11);
      storeA.flush();
    });
    expect(storeA.getSnapshot().lastEventSeq).toBe(11);

    act(() => {
      root.render(
        <SelectionHarness
          pool={pool}
          selection={{ sessionId: "session-b" }}
          onStore={onStore}
        />,
      );
    });
    const storeB = stores.at(-1) as TrajectoryStore;
    expect(storeB).not.toBe(storeA);
    expect(storeB.getSnapshot().lastEventSeq).toBe(0);

    act(() => {
      root.render(
        <SelectionHarness
          pool={pool}
          selection={{ sessionId: "session-a" }}
          onStore={onStore}
        />,
      );
    });
    expect(stores.at(-1)).toBe(storeA);
    expect(storeA.getSnapshot().lastEventSeq).toBe(11);
  });

  it("草稿线程键升级（线程 id → sessionId）迁移同一实例", () => {
    const pool = createTrajectoryStorePool({ maxStores: 3 });
    const stores: TrajectoryStore[] = [];
    const onStore = (store: TrajectoryStore) => stores.push(store);

    act(() => {
      root.render(
        <SelectionHarness
          pool={pool}
          selection={{ threadId: "thread-draft" }}
          onStore={onStore}
        />,
      );
    });
    const draftStore = stores.at(-1) as TrajectoryStore;
    act(() => {
      draftStore.advanceCursor(3);
      draftStore.flush();
    });

    // 首个回合落库：同一线程 id，sessionId 出现。
    act(() => {
      root.render(
        <SelectionHarness
          pool={pool}
          selection={{ threadId: "thread-draft", sessionId: "session-1" }}
          onStore={onStore}
        />,
      );
    });
    expect(stores.at(-1)).toBe(draftStore);
    expect(pool.peek("thread-draft")).toBeUndefined();
    expect(pool.peek("session-1")).toBe(draftStore);
  });

  it("不同线程（真切换）不迁移，各用各的 store", () => {
    const pool = createTrajectoryStorePool({ maxStores: 3 });
    const stores: TrajectoryStore[] = [];
    const onStore = (store: TrajectoryStore) => stores.push(store);

    act(() => {
      root.render(
        <SelectionHarness
          pool={pool}
          selection={{ threadId: "thread-draft" }}
          onStore={onStore}
        />,
      );
    });
    const draftStore = stores.at(-1) as TrajectoryStore;

    act(() => {
      root.render(
        <SelectionHarness
          pool={pool}
          selection={{ threadId: "thread-other", sessionId: "session-9" }}
          onStore={onStore}
        />,
      );
    });
    expect(stores.at(-1)).not.toBe(draftStore);
    expect(pool.peek("thread-draft")).toBe(draftStore);
    expect(pool.peek("session-9")).toBe(stores.at(-1));
  });

  it("useTrajectoryStorePool 卸载时 dispose 池内 store", () => {
    const stores: TrajectoryStore[] = [];
    const onStore = (store: TrajectoryStore) => stores.push(store);
    act(() => {
      root.render(<PoolHarness onStore={onStore} />);
    });
    const store = stores.at(-1) as TrajectoryStore;
    const listener = vi.fn();
    const unsubscribe = store.subscribe(listener);

    act(() => {
      root.unmount();
    });
    act(() => {
      store.advanceCursor(1);
      store.flush();
    });
    // dispose 清空订阅者：卸载后的迟到事件不再通知（页面级池生命周期终点）。
    expect(listener).not.toHaveBeenCalled();
    expect(unsubscribe).toBeTypeOf("function");
  });
});
