// P0-2 随源拆分：权威历史投影下「实时过程区」的存活回归（从 history-projection.test.ts 拆出，
// 控制单文件 500 非空行门禁）。
import { describe, expect, it } from "vitest";

import { applySessionHistoryToThread } from "@/lib/workspace-thread-state";
import type { SessionHistoryResponse } from "@/types/runtime";
import { createThread } from "./test-fixtures";

describe("applySessionHistoryToThread（实时推理分块）", () => {
  it("keeps per-block live reasoning rows when authoritative history matches an in-flight message", () => {
    // 回归（§12.1.5）：历史落盘的推理是整轮合并后的一段，且没有逐帧顺序信息。
    // 快照重建命中在途消息时若用它覆盖，界面上的「推理 → 工具 → 推理」会被压成
    // 一段，正在增长的块也会丢掉 running 标记。
    const response: SessionHistoryResponse = {
      session_id: "session-1",
      count: 1,
      history: [
        {
          role: "assistant",
          content: "结论：入口文件共 42 行。",
          metadata: {
            message_id: "assistant-existing",
            reasoning_details: {
              visibility: "visible",
              content: "先看入口。再看出口。",
            },
          },
        },
      ],
    };
    const thread = createThread();
    thread.messages[0] = {
      ...thread.messages[0],
      streaming: true,
      segments: [
        { type: "reasoning", content: "先看入口。", running: true },
        {
          type: "tool",
          toolCallId: "call-1",
          name: "read_file",
          status: "finished",
        },
        { type: "reasoning", content: "再看出口。" },
        { type: "text", content: "结论：入口文件共 42 行。" },
      ],
    };

    const nextThread = applySessionHistoryToThread(thread, response);

    expect(nextThread.messages).toHaveLength(1);
    expect(nextThread.messages[0].segments).toEqual([
      { type: "reasoning", content: "先看入口。", running: true },
      {
        type: "tool",
        toolCallId: "call-1",
        name: "read_file",
        status: "finished",
      },
      { type: "reasoning", content: "再看出口。" },
      { type: "text", content: "结论：入口文件共 42 行。" },
    ]);
  });
});
