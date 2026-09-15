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
