import { type Dispatch, type SetStateAction } from "react";

import { type Thread } from "@/data/mock";
import { type ConnectionStatus } from "@/lib/connection-status";
import { type SessionRuntimeEvent } from "@/lib/runtime-api";
import { type PublicationLevel } from "@/lib/thread-state/publication";
import { type ThreadStoreUpdater } from "@/lib/thread-state/thread-store";
import {
  type RuntimeDeltaCoordinator,
  type RuntimeLiveDeltaSink,
} from "@/lib/workspace-thread-state";

/** 方案B：重连循环连续失败达到该阈值才把 thread 标记为降级（防瞬断抖动）。 */
export const STREAM_FAILURE_THRESHOLD = 3;

export type SessionRuntimeStreamOptions = {
  applyRuntimeEventToThread: (
    thread: Thread,
    sessionId: string,
    events: SessionRuntimeEvent[],
    event: SessionRuntimeEvent,
    /** 本会话在途回合身份：桥接帧确认归属后才允许补建 streaming 占位消息。 */
    expectedTurnId?: string,
  ) => Thread;
  getErrorMessage: (error: unknown, fallback: string) => string;
  getRuntimeEventSeq: (event: SessionRuntimeEvent) => number;
  mergeRuntimeEvent: (
    existingEvents: SessionRuntimeEvent[],
    nextEvent: SessionRuntimeEvent,
  ) => SessionRuntimeEvent[];
  /** Q4：可选轨迹投递——运行时生命周期事件到达时通知（由调用方转成轨迹 push）。 */
  onTrajectoryEvent?: (event: SessionRuntimeEvent) => void;
  /** P1-7：可选待交互投递——每条运行时事件到达时通知（待交互生命周期归约）。 */
  onRuntimeEvent?: (event: SessionRuntimeEvent) => void;
  /** 方案B：请求进行中才渲染增量文本（历史回放/reload 不渲染）。 */
  renderLiveDeltas?: boolean;
  /** 当前活动 turn（外部调用方透传；预留用于按 turn 维度对齐 delta）。 */
  activeTurnId?: string | null;
  /** Shares delta claims with the direct /api/agent/chat SSE request. */
  deltaCoordinator?: RuntimeDeltaCoordinator;
  /** 方案B：打字机增量事件（assistant_delta/reasoning/image_progress）应用函数。 */
  applyRuntimeDeltaToThread: (
    thread: Thread,
    event: SessionRuntimeEvent,
    expectedTurnId?: string,
    liveSink?: RuntimeLiveDeltaSink,
  ) => Thread;
  selectedThread: Thread | undefined;
  setThreads: Dispatch<SetStateAction<Thread[]>>;
  /**
   * P1-1 内容提交结构门：仅打字机增量（无持久事件）的合帧批走 live 通道
   * （按会话订阅者 = 消息列），不推进页面级结构快照。缺省回落到 `setThreads`
   * （未接双通道的调用方 / 测试环境保持原行为）。
   */
  setThreadsLive?: (updater: ThreadStoreUpdater) => void;
  /**
   * 尾部优先回放（tail-first）的建连闸门：轨迹首屏窗口就绪前不建连。
   *
   * 后端 `/runtime/stream?after=0` 会把整份事件日志按 SSE dump 重放一遍，若在
   * 窗口回放（`tail=1`）之前建连，尾部优先就完全失效（同一份日志既走窗口又走
   * dump）。回调方（workspace-page）在本会话窗口就绪前传 false。
   */
  enabled?: boolean;
  /**
   * 首屏建连游标：返回「轨迹已回放到的最大 seq」。
   *
   * 建连 after 取 `max(本 hook 已消费 seq, 此值)`：尾部窗口已回放到 latest_seq，
   * 因此**首个连接只要新事件**，不再把已回放过的窗口重放一遍（实测大会话
   * 2288 条 / 2.25MB 双份解析 + 双份重放）。
   */
  getReplayCursor?: () => number;
};

/**
 * 连接状态存储结构：按「连接键（thread:session:retryNonce）」存储；
 * 无会话时在渲染期派生为 idle（见 hook.ts）。
 */
export type RuntimeStreamStatusState = {
  key: string;
  status: ConnectionStatus;
} | null;

/** 可变 ref 结构：与 React useRef 返回值兼容（hook 侧创建，订阅侧读写）。 */
export type RuntimeStreamMutableRef<T> = { current: T };

/** 订阅函数依赖的 ref 集合：hook 侧 useRef 实例（见 hook.ts），此处只读写 current。 */
export type RuntimeStreamSubscriptionParams = {
  sessionId: string;
  sessionKey: string;
  setStatusState: Dispatch<SetStateAction<RuntimeStreamStatusState>>;
  runtimeEventsRef: RuntimeStreamMutableRef<Record<string, SessionRuntimeEvent[]>>;
  runtimeSeqRef: RuntimeStreamMutableRef<Record<string, number>>;
  activeTurnIdRef: RuntimeStreamMutableRef<string | null | undefined>;
  deltaCoordinatorRef: RuntimeStreamMutableRef<RuntimeDeltaCoordinator | undefined>;
  onTrajectoryEventRef: RuntimeStreamMutableRef<SessionRuntimeStreamOptions["onTrajectoryEvent"]>;
  onRuntimeEventRef: RuntimeStreamMutableRef<SessionRuntimeStreamOptions["onRuntimeEvent"]>;
  renderLiveDeltasRef: RuntimeStreamMutableRef<boolean>;
  applyRuntimeEventToThreadRef: RuntimeStreamMutableRef<
    SessionRuntimeStreamOptions["applyRuntimeEventToThread"]
  >;
  applyRuntimeDeltaToThreadRef: RuntimeStreamMutableRef<
    SessionRuntimeStreamOptions["applyRuntimeDeltaToThread"]
  >;
  getErrorMessageRef: RuntimeStreamMutableRef<SessionRuntimeStreamOptions["getErrorMessage"]>;
  getRuntimeEventSeqRef: RuntimeStreamMutableRef<SessionRuntimeStreamOptions["getRuntimeEventSeq"]>;
  mergeRuntimeEventRef: RuntimeStreamMutableRef<SessionRuntimeStreamOptions["mergeRuntimeEvent"]>;
  setThreadsRef: RuntimeStreamMutableRef<SessionRuntimeStreamOptions["setThreads"]>;
  setThreadsLiveRef: RuntimeStreamMutableRef<SessionRuntimeStreamOptions["setThreadsLive"]>;
  getPublicationLevelRef: RuntimeStreamMutableRef<(event: SessionRuntimeEvent) => PublicationLevel>;
  selectedThreadRef: RuntimeStreamMutableRef<Thread | undefined>;
  getReplayCursorRef: RuntimeStreamMutableRef<SessionRuntimeStreamOptions["getReplayCursor"]>;
  threadIdRef: RuntimeStreamMutableRef<string | undefined>;
};
