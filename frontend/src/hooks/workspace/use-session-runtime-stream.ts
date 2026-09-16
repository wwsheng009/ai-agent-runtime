import {
  type Dispatch,
  type SetStateAction,
  useCallback,
  useEffect,
  useRef,
  useState,
} from "react";

import { type Thread } from "@/data/mock";
import { type ConnectionStatus } from "@/lib/connection-status";
import {
  streamSessionRuntime,
  type SessionRuntimeEvent,
} from "@/lib/runtime-api";
import { matchesActiveTurn } from "@/lib/thread-state/deltas";
import { applyLiveStreamDelta } from "@/lib/live-stream-text";
import {
  getRuntimeDeltaKeyFromEvent,
  getRuntimeDeltaKind,
  getRuntimeEventTurnId,
  type RuntimeDeltaCoordinator,
  type RuntimeLiveDeltaSink,
} from "@/lib/workspace-thread-state";
import { createStreamingFrameScheduler } from "./agent-chat-turn/streaming-frame";

/** 方案B：重连循环连续失败达到该阈值才把 thread 标记为降级（防瞬断抖动）。 */
const STREAM_FAILURE_THRESHOLD = 3;

type SessionRuntimeStreamOptions = {
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

export function useSessionRuntimeStream({
  applyRuntimeEventToThread,
  applyRuntimeDeltaToThread,
  getErrorMessage,
  getRuntimeEventSeq,
  mergeRuntimeEvent,
  onTrajectoryEvent,
  onRuntimeEvent,
  renderLiveDeltas = false,
  activeTurnId,
  deltaCoordinator,
  selectedThread,
  setThreads,
  enabled = true,
  getReplayCursor,
}: SessionRuntimeStreamOptions) {
  // P1-8：对外暴露连接状态与手动重试入口；手动重试复用同一重连循环
  // （retryNonce 触发 effect 重跑，旧连接先 abort，不新增第二套退避）。
  // 状态按「连接键（thread:session:retryNonce）」存储：无会话时在渲染期派生为
  // idle，重试时自然回落 connecting；effect 同步段不写状态
  // （react-hooks/set-state-in-effect），状态推进只来自流回调与重连循环（异步）。
  const [statusState, setStatusState] = useState<{
    key: string;
    status: ConnectionStatus;
  } | null>(null);
  const [retryNonce, setRetryNonce] = useState(0);
  const retryConnection = useCallback(() => {
    setRetryNonce((current) => current + 1);
  }, []);

  const runtimeEventsRef = useRef<Record<string, SessionRuntimeEvent[]>>({});
  const runtimeSeqRef = useRef<Record<string, number>>({});
  const activeTurnIdRef = useRef(activeTurnId);
  useEffect(() => {
    activeTurnIdRef.current = activeTurnId;
  }, [activeTurnId]);
  const deltaCoordinatorRef = useRef(deltaCoordinator);
  useEffect(() => {
    deltaCoordinatorRef.current = deltaCoordinator;
  }, [deltaCoordinator]);
  // Q4：轨迹投递回调经 ref 转发——调用方每次 render 产生新引用时
  // 不触发 effect 重跑（否则 onEvent 内 setThreads → render → 新 arrow →
  // abort 重连 → 无限重连循环）。ref 写入放在 effect（react-hooks/refs）。
  const onTrajectoryEventRef = useRef(onTrajectoryEvent);
  useEffect(() => {
    onTrajectoryEventRef.current = onTrajectoryEvent;
  }, [onTrajectoryEvent]);
  const onRuntimeEventRef = useRef(onRuntimeEvent);
  useEffect(() => {
    onRuntimeEventRef.current = onRuntimeEvent;
  }, [onRuntimeEvent]);
  const renderLiveDeltasRef = useRef(renderLiveDeltas);
  useEffect(() => {
    renderLiveDeltasRef.current = renderLiveDeltas;
  }, [renderLiveDeltas]);
  // 订阅 effect 的依赖里**只允许留「会话身份」**（sessionId/sessionKey/retryNonce
  // 与 hasThreadId 布尔位）。回调与 setter 一律经 ref 读取：
  // 这些引用由父级 render 决定，父级因任何原因（首屏线程归并、事件到达后
  // setThreads 触发的新 render、父级 useCallback 依赖变化）产生新引用时，
  // 若留在依赖里就会 abort 在途 SSE 再重连——实测大会话首屏 1s 内 6 次建连、
  // 5 次被 cleanup 掐断（浏览器侧表现为 canceled/失败），且每次重连都从游标
  // 重拉一份事件日志。长连接的生命周期必须只由会话切换与手动重试决定。
  const applyRuntimeEventToThreadRef = useRef(applyRuntimeEventToThread);
  useEffect(() => {
    applyRuntimeEventToThreadRef.current = applyRuntimeEventToThread;
  }, [applyRuntimeEventToThread]);
  const applyRuntimeDeltaToThreadRef = useRef(applyRuntimeDeltaToThread);
  useEffect(() => {
    applyRuntimeDeltaToThreadRef.current = applyRuntimeDeltaToThread;
  }, [applyRuntimeDeltaToThread]);
  const getErrorMessageRef = useRef(getErrorMessage);
  useEffect(() => {
    getErrorMessageRef.current = getErrorMessage;
  }, [getErrorMessage]);
  const getRuntimeEventSeqRef = useRef(getRuntimeEventSeq);
  useEffect(() => {
    getRuntimeEventSeqRef.current = getRuntimeEventSeq;
  }, [getRuntimeEventSeq]);
  const mergeRuntimeEventRef = useRef(mergeRuntimeEvent);
  useEffect(() => {
    mergeRuntimeEventRef.current = mergeRuntimeEvent;
  }, [mergeRuntimeEvent]);
  const setThreadsRef = useRef(setThreads);
  useEffect(() => {
    setThreadsRef.current = setThreads;
  }, [setThreads]);
  // 目标线程最新值：判断「这条增量能否落到消息上」必须读线程状态（纯判定，
  // 不写状态）。经 ref 读取，避免把 selectedThread 放进订阅 effect 依赖——
  // 线程对象每次归并都是新引用，进依赖会掐断在途 SSE（见下方 sessionKey 注释）。
  const selectedThreadRef = useRef(selectedThread);
  useEffect(() => {
    selectedThreadRef.current = selectedThread;
  }, [selectedThread]);
  // 尾部优先：建连游标经 ref 读取（调用方每次 render 都会产出新箭头函数，
  // 不能进依赖，否则会掐断在途 SSE——与本文件其它回调同口径）。
  const getReplayCursorRef = useRef(getReplayCursor);
  useEffect(() => {
    getReplayCursorRef.current = getReplayCursor;
  }, [getReplayCursor]);
  const threadId = selectedThread?.id;
  const sessionId = selectedThread?.sessionId;
  // 连接键只由 sessionId + retryNonce 决定，**刻意不含 threadId**：
  // 启动期 `mergeRuntimeSessionsIntoThreads`（lib/thread-state/sessions.ts）会丢弃
  // 占位线程、按 sessionId 重新归并，threadId 在首屏一秒内会多次变化。把它放进
  // effect 依赖会让订阅反复卸载重建——实测大会话首屏 0.75s 内开了 6 条 SSE 连接、
  // 5 条被 cleanup abort（DevTools 里显示为 canceled/失败），并且可能在 dump 传输
  // 中途掐断连接，导致同一份事件日志被重拉。
  const sessionKey = sessionId ? `${sessionId}:${retryNonce}` : null;
  // 事件回填仍需最新 threadId：经 ref 读取，不进依赖。
  const threadIdRef = useRef(threadId);
  useEffect(() => {
    threadIdRef.current = threadId;
  }, [threadId]);
  // 只关心「有没有 threadId」这个布尔位：undefined → 有值 时补跑一次订阅，
  // 值本身变化（重键）不再重订阅。
  const hasThreadId = Boolean(threadId);
  const connectionStatus: ConnectionStatus =
    sessionKey === null
      ? "idle"
      : statusState?.key === sessionKey
        ? statusState.status
        : "connecting";

  useEffect(() => {
    if (!enabled || !hasThreadId || !sessionId || !sessionKey) {
      return;
    }

    const controller = new AbortController();

    // 运行时通道的**提交合帧**（2026-09-15）。chat 通道早已按最小间隔提交
    // （agent-chat-turn/streaming-frame.ts 的 MIN_COMMIT_INTERVAL_MS = 120），
    // 但本通道此前是「每条 SSE 事件一次 setThreads」：实测 e2e/zz-mount-audit.mjs
    // 单次流式回合（507 帧 / 16s）把页面级提交打到 59 次/秒（≈每帧一次），
    // MutationObserver 940 次回调、LayoutCount +945、ScriptDuration 7.3s——每次
    // 提交都要重跑消息列的 markdown 渲染，这就是「输出过程中整页卡住」的主因。
    // 这里复用同一个调度器：事件先入队，按最小间隔 + rAF 合并成一次提交。
    //
    // 关键约束：归属判定与去重 claim（deltaCoordinator.claim）**必须仍在事件
    // 到达时执行**——见 onEvent 内注释：updater 可能被 StrictMode / 并发渲染
    // 重放，claim 放进 updater 会吞掉去重键。因此入队的是已判定好的条目。
    const pendingRuntimeCommits: Array<{
      activeTurn: string;
      event: SessionRuntimeEvent;
      nextEvents: SessionRuntimeEvent[];
      shouldApplyLiveDelta: boolean;
    }> = [];

    const applyPendingRuntimeCommits = () => {
      if (pendingRuntimeCommits.length === 0) {
        return;
      }
      // 按到达顺序在同一次 reducer 里兑现：逐条 apply 的结果与「一条一次提交」
      // 完全一致（都是对最新 state 顺序应用），只是合并成一次页面级提交。
      const batch = pendingRuntimeCommits.splice(0, pendingRuntimeCommits.length);
      setThreadsRef.current((current) =>
        batch.reduce(
          (threads, item) =>
            threads.map((thread) => {
              if (thread.id !== threadIdRef.current) {
                return thread;
              }
              // 通道恢复：收到 stream 事件说明连接已恢复，清除由
              // stream 自身产生的降级标记（recovery 等其他来源的
              // lastError 保持，不被误清）。
              const recovered =
                thread.transport === "error" &&
                thread.lastError?.startsWith("Runtime stream failed")
                  ? { ...thread, transport: "live" as const, lastError: null }
                  : thread;
              // 方案B：请求进行中，打字机增量事件直接渲染到消息；
              // 否则只进事件快照（历史回放/reload 不误渲染），在途回合身份透传给桥接帧。
              // A durable event from another turn never mutates the
              // currently streaming assistant message.  The claim above
              // is intentionally shared by both transport paths.
              if (item.shouldApplyLiveDelta) {
                return applyRuntimeDeltaToThreadRef.current(
                  recovered,
                  item.event,
                  item.activeTurn || undefined,
                );
              }
              return applyRuntimeEventToThreadRef.current(
                recovered, sessionId, item.nextEvents, item.event, item.activeTurn || undefined,
              );
            }),
          current,
        ),
      );
    };

    const runtimeCommitScheduler = createStreamingFrameScheduler(
      applyPendingRuntimeCommits,
    );
    runtimeCommitScheduler.attachVisibilityListener();

    const sleep = (ms: number) =>
      new Promise<void>((resolve) => {
        const timer = setTimeout(resolve, ms);
        controller.signal.addEventListener(
          "abort",
          () => {
            clearTimeout(timer);
            resolve();
          },
          { once: true },
        );
      });

    void (async () => {
      // 方案B：常驻长轮询循环——单次连接结束（空流/断开/会话切换）后
      // 以 after 游标续传重连，保证请求进行中的增量事件始终可达。
      // 防抖：连续失败达到阈值才标记 thread error，偶发瞬断（循环重连
      // 场景的常见噪音）不会把 thread 粘滞在降级状态。
      let consecutiveFailures = 0;
      while (!controller.signal.aborted) {
        let streamFailed = false;
        try {
          await streamSessionRuntime(sessionId, {
            // 重试前以本地 last seq 拉齐：游标即本地已消费的最大 seq，
            // 手动重试与自动重连共用同一 after 语义（不重复消费 delta）。
            // 尾部优先：首个连接取 max(本 hook 已消费 seq, 轨迹已回放 seq)。
            // 窗口回放已把轨迹推进到 latest_seq，故这里通常直接跳过历史 dump，
            // 只订阅窗口之后的新事件；重连时 runtimeSeqRef 已经前进，max 保持
            // 「不重复消费」的既有语义。
            after: Math.max(
              runtimeSeqRef.current[sessionId] ?? 0,
              getReplayCursorRef.current?.() ?? 0,
            ),
            // P1-5 方案 2：父流订阅 live-only 事件——子会话 `subagent.progress`
            // 节流镜像与父会话自身 `tool.progress` 只在 `live=1` 时随 SSE 投递
            // （不落库、无持久化 seq，`trajectoryEventAction` 按 seq=0 即时应用）。
            live: true,
            pollMs: 500,
            signal: controller.signal,
            // P1-8：SSE 建连（响应头到达）即视为在线——后端该流是常驻长连接，
            // 空闲会话不产生任何事件；若只按「收到事件」判定，徽标会永久停在
            // 「连接中… 重试」，把健康连接误报成假故障。与日志流 onOpen→open
            // 同口径；成功建连同时打断「连续失败」计数（瞬断抖动不粘滞降级）。
            onOpen: () => {
              consecutiveFailures = 0;
              setStatusState((current) =>
                current?.key === sessionKey && current.status === "online"
                  ? current
                  : { key: sessionKey, status: "online" },
              );
            },
            onEvent: (event) => {
              // 收到事件 = 通道已恢复；重置连续失败计数。
              consecutiveFailures = 0;
              setStatusState((current) =>
                current?.key === sessionKey && current.status === "online"
                  ? current
                  : { key: sessionKey, status: "online" },
              );
              onTrajectoryEventRef.current?.(event);
              onRuntimeEventRef.current?.(event);

              const nextSeq = getRuntimeEventSeqRef.current(event);
              if (
                nextSeq > 0 &&
                nextSeq > (runtimeSeqRef.current[sessionId] ?? 0)
              ) {
                runtimeSeqRef.current[sessionId] = nextSeq;
              }

              const nextEvents = mergeRuntimeEventRef.current(
                runtimeEventsRef.current[sessionId] ?? [],
                event,
              );
              runtimeEventsRef.current[sessionId] = nextEvents;

              // Claim the shared provider identity outside the React state
              // updater. Functional updaters may be replayed by StrictMode or
              // concurrent rendering; making the claim there would consume a
              // dedupe key even when React discards the update.
              const deltaKind = getRuntimeDeltaKind(event.type);
              const activeTurn = activeTurnIdRef.current?.trim() ?? "";
              const eventTurn = getRuntimeEventTurnId(event);
              // 归属判定与 applyRuntimeDeltaToThread 共用同一语义（「未知」≠
              // 「其他 turn」）。严格相等会让缺 turn_id 的增量在 hook 层被整条
              // 丢弃——真后端 loop.go 仅在 turnID != "" 时注入 turn_id。
              const turnMatches = matchesActiveTurn(activeTurn, eventTurn);
              let shouldApplyLiveDelta = false;
              if (renderLiveDeltasRef.current && deltaKind && turnMatches) {
                // 先判定「这条增量到底能不能落到消息上」，再决定是否消费去重 key。
                // applyRuntimeDeltaToThread 在目标消息不是「当前正在 streaming 的
                // 那条」时会原样返回 thread（turn 身份不足/已定稿/没有可写目标）。
                // 此时若照旧 claim，key 就被吞掉：/api/agent/chat 通道随后拿到同一
                // key 会直接 return，两条通道互相让路，整段回复只能等 turn 结束由
                // 最终快照一次性定型（实测真后端两路各 ~1000 帧、DOM 全程不动，
                // 24s 时整块蹦出）。判定是纯函数调用，不产生副作用。
                const currentThread = selectedThreadRef.current;
                const willApply = Boolean(
                  currentThread &&
                    applyRuntimeDeltaToThreadRef.current(
                      currentThread,
                      event,
                      activeTurn || undefined,
                    ) !== currentThread,
                );
                const deltaKey = getRuntimeDeltaKeyFromEvent(event);
                const coordinator = deltaCoordinatorRef.current;
                shouldApplyLiveDelta =
                  Boolean(deltaKey) && coordinator
                    ? willApply && coordinator.claim(deltaKey)
                    : Boolean(deltaKey) || !coordinator;

                // live 通道（lib/live-stream-text.ts）：已 claim 的增量在事件到达时同步
                // 追加；此处只取副作用，写 store 仍由 applyPendingRuntimeCommits 兑现。
                if (shouldApplyLiveDelta && currentThread) {
                  applyRuntimeDeltaToThreadRef.current(
                    currentThread,
                    event,
                    activeTurn || undefined,
                    // live 通道副作用集中在 lib/live-stream-text.ts 的
                    // applyLiveStreamDelta（正文追加 / 推理追加 / 新推理块覆盖）。
                    (delta) => applyLiveStreamDelta(delta),
                  );
                }
              }

              // 入队 + 合帧提交（见上方 pendingRuntimeCommits 注释）。判定结果
              // （shouldApplyLiveDelta / activeTurn）在事件到达时固化，既保证
              // 「先判定后 claim」的既有语义，又避免把 claim 放进 updater 被重放。
              // 实际 apply 在 applyPendingRuntimeCommits 里按到达顺序兑现。
              pendingRuntimeCommits.push({
                activeTurn,
                event,
                nextEvents,
                shouldApplyLiveDelta,
              });
              runtimeCommitScheduler.schedule();
            },
            onErrorEvent: (payload) => {
              streamFailed = true;
              // 方案B 防抖：SSE error 事件同样计入连续失败，达到阈值才标记降级。
              consecutiveFailures += 1;
              const nextStatus: ConnectionStatus =
                consecutiveFailures >= STREAM_FAILURE_THRESHOLD
                  ? "offline"
                  : "reconnecting";
              setStatusState((current) =>
                current?.key === sessionKey && current.status === nextStatus
                  ? current
                  : { key: sessionKey, status: nextStatus },
              );
              const message =
                typeof payload.error === "string" && payload.error.trim()
                  ? payload.error.trim()
                  : "runtime stream reported an error";
              if (consecutiveFailures >= STREAM_FAILURE_THRESHOLD) {
                // 先兑现挂起的增量，保证降级标记写在最后（顺序与「一条一次提交」一致）。
                runtimeCommitScheduler.flush();
                setThreadsRef.current((current) =>
                  current.map((thread) =>
                    thread.id === threadIdRef.current
                      ? {
                          ...thread,
                          updatedAt: new Date().toISOString(),
                          transport: "error",
                          lastError: `Runtime stream failed: ${message}`,
                        }
                      : thread,
                  ),
                );
              }
            },
          });
        } catch (error) {
          if (controller.signal.aborted) {
            return;
          }
          streamFailed = true;
          const message = getErrorMessageRef.current(
            error,
            "failed to connect runtime stream",
          );
          consecutiveFailures += 1;
          const nextStatus: ConnectionStatus =
            consecutiveFailures >= STREAM_FAILURE_THRESHOLD
              ? "offline"
              : "reconnecting";
          setStatusState((current) =>
            current?.key === sessionKey && current.status === nextStatus
              ? current
              : { key: sessionKey, status: nextStatus },
          );
          if (consecutiveFailures >= STREAM_FAILURE_THRESHOLD) {
            // 同上：先冲刷挂起的增量，再写降级标记。
            runtimeCommitScheduler.flush();
            setThreadsRef.current((current) =>
              current.map((thread) =>
                thread.id === threadIdRef.current
                  ? {
                      ...thread,
                      updatedAt: new Date().toISOString(),
                      transport: "error",
                      lastError: `Runtime stream failed: ${message}`,
                    }
                  : thread,
              ),
            );
          }
        }
        if (controller.signal.aborted) {
          return;
        }
        // 退避重连：错误 1s；正常空流 2s（长轮询后端无空流——事件到达即返回；
        // mock/空后端下降低轮询频率，避免共享测试服务被高频请求拖慢）。
        // 状态不在这里回落：正常收流（服务端/代理按空闲超时关闭长连接）只说明
        // 本轮结束，连接是否仍然可用由下一轮 onOpen 继续验证；按「本轮有没有
        // 事件」回落 connecting 会让已在线会话在每次重连窗口闪回「连接中… 重试」
        // （代理超时拓扑下周期性假故障）。真正的断连在失败分支收敛为
        // reconnecting/offline。
        await sleep(streamFailed ? 1000 : 2000);
      }
    })();

    return () => {
      // 先冲刷再停表：已 claim 的 delta 若被丢弃，另一条通道会因为去重键已被
      // 消费而永久跳过这段文本（两条通道共享 deltaCoordinator 的 key）。
      runtimeCommitScheduler.flush();
      runtimeCommitScheduler.cancel();
      runtimeCommitScheduler.detachVisibilityListener();
      controller.abort();
    };
    // 依赖里只留会话身份：回调/setter 经上面的 ref 读取（保持最新引用但不参与
    // 依赖），否则任何父级 render 造成的引用抖动都会掐断在途 SSE。
  }, [retryNonce, sessionId, sessionKey, hasThreadId, enabled]);

  return { connectionStatus, retryConnection };
}
