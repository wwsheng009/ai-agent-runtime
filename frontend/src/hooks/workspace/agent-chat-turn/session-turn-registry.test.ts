/**
 * 回合状态注册表单测（工作区多会话并发 Batch 1）。
 *
 * 锁定的行为：
 * - 同会话单飞、跨会话并行（A 在跑不阻塞 B，服务端 ErrSessionBusy 的镜像）；
 * - 相位 / 在途身份 / 静默看门狗按会话分键，互不串台；
 * - 同一线程的键升级（草稿线程 id → 服务端 sessionId）自动迁移条目；
 * - `bindSessionTurn` 的兼容写入器（phaseRef / activeTurnIdRef / setter）语义。
 */
import { describe, expect, it, vi } from "vitest";

import {
  bindSessionTurn,
  createSessionTurnRegistry,
} from "./session-turn-registry";
import type { ChatTurnRuntimeState } from "./turn-state";

function createTurnState(): ChatTurnRuntimeState {
  return {
    currentSessionId: "session-1",
  } as unknown as ChatTurnRuntimeState;
}

function beginTurn(
  registry: ReturnType<typeof createSessionTurnRegistry>,
  key: string,
  options?: { threadId?: string; turnId?: string },
) {
  const turnId = options?.turnId ?? `turn-${key}`;
  return registry.beginTurn({
    key,
    threadId: options?.threadId ?? key,
    turnId,
    controller: new AbortController(),
    turnState: createTurnState(),
  });
}

