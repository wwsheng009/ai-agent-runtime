/**
 * Batch 2（多会话并发运行时）：注册表预算 / 调度 / 通知回归。
 *
 * 覆盖方案 §4.2 / §4.6 的资源治理面：
 * - live 预算 1 前台 + 2 后台，超预算降 poll 并在名额空出时补位；
 * - 显式升级（explicit）按最近活动 LRU 顶掉一条后台 live；
 * - 前台切换后前一前台按"有无在途回合"收敛（无回合 → poll）；
 * - 后台 live 终态宽限到期降 poll；
 * - 页面隐藏降采样（前台保留、后台降 poll）与恢复回升；
 * - 订阅者错误隔离与释放通知。
 */

import { afterEach, describe, expect, it, vi } from "vitest";

import type {
  SessionRuntimeEntry,
  SessionRuntimeEntryConfig,
} from "@/lib/session-runtime/entry";
import { createSessionRuntimeRegistry } from "@/lib/session-runtime/registry";
import type {
  SessionRuntimeEntrySnapshot,
  SubscriptionMode,
} from "@/lib/session-runtime/types";
import type { RuntimeSessionActiveTurn, SessionRuntimeEvent } from "@/types/runtime";

type FakeEntry = SessionRuntimeEntry & {
  emitActiveTurn(turn: RuntimeSessionActiveTurn | null): void;
  emitEvent(event: SessionRuntimeEvent): void;
  /** setPaused 调用轨迹（断言隐藏暂停 / 恢复可见）。 */
  pausedStates: boolean[];
};

function createFakeEntry(config: SessionRuntimeEntryConfig): FakeEntry {
  const observers = new Set<(snapshot: SessionRuntimeEntrySnapshot) => void>();
  let snapshot: SessionRuntimeEntrySnapshot = {
    sessionId: config.sessionId,
    mode: "idle",
    status: "idle",
    lastSeq: 0,
    activeTurn: null,
    detached: false,
    pending: { approvals: 0, questions: 0, planPending: false },
    runningAgents: 0,
    lastEventAt: null,
    lastError: null,
    paused: false,
  };
  const pausedStates: boolean[] = [];
  const publish = (patch: Partial<SessionRuntimeEntrySnapshot>) => {
    snapshot = { ...snapshot, ...patch };
    for (const observer of [...observers]) {
      observer(snapshot);
    }
  };
  return {
    sessionId: config.sessionId,
    snapshot: () => snapshot,
    subscribe: (observer) => {
      observers.add(observer);
      return () => {
        observers.delete(observer);
      };
    },
    setMode: (mode: SubscriptionMode) => publish({ mode }),
    setPaused: (paused: boolean) => {
      pausedStates.push(paused);
      publish({ paused });
    },
    noteActiveTurn: (turn) => publish({ activeTurn: turn }),
    retry: () => {},
    dispose: () => observers.clear(),
    emitActiveTurn: (turn) => publish({ activeTurn: turn }),
    emitEvent: (event) => config.onEvent?.(event),
    pausedStates,
  };
}

function activeTurn(turnId: string): RuntimeSessionActiveTurn {
  return {
    sessionId: "session",
    turnId,
    source: "agent_chat_stream",
    detached: false,
  };
}

const disposables: Array<{ dispose(): void }> = [];
let tick = 0;

function makeRegistry(options: Parameters<typeof createSessionRuntimeRegistry>[0] = {}) {
  const registry = createSessionRuntimeRegistry({
    createEntry: createFakeEntry,
    now: () => (tick += 1_000),
    ...options,
  });
  disposables.push(registry);
  return registry;
}

afterEach(() => {
  for (const disposable of disposables.splice(0)) {
    disposable.dispose();
  }
  vi.useRealTimers();
});

