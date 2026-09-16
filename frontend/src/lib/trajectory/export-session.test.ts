// P2-7 / PR-1 第 1 步（第 4 条链路）：会话轨迹导出编排单测。
//
// 锁现状（改动前必须有意识地更新本用例）：
// - 分页：`after` 游标从 0 起、页满才继续，页不满（含空页）即终止；
// - 内容帧判据与轨迹恢复同源（`hasTrajectoryContentFrames`）：没有内容帧的
//   会话回退到会话历史投影，行 seq=0 追加在事件行之后；
// - 脱敏开关同时作用于 payload、文件名后缀与返回值；
// - 下载参数 = 最终 JSONL 文本 + 文件名（导出的唯一产物契约）。

import { beforeEach, describe, expect, it, vi } from "vitest";

import type { SessionRuntimeEvent } from "@/types/runtime";

const mocks = vi.hoisted(() => ({
  fetchSessionRuntimeEvents: vi.fn(),
  fetchSessionHistoryMessages: vi.fn(),
  downloadTrajectoryJsonl: vi.fn(),
}));

vi.mock("@/api/runtime/sessions", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/runtime/sessions")>();
  return {
    ...actual,
    fetchSessionRuntimeEvents: mocks.fetchSessionRuntimeEvents,
  };
});

vi.mock("@/lib/trajectory/history-fallback", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("@/lib/trajectory/history-fallback")>();
  return {
    ...actual,
    fetchSessionHistoryMessages: mocks.fetchSessionHistoryMessages,
  };
});

vi.mock("@/lib/trajectory/export", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("@/lib/trajectory/export")>();
  return { ...actual, downloadTrajectoryJsonl: mocks.downloadTrajectoryJsonl };
});

import { exportSessionTrajectoryJsonl } from "@/lib/trajectory/export-session";
import { TRAJECTORY_RECOVERY_PAGE_SIZE } from "@/lib/trajectory/recovery";

function event(
  type: string,
  seq: number,
  extra: Record<string, unknown> = {},
): SessionRuntimeEvent {
  return {
    type,
    timestamp: "2026-08-16T00:00:10Z",
    payload: { ...extra, seq },
  };
}

/** 生成一页内容帧（chat.sse.chunk，seq 连续递增）。 */
function contentPage(count: number, startSeq: number): SessionRuntimeEvent[] {
  return Array.from({ length: count }, (_, index) =>
    event("chat.sse.chunk", startSeq + index, {
      type: "text",
      content: `chunk-${startSeq + index}`,
    }),
  );
}

function downloadedJsonl(): string {
  expect(mocks.downloadTrajectoryJsonl).toHaveBeenCalledTimes(1);
  return mocks.downloadTrajectoryJsonl.mock.calls[0][0] as string;
}

function downloadedFilename(): string {
  return mocks.downloadTrajectoryJsonl.mock.calls[0][1] as string;
}

function parseLines(jsonl: string): Array<Record<string, unknown>> {
  return jsonl.split("\n").map((line) => JSON.parse(line));
}

beforeEach(() => {
  mocks.fetchSessionRuntimeEvents.mockReset();
  mocks.fetchSessionHistoryMessages.mockReset();
  mocks.downloadTrajectoryJsonl.mockReset();
});

