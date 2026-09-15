/**
 * 对话面的会话历史**分页**（尾部优先）：首屏取最新一页（`/history` 不传
 * `before_seq` 即返回最新 limit 条），更早内容按需向前翻页——`before_seq` 是
 * 排他上界，用上一页返回的 `next_before_seq` 继续向前（与后端 /history 同语义）。
 *
 * 与轨迹窗口的区别：轨迹链路（lib/trajectory/recovery.ts）分页的是 EventStore
 * 事件、按 seq 前插到轨迹 store；这里分页的是**权威历史消息**，直接前插到线程
 * 消息列表（投影见 lib/thread-state/history-artifacts.ts）。
 */
import type { SessionHistoryResponse } from "@/lib/runtime-api";

export type SessionHistoryPagingState = {
  /**
   * 分页状态所属会话：会话切换后旧状态立即失效（`pagingForSession` 派生成空闲态，
   * 不需要 effect 清理，也不会把 A 会话的游标用到 B 会话上）。
   */
  sessionId: string | null;
  /** 还有更早一页（服务端 `has_more`）。 */
  hasMore: boolean;
  /** 「加载更早」在途：入口禁用，避免并发翻页。 */
  loading: boolean;
  /** 下一页的排他上界（`before_seq`）；0 表示没有可用游标（未拿到分页元数据）。 */
  nextBeforeSeq: number;
};

/**
 * 「加载更早」入口契约：按钮与触顶自动加载共用**同一条**幂等入口（在途 / 已到
 * 开头 / 无分页元数据时 `onLoadEarlier` 自身短路），宿主只透传状态即可。
 */
export type HistoryEarlierLoader = {
  /** 还有更早一页：决定入口是否渲染。 */
  hasMore: boolean;
  /** 在途：入口禁用并切到「正在加载」文案。 */
  loading: boolean;
  onLoadEarlier: () => void;
};

export const IDLE_HISTORY_PAGING: SessionHistoryPagingState = {
  sessionId: null,
  hasMore: false,
  loading: false,
  nextBeforeSeq: 0,
};

/** 「加载更早」每页条数：与后端 /history 的默认 limit（100）对齐。 */
export const HISTORY_OLDER_PAGE_SIZE = 100;

/**
 * 一次「加载更早」最多连续向前翻的页数。重同步会把游标退回「最新一页」的边界，
 * 于是首次点击可能与常住窗口整页重叠（解析后没有可见新增）：自动续翻，直到出现
 * 新增内容、翻到开头或达到上限，避免「点了没反应」；上限同时兜住死循环。
 */
export const HISTORY_OLDER_MAX_PAGES = 8;

/**
 * 从一页历史响应读出分页状态。缺省 / 非法字段一律回落成「已到开头」：宁可少显示
 * 入口，也不给一个点了必然空转的「加载更早」（旧后端不返回分页元数据即属此列）。
 */
export function resolveHistoryPaging(
  sessionId: string,
  response: Pick<SessionHistoryResponse, "has_more" | "next_before_seq">,
): SessionHistoryPagingState {
  const raw = Number(response.next_before_seq ?? 0);
  const nextBeforeSeq = Number.isFinite(raw) && raw > 0 ? Math.floor(raw) : 0;
  return {
    sessionId,
    hasMore: Boolean(response.has_more) && nextBeforeSeq > 0,
    loading: false,
    nextBeforeSeq,
  };
}

/** 本会话的分页状态；会话切换（或尚无会话）时回落空闲态，不把旧游标带过去。 */
export function pagingForSession(
  state: SessionHistoryPagingState,
  sessionId: string | undefined,
): SessionHistoryPagingState {
  if (!sessionId || state.sessionId !== sessionId) {
    return IDLE_HISTORY_PAGING;
  }
  return state;
}