describe("createSessionRuntimeRegistry 预算与调度", () => {
  it("live 预算 1 前台 + 2 后台：超预算降 poll，释放后补位", () => {
    const registry = makeRegistry();
    registry.ensure("A", "selected");
    registry.ensure("B", "active-turn");
    registry.ensure("C", "active-turn");
    registry.ensure("D", "active-turn");

    expect(registry.snapshot("A")?.mode).toBe("live");
    expect(registry.snapshot("B")?.mode).toBe("live");
    expect(registry.snapshot("C")?.mode).toBe("live");
    expect(registry.snapshot("D")?.mode).toBe("poll");

    registry.release("B", "deselected");
    expect(registry.snapshot("D")?.mode).toBe("live");
    expect(registry.snapshot("A")?.mode).toBe("live");
    expect(registry.snapshot("B")).toBeUndefined();
  });

  it("10 个会话规模：live 总数 ≤ 1 前台 + 2 后台，其余全部 poll", () => {
    const registry = makeRegistry();
    registry.ensure("S0", "selected");
    for (let index = 1; index <= 10; index += 1) {
      registry.ensure(`S${index}`, "active-turn");
    }

    const entries = registry.getEntriesSnapshot();
    expect(entries).toHaveLength(11);
    const live = entries.filter((entry) => entry.mode === "live");
    expect(live.map((entry) => entry.sessionId)).toEqual(["S0", "S1", "S2"]);
    expect(entries.filter((entry) => entry.mode === "poll")).toHaveLength(8);
  });

  it("poll 名额上限：超出保持 idle，释放后按等待顺序补位", () => {
    const registry = makeRegistry({ maxPollSessions: 2 });
    registry.ensure("A", "recent");
    registry.ensure("B", "recent");
    registry.ensure("C", "recent");
    registry.ensure("D", "recent");

    expect(registry.snapshot("A")?.mode).toBe("poll");
    expect(registry.snapshot("B")?.mode).toBe("poll");
    expect(registry.snapshot("C")?.mode).toBe("idle");
    expect(registry.snapshot("D")?.mode).toBe("idle");

    registry.release("A", "idle");
    expect(registry.snapshot("C")?.mode).toBe("poll");
    expect(registry.snapshot("D")?.mode).toBe("idle");

    registry.release("B", "idle");
    expect(registry.snapshot("D")?.mode).toBe("poll");
  });

  it("显式升级按最近活动 LRU 顶掉一条后台 live", () => {
    const registry = makeRegistry();
    registry.ensure("A", "selected");
    registry.ensure("B", "active-turn");
    registry.ensure("C", "active-turn");
    // 刷新 C 的活动时间：B 成为最久未触碰的后台 live。
    registry.ensure("C", "active-turn");

    registry.ensure("D", "explicit");
    expect(registry.snapshot("D")?.mode).toBe("live");
    expect(registry.snapshot("C")?.mode).toBe("live");
    expect(registry.snapshot("B")?.mode).toBe("poll");
    expect(registry.snapshot("A")?.mode).toBe("live");
  });

  it("前台切换：前一前台无在途回合则降 poll，有在途回合保留 live", () => {
    const registry = makeRegistry();
    registry.ensure("A", "selected");
    registry.ensure("B", "selected");
    expect(registry.snapshot("B")?.mode).toBe("live");
    expect(registry.snapshot("A")?.mode).toBe("poll");

    registry.ensure("C", "selected");
    expect(registry.snapshot("C")?.mode).toBe("live");
    // A/B 都在后台：B 是前一前台，无回合 → poll。
    expect(registry.snapshot("B")?.mode).toBe("poll");
  });

  it("后台 live 终态宽限到期降 poll", async () => {
    vi.useFakeTimers();
    const created = new Map<string, FakeEntry>();
    const registry = makeRegistry({
      backgroundLiveGraceMs: 1_000,
      createEntry: (config) => {
        const entry = createFakeEntry(config);
        created.set(config.sessionId, entry);
        return entry;
      },
    });
    registry.ensure("A", "selected");
    registry.ensure("B", "active-turn");
    expect(registry.snapshot("B")?.mode).toBe("live");

    created.get("B")?.emitActiveTurn(activeTurn("turn-1"));
    expect(registry.snapshot("B")?.activeTurn?.turnId).toBe("turn-1");
    created.get("B")?.emitActiveTurn(null);

    await vi.advanceTimersByTimeAsync(1_000);
    expect(registry.snapshot("B")?.mode).toBe("poll");
  });

  it("页面隐藏：后台 live 降 poll、前台保留；恢复可见回升", () => {
    const registry = makeRegistry();
    registry.ensure("A", "selected");
    registry.ensure("B", "active-turn");
    expect(registry.snapshot("B")?.mode).toBe("live");

    registry.noteVisibility(true);
    expect(registry.snapshot("A")?.mode).toBe("live");
    expect(registry.snapshot("B")?.mode).toBe("poll");

    registry.noteVisibility(false);
    expect(registry.snapshot("B")?.mode).toBe("live");
  });

  it("页面隐藏暂停后台 poll、恢复可见恢复轮询", () => {
    const created = new Map<string, FakeEntry>();
    const registry = makeRegistry({
      createEntry: (config) => {
        const entry = createFakeEntry(config);
        created.set(config.sessionId, entry);
        return entry;
      },
    });
    registry.ensure("A", "recent");
    expect(registry.snapshot("A")?.mode).toBe("poll");
    expect(registry.snapshot("A")?.paused).toBe(false);

    registry.noteVisibility(true);
    expect(created.get("A")?.pausedStates).toContain(true);
    expect(registry.snapshot("A")?.paused).toBe(true);
    expect(registry.snapshot("A")?.mode).toBe("poll");

    registry.noteVisibility(false);
    expect(registry.snapshot("A")?.paused).toBe(false);
    expect(registry.snapshot("A")?.mode).toBe("poll");
  });
});

