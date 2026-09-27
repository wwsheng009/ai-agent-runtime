import {
  type Dispatch,
  type SetStateAction,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";

import { type Thread } from "@/data/mock";
import {
  getSessionHistory,
  getSessionRuntimeState,
  type SessionHistoryResponse,
} from "@/lib/runtime-api";
import { prependSessionHistoryToThread } from "@/lib/thread-state";
import {
  HISTORY_OLDER_MAX_PAGES,
  HISTORY_OLDER_PAGE_SIZE,
  IDLE_HISTORY_PAGING,
  pagingForSession,
  resolveHistoryPaging,
  type HistoryEarlierLoader,
  type SessionHistoryPagingState,
} from "@/lib/thread-state/history-paging";

import { buildRuntimeEventReloadKey } from "./runtime-checkpoints/reload-keys";

/**
 * 降级态收敛时，若服务端仍有在途回合，则以该周期复查 `/runtime` 快照，直到回合
 * 收尾再去拉权威历史。只在「已判降级 + 本地空闲」的窗口内运行，且每段降级至多
 * 一条等待链，不会对正常路径产生额外请求。
 */
const DEGRADED_RECONCILE_PROBE_MS = 3000;

type SessionHistorySyncOptions = {
  applySessionHistoryToThread: (
    thread: Thread,
    response: SessionHistoryResponse,
  ) => Thread;
  isResponding: boolean;
  lastRuntimeEventType?: string;
  runtimeEventCount?: number;
  selectedThread: Thread | undefined;
  setThreads: Dispatch<SetStateAction<Thread[]>>;
};

/**
 * 会重写服务端会话历史的运行时事件：
 * - rewind_finished：检查点还原（模式含会话时截断对话）
 * - backtrack_finished：会话回溯
 * 两者都会让本地消息列表过期，必须重新拉取权威历史，否则界面停留在回滚前的内容。
 */
const HISTORY_REWRITE_EVENT_TYPES = new Set(["rewind_finished", "backtrack_finished"]);

export function shouldReloadHistoryForRuntimeEvent(lastRuntimeEventType?: string) {
  return Boolean(
    lastRuntimeEventType && HISTORY_REWRITE_EVENT_TYPES.has(lastRuntimeEventType),
  );
}

export function shouldSyncSessionHistory(
  selectedThread: Thread | undefined,
  isResponding: boolean,
) {
  return Boolean(
    selectedThread?.sessionId &&
      !isResponding &&
      selectedThread.transport !== "error",
  );
}

/**
 * 降级态收敛判据（断流自愈的最后一段）：传输已判降级（`transport === "error"`）
 * 且不再有任何在途回合时，必须再拉一次权威历史，把本地投影整块对齐。
 *
 * 缺口背景（用户可见）：回合中途 SSE 断开时，事件通道最多只能把已落库的增量补到
 * 断点附近；尾部（以及只在 chat 通道收口的定型文本）不会再来。而增量渲染闸门
 * `renderLiveDeltas` 只在「回合进行中」打开，回合结束后重放的增量会被整帧丢弃——
 * 于是原地标签页永久停在截断前缀上。对照实验：同一会话另开页签（直接读 `/history`）
 * 内容完整，原地页签却永远缺尾巴。
 *
 * 与 `shouldSyncSessionHistory` 的分工：常规同步在健康态负责「回合结束后对齐」；
 * 本判据只在降级态兜底，并由调用方按「每段降级只自动尝试一次」去重（失败后交还
 * 连接徽标的「重试」），避免对真正宕机的后端形成重试风暴。
 *
 * 注意 `isResponding` 必须是**本地在途回合 ∪ 服务端在途回合**：chat POST 被掐断后
 * 本地立即空闲，但服务端回合仍在跑（`resume_on_disconnect`），此时拉到的历史还不含
 * 最终消息，过早对齐会「清了降级却留着截断」。
 */
export function shouldReconcileDegradedThread(
  selectedThread: Thread | undefined,
  isResponding: boolean,
) {
  if (
    !selectedThread?.sessionId ||
    isResponding ||
    selectedThread.transport !== "error"
  ) {
    return false;
  }
  // 降级成因就是「历史自己的请求失败」时不再自动探活：立刻重拉只会变成重试风暴，
  // 这类失败交还连接徽标的「重试」（语义与 P1-8 一致）。
  const message = (selectedThread.lastError ?? "").trim();
  return !HISTORY_SELF_FAILURE_PREFIXES.some((prefix) =>
    message.startsWith(prefix),
  );
}

/** 历史自身请求失败的降级原因前缀（见 `shouldReconcileDegradedThread`）。 */
const HISTORY_SELF_FAILURE_PREFIXES = [
  "Session history load failed:",
  "Session history sync failed:",
  "Session history recovery failed:",
] as const;

export function useSessionHistorySync({
  applySessionHistoryToThread,
  isResponding,
  lastRuntimeEventType,
  runtimeEventCount,
  selectedThread,
  setThreads,
}: SessionHistorySyncOptions) {
  const threadId = selectedThread?.id;
  const sessionId = selectedThread?.sessionId;
  const threadTransport = selectedThread?.transport;
  const [paging, setPaging] = useState<SessionHistoryPagingState>(IDLE_HISTORY_PAGING);
  // 「加载更早」要读最新线程，但入口回调的标识最好稳定（下游 memo 才有效）：
  // 用 ref 承载每帧最新线程，回调依赖里就只剩会话 / setter 与分页状态。
  const threadRef = useRef<Thread | undefined>(selectedThread);
  threadRef.current = selectedThread;
  // 在途标志：状态提交前的连点（滚动与按钮可能同帧触发）由它兜住，保证一次只翻一页。
  const loadingEarlierRef = useRef(false);
  // 会话切换后旧游标立即失效（派生空闲态，不需要 effect 清理）。
  const historyPaging = pagingForSession(paging, sessionId);
  const historyRewriteEventKey = shouldReloadHistoryForRuntimeEvent(lastRuntimeEventType)
    ? buildRuntimeEventReloadKey(lastRuntimeEventType, runtimeEventCount)
    : "";

  const syncHistory = useCallback(
    async (isCancelled: () => boolean) => {
      if (!sessionId || !threadId) {
        return;
      }
      try {
        const response = await getSessionHistory(sessionId);
        if (isCancelled()) {
          return;
        }
        setThreads((current) =>
          current.map((thread) =>
            thread.id === threadId
              ? applySessionHistoryToThread(thread, response)
              : thread,
          ),
        );
        // 同步拿到的是「最新一页」：分页游标随之退回这一页的边界，
        // 「加载更早」永远从这里向前翻（与轨迹窗口同一套尾部优先语义）。
        setPaging(resolveHistoryPaging(sessionId, response));
      } catch (error) {
        if (isCancelled()) {
          return;
        }

        const message =
          error instanceof Error ? error.message : "failed to load session history";
        setThreads((current) =>
          current.map((thread) =>
            thread.id === threadId
              ? {
                  ...thread,
                  updatedAt: new Date().toISOString(),
                  transport: "error",
                  lastError: `Session history sync failed: ${message}`,
                }
              : thread,
          ),
        );
      }
    },
    [applySessionHistoryToThread, sessionId, setThreads, threadId],
  );

  useEffect(() => {
    if (
      !sessionId ||
      !threadId ||
      isResponding ||
      threadTransport === "error"
    ) {
      return;
    }

    let cancelled = false;

    void syncHistory(() => cancelled);

    return () => {
      cancelled = true;
    };
  }, [isResponding, sessionId, syncHistory, threadId, threadTransport]);

  /**
   * P1-8 手动恢复：连接徽标「重试」在会话已降级（`transport === "error"`）时的
   * 第二段动作。自动同步在降级期间被跳过（见 `shouldSyncSessionHistory`），
   * 直连 chat / 轨迹恢复等非会话流来源的降级因此只能等下一条 chat 回合的 meta
   * 事件清除——空闲会话上点「重试」看不到任何变化，按钮形同失效。
   * 这里用一次权威历史拉取做真实探活：成功即证明运行时可达且本地投影已对齐，
   * 收敛回在线；失败则如实写入恢复失败原因（不静默、不假装恢复）。
   */
  const recoverSessionHistory = useCallback(async () => {
    if (!sessionId || !threadId) {
      return false;
    }

    try {
      const response = await getSessionHistory(sessionId);
      setThreads((current) =>
        current.map((thread) =>
          thread.id === threadId
            ? {
                ...applySessionHistoryToThread(thread, response),
                transport: "live" as const,
                lastError: null,
              }
            : thread,
        ),
      );
      return true;
    } catch (error) {
      const message =
        error instanceof Error ? error.message : "failed to recover session history";
      setThreads((current) =>
        current.map((thread) =>
          thread.id === threadId
            ? {
                ...thread,
                updatedAt: new Date().toISOString(),
                transport: "error" as const,
                lastError: `Session history recovery failed: ${message}`,
              }
            : thread,
        ),
      );
      return false;
    }
  }, [applySessionHistoryToThread, sessionId, setThreads, threadId]);

  // 降级态收敛（断流自愈）：见 shouldReconcileDegradedThread 的缺口说明。
  //
  // 两道闸门缺一不可：
  //   1) 「每段降级只自动尝试一次」——成功恢复会把线程拉回 live 并复位哨兵，下一次
  //      真的再降级仍会自动收敛；失败则保持 error + 哨兵（人工入口是徽标「重试」），
  //      避免对宕机后端形成重试风暴；
  //   2) 权威在途闸门——`isResponding` 只看得到本地回合：chat POST 被掐断后本地
  //      立即空闲，而服务端回合仍在跑（`resume_on_disconnect`），此时历史里还没有
  //      最终消息。过早对齐会「清了降级却留下截断」，且增量游标已经越过被丢弃的
  //      尾部帧，事后再也补不回来。因此先探 `/runtime` 的 `active_turn`，有回合就
  //      等它收尾（降级期小步轮询），没有才真正拉历史。
  const degradedSessionRef = useRef<string>("");
  const degradedTimerRef = useRef<number | null>(null);
  const clearDegradedTimer = useCallback(() => {
    if (degradedTimerRef.current !== null) {
      window.clearTimeout(degradedTimerRef.current);
      degradedTimerRef.current = null;
    }
  }, []);
  useEffect(() => () => clearDegradedTimer(), [clearDegradedTimer]);

  const degradedTransport = selectedThread?.transport;
  const shouldReconcileDegraded = shouldReconcileDegradedThread(
    selectedThread,
    isResponding,
  );
  useEffect(() => {
    if (!sessionId) {
      return;
    }
    if (degradedTransport !== "error") {
      degradedSessionRef.current = "";
      clearDegradedTimer();
      return;
    }
    if (!shouldReconcileDegraded) {
      return;
    }
    if (degradedSessionRef.current === sessionId) {
      return;
    }
    degradedSessionRef.current = sessionId;

    let cancelled = false;
    const attempt = async () => {
      if (cancelled) {
        return;
      }
      let serverTurnActive = false;
      try {
        const snapshot = await getSessionRuntimeState(sessionId);
        serverTurnActive = Boolean(snapshot?.activeTurn);
      } catch {
        // 快照不可达：不拦着收敛，让历史探活自己给出失败原因（不静默）。
        serverTurnActive = false;
      }
      if (cancelled) {
        return;
      }
      if (serverTurnActive) {
        clearDegradedTimer();
        degradedTimerRef.current = window.setTimeout(() => {
          void attempt();
        }, DEGRADED_RECONCILE_PROBE_MS);
        return;
      }
      clearDegradedTimer();
      void recoverSessionHistory();
    };
    void attempt();
    return () => {
      cancelled = true;
    };
  }, [
    clearDegradedTimer,
    degradedTransport,
    recoverSessionHistory,
    sessionId,
    shouldReconcileDegraded,
  ]);

  /**
   * 「加载更早」（对话面）：按 `before_seq` 向前翻权威历史并前插到消息列表。
   * - 幂等：在途 / 已到开头 / 本会话尚无分页元数据时直接返回——按钮与滚到顶端
   *   会自动触发同一条入口，靠 `loadingEarlierRef` 与 `loading` 双保险防并发翻页。
   * - 续翻：重同步会把游标退回「最新一页」的边界，于是首次点击可能与常住窗口
   *   整页重叠（解析后没有可见新增）；此时带着新游标继续向前，直到出现新增内容、
   *   翻到开头或达到上限（`HISTORY_OLDER_MAX_PAGES`），避免「点了没反应」。
   * - 失败：游标原地不动（原样可重试），线程标记降级并写出可读原因——与
   *   `syncHistory` 同一套「不静默吞错」语义，恢复仍走连接徽标的「重试」。
   */
  const loadEarlier = useCallback(async () => {
    const thread = threadRef.current;
    if (!sessionId || !thread || thread.id !== threadId) {
      return;
    }
    if (!historyPaging.hasMore || historyPaging.loading || loadingEarlierRef.current) {
      return;
    }

    loadingEarlierRef.current = true;
    let cursor = historyPaging.nextBeforeSeq;
    // 显式 boolean：上面的 `!historyPaging.hasMore` 守卫会把该属性收窄成字面量
    // `true`，不标注就会让 `hasMore` 继承字面量类型、下一行赋值报 TS2322。
    let hasMore: boolean = historyPaging.hasMore;
    setPaging((current) => ({ ...current, sessionId, loading: true }));

    try {
      for (let page = 0; page < HISTORY_OLDER_MAX_PAGES; page += 1) {
        if (!hasMore || cursor <= 0) {
          break;
        }
        const response = await getSessionHistory(sessionId, {
          beforeSeq: cursor,
          limit: HISTORY_OLDER_PAGE_SIZE,
        });
        cursor = Number(response.next_before_seq ?? 0);
        hasMore = Boolean(response.has_more) && cursor > 0;
        const target = threadRef.current;
        if (!target || target.id !== threadId) {
          break;
        }
        const merged = prependSessionHistoryToThread(target, response);
        if (merged === target) {
          // 整页都与常住窗口重叠：没有可见新增，带着新游标继续向前翻。
          continue;
        }
        setThreads((current) =>
          current.map((item) => (item.id === threadId ? merged : item)),
        );
        break;
      }
      setPaging({ sessionId, hasMore, loading: false, nextBeforeSeq: cursor });
    } catch (error) {
      const message =
        error instanceof Error ? error.message : "failed to load earlier history";
      setPaging((current) => ({ ...current, loading: false }));
      setThreads((current) =>
        current.map((item) =>
          item.id === threadId
            ? {
                ...item,
                updatedAt: new Date().toISOString(),
                transport: "error" as const,
                lastError: `Session history load failed: ${message}`,
              }
            : item,
        ),
      );
    } finally {
      loadingEarlierRef.current = false;
    }
  }, [historyPaging, sessionId, setThreads, threadId]);

  // 入口契约：引用稳定，宿主直接透传给消息列表（按钮与触顶自动加载共用）。
  const earlierLoader = useMemo<HistoryEarlierLoader>(
    () => ({
      hasMore: historyPaging.hasMore,
      loading: historyPaging.loading,
      onLoadEarlier: () => {
        void loadEarlier();
      },
    }),
    [historyPaging.hasMore, historyPaging.loading, loadEarlier],
  );

  // 事件驱动的重放同步：回滚事件（检查点还原 / 会话回溯）到达后立刻重建消息列表。
  // 依赖事件键（类型 + 计数），因此同一事件只同步一次、连续回滚各自同步一次。
  useEffect(() => {
    if (!historyRewriteEventKey || !sessionId || !threadId) {
      return;
    }
    if (isResponding || threadTransport === "error") {
      return;
    }

    let cancelled = false;

    void syncHistory(() => cancelled);

    return () => {
      cancelled = true;
    };
  }, [
    historyRewriteEventKey,
    isResponding,
    sessionId,
    syncHistory,
    threadId,
    threadTransport,
  ]);

  return { earlierLoader, recoverSessionHistory };
}