describe("createSessionTurnRegistry（回合状态注册表）", () => {
  it("同会话单飞、跨会话并行：A 在跑不阻塞 B", () => {
    const registry = createSessionTurnRegistry();

    beginTurn(registry, "session-a");
    expect(registry.isBusy("session-a")).toBe(true);
    expect(registry.isBusy("session-b")).toBe(false);
    expect(registry.getSnapshot("session-a").entry).not.toBeNull();

    // 后台会话同时起回合：两条通道各持一份状态。
    beginTurn(registry, "session-b");
    expect(registry.activeKeys()).toEqual(["session-a", "session-b"]);

    // A 收尾只清 A：B 仍在跑。
    registry.finishTurn("turn-session-a");
    expect(registry.isBusy("session-a")).toBe(false);
    expect(registry.isBusy("session-b")).toBe(true);
    expect(registry.getSnapshot("session-a").entry).toBeNull();
    expect(registry.getSnapshot("session-b").entry?.turnId).toBe("turn-session-b");
  });

  it("相位 / 在途身份按回合写入，finalize 起手清空在途身份且条目仍在", () => {
    const registry = createSessionTurnRegistry();
    beginTurn(registry, "session-a");

    registry.setPhase("turn-session-a", "streaming");
    expect(registry.phaseOf("turn-session-a")).toBe("streaming");
    expect(registry.activeTurnIdOf("turn-session-a")).toBe("turn-session-a");

    registry.clearActiveTurnId("turn-session-a");
    expect(registry.activeTurnIdOf("turn-session-a")).toBeNull();
    // 条目还在：isResponding 保持 true，直到通道收尾。
    expect(registry.isTurnRunning("turn-session-a")).toBe(true);
    expect(registry.isBusy("session-a")).toBe(true);

    registry.finishTurn("turn-session-a");
    expect(registry.phaseOf("turn-session-a")).toBeNull();
    expect(registry.isTurnRunning("turn-session-a")).toBe(false);
  });

  it("静默看门狗标记按会话隔离，setStalled 幂等且通知订阅方", () => {
    const registry = createSessionTurnRegistry();
    const listener = vi.fn();
    registry.subscribe(listener);

    beginTurn(registry, "session-a");
    beginTurn(registry, "session-b");
    listener.mockClear();

    registry.setStalled("session-a", true);
    expect(registry.getSnapshot("session-a").stalled).toBe(true);
    expect(registry.getSnapshot("session-b").stalled).toBe(false);
    expect(listener).toHaveBeenCalledTimes(1);

    // 幂等：同值不再通知。
    registry.setStalled("session-a", true);
    expect(listener).toHaveBeenCalledTimes(1);

    registry.setStalled("session-a", false);
    expect(registry.getSnapshot("session-a").stalled).toBe(false);
    expect(listener).toHaveBeenCalledTimes(2);
  });

  it("adopt 迁移同一线程的键升级（条目 + stall 一起搬）", () => {
    const registry = createSessionTurnRegistry();
    beginTurn(registry, "thread-draft", { threadId: "thread-draft", turnId: "turn-1" });
    registry.setStalled("thread-draft", true);

    registry.adopt("thread-draft", "session-1");

    expect(registry.isBusy("thread-draft")).toBe(false);
    expect(registry.getSnapshot("thread-draft").entry).toBeNull();
    const moved = registry.getSnapshot("session-1");
    expect(moved.entry?.turnId).toBe("turn-1");
    expect(moved.entry?.key).toBe("session-1");
    expect(moved.stalled).toBe(true);
    // 回合身份仍可定位（finalize/finish 走 turnId）。
    expect(registry.isTurnRunning("turn-1")).toBe(true);
    expect(registry.activeKeys()).toEqual(["session-1"]);
  });

  it("resolveThreadKey：同线程解析时自动迁移，异线程不迁移", () => {
    const registry = createSessionTurnRegistry();
    expect(registry.resolveThreadKey("thread-draft", "thread-draft")).toBe(
      "thread-draft",
    );
    beginTurn(registry, "thread-draft", {
      threadId: "thread-draft",
      turnId: "turn-1",
    });

    // 首个回合落库：同一 threadId、键升级为 sessionId。
    expect(registry.resolveThreadKey("thread-draft", "session-1")).toBe(
      "session-1",
    );
    expect(registry.isBusy("session-1")).toBe(true);
    expect(registry.getSnapshot("thread-draft").entry).toBeNull();

    // 真切换（另一个线程）：不迁移别人的条目。
    beginTurn(registry, "session-9", { threadId: "thread-9", turnId: "turn-9" });
    expect(registry.resolveThreadKey("thread-9", "session-9")).toBe("session-9");
    expect(registry.isBusy("session-1")).toBe(true);

    // 空线程身份：原样返回（选中会话尚未就绪时退化为按 key 判定）。
    expect(registry.resolveThreadKey("", "session-1")).toBe("session-1");
  });

  it("bindSessionTurn：phaseRef / activeTurnIdRef 与 setter 桥接到本回合条目", () => {
    const registry = createSessionTurnRegistry();
    beginTurn(registry, "session-a");
    const bindings = bindSessionTurn(registry, "turn-session-a");

    expect(bindings.phaseRef.current).toBeNull();
    bindings.setPhase("connecting");
    expect(bindings.phaseRef.current).toBe("connecting");
    expect(registry.phaseOf("turn-session-a")).toBe("connecting");

    bindings.phaseRef.current = "streaming";
    expect(registry.phaseOf("turn-session-a")).toBe("streaming");

    bindings.setActiveTurnId(null);
    expect(bindings.activeTurnIdRef.current).toBeNull();
    bindings.activeTurnIdRef.current = null;
    expect(registry.activeTurnIdOf("turn-session-a")).toBeNull();
  });

  it("getSnapshot 同键返回缓存引用（useSyncExternalStore 契约）", () => {
    const registry = createSessionTurnRegistry();
    beginTurn(registry, "session-a");
    const first = registry.getSnapshot("session-a");
    expect(registry.getSnapshot("session-a")).toBe(first);

    // 状态推进使缓存失效：引用变化才会触发订阅方重渲染。
    registry.setPhase("turn-session-a", "streaming");
    expect(registry.getSnapshot("session-a")).not.toBe(first);
  });

  it("abortAll 中止所有在途回合（卸载清理）", () => {
    const registry = createSessionTurnRegistry();
    const a = beginTurn(registry, "session-a");
    const b = beginTurn(registry, "session-b");

    registry.abortAll();

    expect(a.controller.signal.aborted).toBe(true);
    expect(b.controller.signal.aborted).toBe(true);
  });
});