describe("createSessionRuntimeRegistry 通知", () => {
  it("订阅者错误隔离；释放后通知失效", () => {
    const created = new Map<string, FakeEntry>();
    const registry = makeRegistry({
      createEntry: (config) => {
        const entry = createFakeEntry(config);
        created.set(config.sessionId, entry);
        return entry;
      },
    });
    registry.ensure("A", "selected");

    const seen: Array<string | undefined> = [];
    registry.subscribeEntry("A", () => {
      throw new Error("subscriber boom");
    });
    registry.subscribeEntry("A", (snapshot) => seen.push(snapshot?.status));

    created.get("A")?.emitActiveTurn(activeTurn("turn-1"));
    // 抛错的订阅者不影响其它订阅者（错误隔离）。
    expect(seen.length).toBeGreaterThan(0);

    const before = seen.length;
    registry.release("A", "deselected");
    expect(seen.length).toBe(before + 1);
    expect(seen[seen.length - 1]).toBeUndefined();
  });

  it("onEvent 出口把会话身份透传给页面层（错误隔离）", () => {
    const created = new Map<string, FakeEntry>();
    const registry = makeRegistry({
      createEntry: (config) => {
        const entry = createFakeEntry(config);
        created.set(config.sessionId, entry);
        return entry;
      },
    });
    registry.ensure("A", "selected");
    const received: Array<[string, string]> = [];
    registry.onEvent((sessionId, event) => {
      received.push([sessionId, event.type]);
      throw new Error("outlet boom");
    });
    const nested: Array<[string, string]> = [];
    registry.onEvent((sessionId, event) => nested.push([sessionId, event.type]));

    created.get("A")?.emitEvent({ type: "assistant_delta", timestamp: "t" });
    // 抛错的出口不影响其它出口（后台会话的"谁的事件"身份不因单个订阅者丢失）。
    expect(received).toEqual([["A", "assistant_delta"]]);
    expect(nested).toEqual([["A", "assistant_delta"]]);
  });
});
