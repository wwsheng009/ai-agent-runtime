import { describe, expect, it } from "vitest";

import {
  HISTORY_OLDER_PAGE_SIZE,
  IDLE_HISTORY_PAGING,
  pagingForSession,
  resolveHistoryPaging,
} from "@/lib/thread-state/history-paging";

describe("resolveHistoryPaging", () => {
  it("reads has_more + next_before_seq from the page", () => {
    expect(
      resolveHistoryPaging("session-1", { has_more: true, next_before_seq: 42 }),
    ).toEqual({
      sessionId: "session-1",
      hasMore: true,
      loading: false,
      nextBeforeSeq: 42,
    });
  });

  it("falls back to 'no earlier page' when the response has no paging metadata", () => {
    // 旧后端 / 非窗口模式响应不带分页字段：宁可少一个入口，也不给点了必然空转的按钮。
    expect(resolveHistoryPaging("session-1", {})).toEqual({
      sessionId: "session-1",
      hasMore: false,
      loading: false,
      nextBeforeSeq: 0,
    });
    expect(resolveHistoryPaging("session-1", { has_more: false, next_before_seq: 7 })).toEqual({
      sessionId: "session-1",
      hasMore: false,
      loading: false,
      nextBeforeSeq: 7,
    });
    // has_more=true 但没有游标 → 无法向前翻，同样按「到头」处理。
    expect(
      resolveHistoryPaging("session-1", { has_more: true, next_before_seq: 0 }),
    ).toEqual({
      sessionId: "session-1",
      hasMore: false,
      loading: false,
      nextBeforeSeq: 0,
    });
  });

  it("defaults the older page size to the backend /history default", () => {
    expect(HISTORY_OLDER_PAGE_SIZE).toBe(100);
  });
});

describe("pagingForSession", () => {
  it("returns the state only for the session that produced it", () => {
    const state = { sessionId: "session-1", hasMore: true, loading: false, nextBeforeSeq: 42 };
    expect(pagingForSession(state, "session-1")).toBe(state);
  });

  it("drops a stale cursor when the session switches (or is missing)", () => {
    const state = { sessionId: "session-1", hasMore: true, loading: false, nextBeforeSeq: 42 };
    // 会话切换后旧游标不能带到新会话上（否则「加载更早」会去翻别的会话）。
    expect(pagingForSession(state, "session-2")).toBe(IDLE_HISTORY_PAGING);
    expect(pagingForSession(state, undefined)).toBe(IDLE_HISTORY_PAGING);
    expect(pagingForSession(IDLE_HISTORY_PAGING, "session-1")).toBe(IDLE_HISTORY_PAGING);
  });
});
