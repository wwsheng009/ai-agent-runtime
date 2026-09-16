// Batch 1（PR-1）导出对照用例（方案 §7 / §4 前置条件 2）。
//
// 同一轮回合分别以「旧写入」（含 `chat.sse.chunk` / `chat.sse.reasoning` / 同源
// `chat.sse.observation` 与内嵌 `done.result`）与「新写入」（去重后：chunk/reasoning/
// observation 只走 wire、`done.result` 落盘裁剪）导出 JSONL，断言共有行逐字段等价，
// 并把差异固化为显式清单（而不是让差异悄悄漂移）。
//
// 本用例锁定的差异清单：
// 1. 新导出不再出现 chunk / reasoning / observation 行（旧导出里它们是回放内容源）；
// 2. `done` 行的 `payload.result` 在新导出中缺失（旧导出有）；
// 3. 其余共有行（meta / tool_end / result）payload 逐字段等价（seq 是游标，允许不同）；
// 4. bus 侧增量事件（`assistant_delta` / `assistant.reasoning`）不进导出——
//    导出口径只认 `chat.sse.*`；轨迹视图的「内容帧」判定不依赖这些行
//    （`recovery.isTrajectoryContentEvent` 同时接受 bus 侧增量事件），因此 UI 投影不变；
//    若后续要求导出文件也保留助手正文，需在 export 侧纳入 bus 侧内容事件（独立 PR）。

import { describe, expect, it } from "vitest";

import { eventsToTrajectoryJsonl } from "@/lib/trajectory/export";
import type { SessionRuntimeEvent } from "@/types/runtime";

type ExportRow = {
  seq: number;
  ts: string;
  kind: string;
  payload: Record<string, unknown>;
};

function chatEvent(
  kind: string,
  seq: number,
  payload: Record<string, unknown> = {},
): SessionRuntimeEvent {
  return {
    type: `chat.sse.${kind}`,
    timestamp: "2026-09-16T08:00:00Z",
    payload: { ...payload, seq },
  };
}

/** 旧写入：内容帧与同源 observation 都落盘，done 内嵌完整 result。 */
const oldWrites: SessionRuntimeEvent[] = [
  chatEvent("meta", 1, { kind: "llm" }),
  chatEvent("reasoning", 2, { content: "thinking" }),
  chatEvent("chunk", 3, { type: "text", content: "hello " }),
  chatEvent("observation", 4, { tool: "read_file", content: "tool copy" }),
  chatEvent("tool_end", 5, { tool: "read_file", status: "completed" }),
  chatEvent("chunk", 6, { type: "text", content: "world" }),
  chatEvent("result", 7, { content: "hello world" }),
  chatEvent("done", 8, {
    status: "completed",
    content: "hello world",
    result: { content: "hello world" },
  }),
];

/** 新写入：chunk/reasoning/同源 observation 只走 wire；done.result 落盘裁剪。 */
const newWrites: SessionRuntimeEvent[] = [
  chatEvent("meta", 1, { kind: "llm" }),
  chatEvent("tool_end", 2, { tool: "read_file", status: "completed" }),
  chatEvent("result", 3, { content: "hello world" }),
  chatEvent("done", 4, { status: "completed", content: "hello world" }),
];

function exportRows(events: SessionRuntimeEvent[]): ExportRow[] {
  const jsonl = eventsToTrajectoryJsonl(events);
  return jsonl.split("\n").map((line) => JSON.parse(line) as ExportRow);
}

function rowByKind(rows: ExportRow[], kind: string): ExportRow {
  const row = rows.find((item) => item.kind === kind);
  expect(row, `missing exported row: ${kind}`).toBeDefined();
  return row as ExportRow;
}

describe("Batch 1 去重后的导出差异清单", () => {
  it("共有行 payload 逐字段等价（seq 为游标，允许不同）", () => {
    const oldRows = exportRows(oldWrites);
    const newRows = exportRows(newWrites);

    for (const kind of ["meta", "tool_end", "result"]) {
      expect(rowByKind(newRows, kind).payload).toEqual(
        rowByKind(oldRows, kind).payload,
      );
    }
  });

  it("新导出不再包含 chunk / reasoning / observation 行", () => {
    const oldKinds = new Set(exportRows(oldWrites).map((row) => row.kind));
    const newKinds = new Set(exportRows(newWrites).map((row) => row.kind));

    expect([...newKinds].sort()).toEqual(["done", "meta", "result", "tool_end"]);
    const removed = [...oldKinds].filter((kind) => !newKinds.has(kind)).sort();
    expect(removed).toEqual(["chunk", "observation", "reasoning"]);
  });

  it("done 行的唯一字段差异是 result（显式差异清单）", () => {
    const oldDone = rowByKind(exportRows(oldWrites), "done");
    const newDone = rowByKind(exportRows(newWrites), "done");

    expect(Object.keys(oldDone.payload)).toContain("result");
    expect(Object.keys(newDone.payload)).not.toContain("result");
    const removedKeys = Object.keys(oldDone.payload).filter(
      (key) => !(key in newDone.payload),
    );
    expect(removedKeys).toEqual(["result"]);
    const addedKeys = Object.keys(newDone.payload).filter(
      (key) => !(key in oldDone.payload),
    );
    expect(addedKeys).toEqual([]);
    expect(newDone.payload.status).toBe(oldDone.payload.status);
    expect(newDone.payload.content).toBe(oldDone.payload.content);
  });

  it("bus 侧增量事件不进导出（导出口径只认 chat.sse.*）", () => {
    const rows = exportRows([
      {
        type: "assistant_delta",
        timestamp: "2026-09-16T08:00:00Z",
        payload: { seq: 1, content: "hello " },
      },
      chatEvent("done", 2, { status: "completed" }),
    ]);

    expect(rows.map((row) => row.kind)).toEqual(["done"]);
  });
});
