/**
 * 会话事件 / 历史的**窗口读取**（尾部优先回放，P3-1 演进）：
 * - `getSessionHistory`：持久化会话历史（P4 历史兜底投影的数据源）；
 * - `fetchSessionRuntimeEvents`：EventStore 事件，支持两种语义——增量续拉
 *   （`after` 升序分页）与尾部优先窗口（`tail` / `beforeSeq` 取一页 + 游标），
 *   后者是首屏「只回放最近的消息、更早内容按需向前翻页」的数据源。
 *
 * 从 `./sessions` 拆出（P0-2 单文件 ≤ 500 非空行）：`sessions.ts` 以 barrel
 * 形式 re-export，调用方路径与既有 `vi.mock("@/api/runtime/sessions")` 不变。
 */
import type { SessionHistoryResponse, SessionRuntimeEvent } from "@/types/runtime";

import { buildRuntimeUrlWithQuery, fetchRuntimeJson } from "./shared";

export type SessionHistoryQuery = {
  /** 每页条数（后端默认 100、上限 1000）。 */
  limit?: number;
  /** 向前翻页游标（后端 `before_seq`；缺省取最新一页）。 */
  beforeSeq?: number;
};

export async function getSessionHistory(
  sessionId: string,
  query: SessionHistoryQuery = {},
): Promise<SessionHistoryResponse> {
  return fetchRuntimeJson<SessionHistoryResponse>(
    buildRuntimeUrlWithQuery(
      `/api/runtime/sessions/${encodeURIComponent(sessionId)}/history`,
      { limit: query.limit, before_seq: query.beforeSeq },
    ),
    {
      headers: {
        Accept: "application/json",
      },
    },
  );
}

export type RuntimeSessionEventsResponse = {
  events: SessionRuntimeEvent[];
  count: number;
  latest_seq: number;
  /**
   * 尾部优先窗口（`tail=1` / `before_seq=N`）才有的分页元数据：
   * `first_seq` 为本次返回的最早 seq（同时是下一页的 `next_before_seq`），
   * `has_more` 表示还有更早的一页。`after` 升序续拉时不返回这些字段。
   */
  first_seq?: number;
  last_seq?: number;
  has_more?: boolean;
  next_before_seq?: number;
};

export type RuntimeSessionEventsQuery = {
  after?: number;
  limit?: number;
  /** 尾部优先：取最新一页（等价于 before_seq=+∞）。 */
  tail?: boolean;
  /** 尾部优先：取 seq < beforeSeq 的上一页（排他上界，与 /history 同语义）。 */
  beforeSeq?: number;
};

/**
 * 拉取会话事件：
 * - 增量续拉（P3-1/P3-2）：`after=已收最大 seq`，升序、limit 分页；
 * - 尾部优先窗口（tail-first 回放）：`tail` / `beforeSeq` 二选一，返回升序的
 *   一页 + `has_more` / `next_before_seq`，供首屏只回放「最近的消息」并按需
 *   向前翻页（大会话实测 2288 条 / 2.25MB，全量重放是首屏 long task 的主要来源）。
 */
export async function fetchSessionRuntimeEvents(
  sessionId: string,
  query: RuntimeSessionEventsQuery = {},
): Promise<RuntimeSessionEventsResponse> {
  return fetchRuntimeJson<RuntimeSessionEventsResponse>(
    buildRuntimeUrlWithQuery(
      `/api/runtime/sessions/${encodeURIComponent(sessionId)}/runtime/events`,
      {
        after: query.after,
        limit: query.limit,
        tail: query.tail ? 1 : undefined,
        before_seq: query.beforeSeq,
      },
    ),
    {
      headers: {
        Accept: "application/json",
      },
    },
  );
}
