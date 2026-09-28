import {
  act,
  useCallback,
  useState,
  type Dispatch,
  type SetStateAction,
} from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { Thread } from "@/data/mock";
import { resetLiveStreamTextStore } from "@/lib/live-stream-text";
import {
  createRuntimeDeltaCoordinator,
  getRuntimeEventSeq,
  mergeRuntimeEvent,
  type RuntimeDeltaCoordinator,
} from "@/lib/workspace-thread-state";
import type { SessionRuntimeEvent } from "@/lib/runtime-api";
import type { AgentChatStreamChunkPayload } from "@/types/runtime";

vi.mock("@/lib/runtime-api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/runtime-api")>();
  return {
    ...actual,
    streamSessionRuntime: vi.fn(),
  };
});

import { streamSessionRuntime } from "@/lib/runtime-api";

import { createAgentChatStreamHandlers } from "./agent-chat-turn/stream-handlers";
import { createTurnRuntimeState } from "./agent-chat-turn/turn-state";
import { resetFrameIntake } from "./frame-intake";
import { useSessionRuntimeStream } from "./use-session-runtime-stream";

const mockStream = vi.mocked(streamSessionRuntime);
type StreamOptions = Parameters<typeof streamSessionRuntime>[1];
let captured: StreamOptions | null = null;

// 真实 applyRuntimeDeltaToThread 命中时会返回新对象；mock 保持同一契约，
// 否则 willApply 探针（判定「这条增量能否落到消息上」）恒为 false，
// live 追加会被跳过，per-frame 计数口径失真。
const applyDeltaSpy = vi.fn((thread: Thread) => ({ ...thread }));
const applyEventSpy = vi.fn((thread: Thread) => thread);

function createThread(): Thread {
  return {
    id: "thread-1",
    title: "Thread",
    summary: "Summary",
    updatedAt: "2026-08-30T00:00:00Z",
    status: "active",
    sessionId: "session-1",
    transport: "live",
    runtimeSource: "runtime",
    lastError: null,
    tags: [],
    prompts: [],
    messages: [
      {
        id: "assistant-1",
        role: "assistant",
        author: "Runtime stream",
        label: "streaming",
        segments: [{ type: "text", content: "..." }],
      },
    ],
    artifacts: [],
  };
}

/**
 * 同一帧在两条通道上的两种形态：runtime 事件（payload.seq = 持久化游标）与
 * chat wire 帧（_event.sequence）。`sequence` 是 provider 的逐帧序号，与
 * `seq` 同步推进——provider key（stream_id + sequence）因此逐帧唯一，跨通道
 * 去重不会把相邻两帧误判成同一 key。
 */
function runtimeDelta(seq: number, delta = "A"): SessionRuntimeEvent {
  return {
    type: "assistant_delta",
    timestamp: "2026-08-30T00:00:01Z",
    payload: {
      delta,
      seq,
      sequence: seq,
      stream_id: "stream-1",
      turn_id: "turn-1",
    },
  };
}

function chatChunk(payload: Record<string, unknown>) {
  return payload as unknown as AgentChatStreamChunkPayload;
}

function Harness({
  coordinator,
  onTrajectoryEvent,
}: {
  coordinator: RuntimeDeltaCoordinator;
  onTrajectoryEvent: (event: SessionRuntimeEvent) => void;
}) {
  const [thread] = useState<Thread>(createThread);
  // 提交路径只需把 updater 跑起来（apply 次数由 spy 计数断言），不保留 React 状态。
  const setThreads = useCallback<Dispatch<SetStateAction<Thread[]>>>(
    (updater) => {
      if (typeof updater === "function") {
        updater([thread]);
      }
    },
    [thread],
  );
  useSessionRuntimeStream({
    applyRuntimeDeltaToThread: applyDeltaSpy,
    applyRuntimeEventToThread: applyEventSpy,
    // 与 chat 入口同一个协调器：L3 帧闸门按其作用域共享跨通道水位。
    deltaCoordinator: coordinator,
    getErrorMessage: (_error, fallback) => fallback,
    getRuntimeEventSeq,
    mergeRuntimeEvent,
    renderLiveDeltas: true,
    activeTurnId: "turn-1",
    onTrajectoryEvent,
    selectedThread: thread,
    setThreads,
  });
  return null;
}

