// 工具回执投影：历史 role="tool" → tool segment（名称/入参/明细/结果），
// 供消息列表渲染为 24px 工具行而不是通用「上下文注入」行。
import { describe, expect, it } from "vitest";

import { type ChatMessage } from "@/data/mock";
import { type SessionHistoryMessage } from "@/types/runtime";

import { HISTORY_TOOL_RESULT_LIMIT, mapSessionHistoryToMessages } from "./history-mapping";

function toolSegmentOf(message: ChatMessage) {
  const segment = message.segments.find((item) => item.type === "tool");
  if (!segment || segment.type !== "tool") {
    throw new Error("expected a tool segment");
  }
  return segment;
}

describe("history tool receipts", () => {
  it("配对 tool_calls 回填工具名/入参/文件明细与结果", () => {
    const history: SessionHistoryMessage[] = [
      {
        role: "assistant",
        content: "",
        tool_calls: [
          {
            id: "call-1",
            name: "read_file",
            arguments: { file_path: "frontend/src/app.tsx" },
          },
        ],
      },
      {
        role: "tool",
        content: "export const app = true;",
        tool_call_id: "call-1",
      },
    ];

    const [, receipt] = mapSessionHistoryToMessages("session-1", history, []);
    const segment = toolSegmentOf(receipt.message);

    expect(receipt.message.label).toBe("tool");
    expect(receipt.message.author).toBe("Tool receipt");
    expect(receipt.message.role).toBe("assistant");
    expect(segment.toolCallId).toBe("call-1");
    expect(segment.name).toBe("read_file");
    expect(segment.status).toBe("finished");
    expect(segment.argsSummary).toContain("file_path");
    expect(segment.details?.filePath).toBe("frontend/src/app.tsx");
    expect(segment.resultSummary).toBe("export const app = true;");
  });

  it("RawInput 字符串同样可解析；失败 metadata 落到 error 态", () => {
    const history: SessionHistoryMessage[] = [
      {
        role: "assistant",
        content: "",
        tool_calls: [
          {
            id: "call-2",
            name: "shell",
            input: '{"command":"npm test"}',
          },
        ],
      },
      {
        role: "tool",
        content: "exit status 1",
        tool_call_id: "call-2",
        metadata: { error: "exit status 1" },
      },
    ];

    const [, receipt] = mapSessionHistoryToMessages("session-1", history, []);
    const segment = toolSegmentOf(receipt.message);

    expect(segment.name).toBe("shell");
    expect(segment.status).toBe("error");
    expect(segment.errorMessage).toBe("exit status 1");
    expect(segment.details?.command).toBe("npm test");
  });

  it("缺失配对时退回 metadata 工具名，仍产出 tool segment", () => {
    const history: SessionHistoryMessage[] = [
      {
        role: "tool",
        content: "done",
        tool_call_id: "call-missing",
        metadata: { tool_name: "list_dir" },
      },
    ];

    const [receipt] = mapSessionHistoryToMessages("session-1", history, []);
    const segment = toolSegmentOf(receipt.message);

    expect(segment.name).toBe("list_dir");
    expect(segment.toolCallId).toBe("call-missing");
    expect(segment.resultSummary).toBe("done");
  });

  it("超长结果按上限截断展示（不改写来源数据）", () => {
    const long = "x".repeat(HISTORY_TOOL_RESULT_LIMIT + 500);
    const [receipt] = mapSessionHistoryToMessages(
      "session-1",
      [{ role: "tool", content: long, tool_call_id: "call-3" }],
      [],
    );
    const segment = toolSegmentOf(receipt.message);

    expect(segment.resultSummary).toHaveLength(HISTORY_TOOL_RESULT_LIMIT + 1);
    expect(segment.resultSummary?.endsWith("…")).toBe(true);
  });

  it("合并既有 live 工具卡时按 toolCallId 去重", () => {
    const existing: ChatMessage[] = [
      {
        id: "msg-1",
        role: "assistant",
        author: "Runtime stream",
        label: "streaming",
        segments: [
          {
            type: "tool",
            toolCallId: "call-1",
            name: "read_file",
            status: "finished",
          },
        ],
      },
    ];
    const history: SessionHistoryMessage[] = [
      {
        role: "assistant",
        content: "",
        metadata: { message_id: "msg-1" },
        tool_calls: [{ id: "call-1", name: "read_file" }],
      },
      { role: "tool", content: "data", tool_call_id: "call-1" },
    ];

    const [merged] = mapSessionHistoryToMessages("session-1", history, existing);
    const toolSegments = merged.message.segments.filter((item) => item.type === "tool");

    expect(toolSegments).toHaveLength(1);
    expect(toolSegments[0]?.type === "tool" ? toolSegments[0].toolCallId : "").toBe(
      "call-1",
    );
  });
});