describe("exportSessionTrajectoryJsonl 分页与产物", () => {
  it("页满继续、页不满终止，游标取页内最后一条持久化 seq", async () => {
    const firstPage = contentPage(TRAJECTORY_RECOVERY_PAGE_SIZE, 1);
    const lastSeq = TRAJECTORY_RECOVERY_PAGE_SIZE;
    mocks.fetchSessionRuntimeEvents
      .mockResolvedValueOnce({ events: firstPage })
      .mockResolvedValueOnce({ events: contentPage(2, lastSeq + 1) });

    const result = await exportSessionTrajectoryJsonl("session-1");

    expect(mocks.fetchSessionRuntimeEvents).toHaveBeenCalledTimes(2);
    expect(mocks.fetchSessionRuntimeEvents.mock.calls[0]).toEqual([
      "session-1",
      { after: 0, limit: TRAJECTORY_RECOVERY_PAGE_SIZE },
    ]);
    expect(mocks.fetchSessionRuntimeEvents.mock.calls[1]).toEqual([
      "session-1",
      { after: lastSeq, limit: TRAJECTORY_RECOVERY_PAGE_SIZE },
    ]);
    expect(result).toMatchObject({
      eventCount: TRAJECTORY_RECOVERY_PAGE_SIZE + 2,
      historyRowCount: 0,
      redacted: false,
    });
    // 有内容帧 → 不回退会话历史。
    expect(mocks.fetchSessionHistoryMessages).not.toHaveBeenCalled();

    const lines = parseLines(downloadedJsonl());
    expect(lines).toHaveLength(TRAJECTORY_RECOVERY_PAGE_SIZE + 2);
    // 行格式：{seq, ts, kind, payload}，payload 不含游标字段 seq。
    expect(lines[0]).toEqual({
      seq: 1,
      ts: "2026-08-16T00:00:10Z",
      kind: "chunk",
      payload: { type: "text", content: "chunk-1" },
    });
    expect(lines[lines.length - 1]).toMatchObject({
      seq: TRAJECTORY_RECOVERY_PAGE_SIZE + 2,
    });
    expect(downloadedFilename()).toMatch(
      /^trajectory-session-1-\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2}\.jsonl$/,
    );
  });

  it("空首页即终止：只拉一次、不推进游标、下载空文件", async () => {
    mocks.fetchSessionRuntimeEvents.mockResolvedValueOnce({ events: [] });
    mocks.fetchSessionHistoryMessages.mockResolvedValueOnce([]);

    const result = await exportSessionTrajectoryJsonl("session-empty");

    expect(mocks.fetchSessionRuntimeEvents).toHaveBeenCalledTimes(1);
    expect(mocks.fetchSessionHistoryMessages).toHaveBeenCalledWith(
      "session-empty",
    );
    expect(result).toMatchObject({ eventCount: 0, historyRowCount: 0 });
    expect(downloadedJsonl()).toBe("");
  });
});

describe("exportSessionTrajectoryJsonl 历史兜底与脱敏", () => {
  it("无内容帧：生命周期事件被过滤，历史投影行 seq=0 追加在事件行之后", async () => {
    mocks.fetchSessionRuntimeEvents.mockResolvedValueOnce({
      events: [event("session_start", 1, { status: "running" })],
    });
    mocks.fetchSessionHistoryMessages.mockResolvedValueOnce([
      { role: "user", content: "你好" },
      { role: "assistant", content: "收到" },
      { role: "system", content: "提示词脚手架" },
    ]);

    const result = await exportSessionTrajectoryJsonl("session-history");

    // eventCount 是「未过滤前的拉取总量」，历史行数单独计数。
    expect(result).toMatchObject({ eventCount: 1, historyRowCount: 2 });
    const lines = parseLines(downloadedJsonl());
    expect(lines.map((line) => line.kind)).toEqual(["user", "chunk"]);
    expect(lines.map((line) => line.seq)).toEqual([0, 0]);
    expect(lines[0].payload).toMatchObject({ content: "你好" });
    expect(lines[1].payload).toMatchObject({ content: "收到" });
    expect(typeof lines[0].ts).toBe("string");
  });

  it("有内容帧：即使历史非空也不回退（导出即轨迹视图内容）", async () => {
    mocks.fetchSessionRuntimeEvents.mockResolvedValueOnce({
      events: [event("chat.sse.chunk", 1, { type: "text", content: "hi" })],
    });
    mocks.fetchSessionHistoryMessages.mockResolvedValueOnce([
      { role: "user", content: "只在历史里" },
    ]);

    const result = await exportSessionTrajectoryJsonl("session-content");

    expect(mocks.fetchSessionHistoryMessages).not.toHaveBeenCalled();
    expect(result.historyRowCount).toBe(0);
    expect(parseLines(downloadedJsonl())).toHaveLength(1);
  });

  it("脱敏导出：掩码工具参数、文件名带 -redacted 后缀", async () => {
    mocks.fetchSessionRuntimeEvents.mockResolvedValueOnce({
      events: [
        event("chat.sse.tool_start", 1, {
          tool: { id: "tool-1", name: "web_search", args: { query: "paris" } },
        }),
      ],
    });

    const result = await exportSessionTrajectoryJsonl("session-redact", {
      redact: true,
    });

    const lines = parseLines(downloadedJsonl());
    expect(lines[0].payload).toMatchObject({
      tool: {
        id: "tool-1",
        name: "web_search",
        args: { query: "<redacted>" },
      },
    });
    expect(result.redacted).toBe(true);
    expect(downloadedFilename()).toMatch(
      /^trajectory-session-redact-redacted-\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2}\.jsonl$/,
    );
  });
});