function createChatHarness(coordinator?: RuntimeDeltaCoordinator) {
  const turnState = createTurnRuntimeState({} as Thread);
  const pushTrajectory = vi.fn();
  const claimSpy = coordinator ? vi.spyOn(coordinator, "claim") : null;
  const handlers = createAgentChatStreamHandlers({
    assistantMessageId: "msg-1",
    attachTurnArtifact: vi.fn(),
    controller: new AbortController(),
    deltaCoordinator: coordinator,
    finalizeTurn: vi.fn(),
    frameScheduler: { flush: vi.fn(), schedule: vi.fn() },
    handleToolEnd: vi.fn(),
    notifyFailure: vi.fn(),
    pushTrajectory,
    sessionId: "session-1",
    setPhaseAndRef: vi.fn(),
    turnId: "turn-1",
    turnState,
    updateCurrentThread: vi.fn(),
    updateStreamingError: vi.fn(),
    upsertLiveToolSegment: vi.fn(),
  });
  return { claimSpy, handlers, pushTrajectory, turnState };
}

describe("L3 跨通道帧闸门：同一个 token 帧只生效一次", () => {
  let container: HTMLDivElement;
  let root: Root;
  let mounted: boolean;

  beforeEach(() => {
    (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
      true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    mounted = true;
    captured = null;
    resetFrameIntake();
    resetLiveStreamTextStore();
    applyDeltaSpy.mockClear();
    applyEventSpy.mockClear();
    mockStream.mockReset();
    mockStream.mockImplementation(async (_sessionId, options) => {
      captured = options;
      await new Promise<void>((resolve) => {
        options.signal?.addEventListener("abort", () => resolve(), { once: true });
      });
    });
  });

  const unmountRoot = async () => {
    if (!mounted) {
      return;
    }
    mounted = false;
    await act(async () => {
      root.unmount();
    });
  };

  afterEach(async () => {
    await unmountRoot();
    container.remove();
  });

  const mount = async (
    coordinator: RuntimeDeltaCoordinator,
    onTrajectoryEvent: (event: SessionRuntimeEvent) => void,
  ) => {
    await act(async () => {
      root.render(
        <Harness coordinator={coordinator} onTrajectoryEvent={onTrajectoryEvent} />,
      );
    });
    const onEvent = captured?.onEvent;
    if (!onEvent) {
      throw new Error("runtime stream options were not captured");
    }
    return onEvent;
  };

  it("runtime 入口：同 seq 重投整帧丢弃，不重复应用", async () => {
    const coordinator = createRuntimeDeltaCoordinator();
    const onTrajectory = vi.fn();
    const onEvent = await mount(coordinator, onTrajectory);

    act(() => {
      onEvent(runtimeDelta(7));
    });
    expect(onTrajectory).toHaveBeenCalledTimes(1);
    // 每条放行的增量两次触碰 apply：willApply 探测 + live 追加（判定在 updater 外）。
    const appliesPerFrame = applyDeltaSpy.mock.calls.length;
    expect(appliesPerFrame).toBe(2);

    // 同一帧经窗口回放 / after dump / live 重叠二次投递：入口整帧丢弃。
    act(() => {
      onEvent(runtimeDelta(7));
    });
    expect(onTrajectory).toHaveBeenCalledTimes(1);
    expect(applyDeltaSpy).toHaveBeenCalledTimes(appliesPerFrame);

    // seq 推进则放行。
    act(() => {
      onEvent(runtimeDelta(8, "B"));
    });
    expect(onTrajectory).toHaveBeenCalledTimes(2);
    expect(applyDeltaSpy).toHaveBeenCalledTimes(appliesPerFrame * 2);

    // unmount 前 flush：被放行的两条帧进入合帧提交（每条再兑现一次 apply）。
    await unmountRoot();
    expect(applyDeltaSpy).toHaveBeenCalledTimes(appliesPerFrame * 2 + 2);
  });

  it("runtime 先应用后，chat 通道的同 seq 孪生帧被拒且不消费 delta key", async () => {
    const coordinator = createRuntimeDeltaCoordinator();
    const onTrajectory = vi.fn();
    const onEvent = await mount(coordinator, onTrajectory);

    act(() => {
      onEvent(runtimeDelta(7));
    });
    expect(onTrajectory).toHaveBeenCalledTimes(1);

    const { claimSpy, handlers, pushTrajectory, turnState } =
      createChatHarness(coordinator);
    handlers.onChunk(
      chatChunk({
        type: "text",
        content: "A",
        stream_id: "stream-1",
        sequence: 7,
        _event: { sequence: 7 },
      }),
    );

    expect(turnState.streamedText).toBe("");
    expect(pushTrajectory).not.toHaveBeenCalled();
    expect(claimSpy).not.toHaveBeenCalled();

    // seq=0 的 wire-only 帧不受闸门影响，仍走既有 claim 路径。
    handlers.onChunk(
      chatChunk({ type: "text", content: "B", stream_id: "stream-2", sequence: 2 }),
    );
    expect(turnState.streamedText).toBe("B");
    expect(claimSpy).toHaveBeenCalledTimes(1);
  });

  it("chat 先应用（带持久化 seq）后，runtime 通道的同 seq 帧被拒", async () => {
    const coordinator = createRuntimeDeltaCoordinator();
    const { handlers, turnState } = createChatHarness(coordinator);
    handlers.onChunk(
      chatChunk({
        type: "text",
        content: "A",
        stream_id: "stream-1",
        // 孪生帧共享 provider 身份（stream_id + sequence），与 runtime 帧一致。
        sequence: 7,
        _event: { sequence: 7 },
      }),
    );
    expect(turnState.streamedText).toBe("A");

    const onTrajectory = vi.fn();
    const onEvent = await mount(coordinator, onTrajectory);
    act(() => {
      onEvent(runtimeDelta(7));
    });
    expect(onTrajectory).not.toHaveBeenCalled();
    expect(applyDeltaSpy).not.toHaveBeenCalled();

    act(() => {
      onEvent(runtimeDelta(8, "B"));
    });
    expect(onTrajectory).toHaveBeenCalledTimes(1);
  });

  it("runtime 入口只对内容增量帧过闸（工具/生命周期帧不参与）", async () => {
    const coordinator = createRuntimeDeltaCoordinator();
    const onTrajectory = vi.fn();
    const onEvent = await mount(coordinator, onTrajectory);
    const lifecycle: SessionRuntimeEvent = {
      type: "turn.started",
      timestamp: "2026-08-30T00:00:01Z",
      payload: { seq: 7, turn_id: "turn-1" },
    };

    act(() => {
      onEvent(lifecycle);
      onEvent(lifecycle);
    });

    // 非增量帧不参与闸门：两条都走既有 merge/apply 路径（行为不变）。
    expect(onTrajectory).toHaveBeenCalledTimes(2);
  });

  it("直连 chat 流持有时 runtime 只消费不应用，释放后恢复", async () => {
    const coordinator = createRuntimeDeltaCoordinator();
    const onTrajectory = vi.fn();
    const onEvent = await mount(coordinator, onTrajectory);

    coordinator.holdDirectStream("session-1");
    act(() => {
      onEvent(runtimeDelta(7));
    });
    // 帧仍进轨迹（消费），但增量不落消息：占位期间不 apply、也不消费 claim。
    expect(onTrajectory).toHaveBeenCalledTimes(1);
    expect(applyDeltaSpy).not.toHaveBeenCalled();

    // 释放后 runtime 恢复：同一条增量仍可应用（占位期间没吞 claim）。
    coordinator.releaseDirectStream("session-1");
    act(() => {
      onEvent(runtimeDelta(8, "B"));
    });
    expect(applyDeltaSpy).toHaveBeenCalled();
  });
});
