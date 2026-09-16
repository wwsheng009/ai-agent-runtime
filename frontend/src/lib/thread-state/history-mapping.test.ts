// 工具回执投影：历史 role="tool" → tool segment（名称/入参/明细/结果），
// 供消息列表渲染为 24px 工具行而不是通用「上下文注入」行。
import { describe, expect, it } from "vitest";

import { type ChatMessage } from "@/data/mock";
import { resolveToolRowPresentation } from "@/lib/tool-row";
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

  // 回归（2026-09-16 回放页面 bug）：view 的批量形态 `files: [{file_path}, …]` 在回放
  // 投影里拿不到顶层单文件键，整行 24px 摘要空白（实测 14 行 view 里 9 行无摘要）。
  it("view 批量 files 入参：回放摘要显示第一个文件路径", () => {
    const history: SessionHistoryMessage[] = [
      {
        role: "assistant",
        content: "",
        tool_calls: [
          {
            id: "call-view-batch",
            name: "view",
            arguments: {
              files: [
                { file_path: "backend/cmd/aicli/ui/screen.go", limit: 120 },
                { file_path: "backend/cmd/aicli/ui/app_screen_layout.go" },
              ],
            },
          },
        ],
      },
      { role: "tool", content: "package ui", tool_call_id: "call-view-batch" },
    ];

    const [, receipt] = mapSessionHistoryToMessages("session-1", history, []);
    const segment = toolSegmentOf(receipt.message);

    expect(segment.name).toBe("view");
    expect(segment.details?.filePath).toBe("backend/cmd/aicli/ui/screen.go");
    expect(resolveToolRowPresentation(segment).summary.parts).toEqual([
      { type: "path", path: "backend/cmd/aicli/ui/screen.go" },
    ]);
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

// §12.1.4：空 content 不得降级成 "[empty message]" 占位文本——工具回合 / 仅推理 /
// 仅附件的空正文是正常协议形态，占位文案会顶到过程区（含匹配到 live 消息的合并路径）。
describe("历史空消息不占位", () => {
  it("工具回合的空 content 不生成文本段", () => {
    const history: SessionHistoryMessage[] = [
      {
        role: "assistant",
        content: "",
        tool_calls: [{ id: "call-1", name: "read_file" }],
      },
      { role: "tool", content: "42 行", tool_call_id: "call-1" },
    ];

    const [assistant, receipt] = mapSessionHistoryToMessages("session-1", history, []);

    // 空正文的 assistant 步不产出行段（工具段挂在回执条目上）。
    expect(assistant.message.segments).toEqual([]);
    expect(toolSegmentOf(receipt.message).name).toBe("read_file");
    expect(
      JSON.stringify([assistant, receipt].map((entry) => entry.message.segments)),
    ).not.toContain("[empty message]");
  });

  it("仅推理消息只保留推理段", () => {
    const history: SessionHistoryMessage[] = [
      {
        role: "assistant",
        content: "",
        metadata: {
          reasoning_details: { visibility: "visible", content: "先盘点入口文件" },
        },
      },
    ];

    const [assistant] = mapSessionHistoryToMessages("session-1", history, []);

    expect(assistant.message.segments.map((segment) => segment.type)).toEqual([
      "reasoning",
    ]);
  });

  // 回归：历史条目同时有正文与推理时，推理段必须排在正文段之前——
  // 页面按段落顺序渲染，旧实现（正文在前）会把「推理过程」行显示在回答下方。
  it("正文与推理并存时推理段排在正文段之前", () => {
    const history: SessionHistoryMessage[] = [
      {
        role: "assistant",
        content: "结论：入口文件共 42 行。",
        metadata: {
          reasoning_details: { visibility: "visible", content: "先盘点入口文件" },
        },
      },
    ];

    const [assistant] = mapSessionHistoryToMessages("session-1", history, []);

    expect(assistant.message.segments.map((segment) => segment.type)).toEqual([
      "reasoning",
      "text",
    ]);
  });

  it("匹配 live 消息时以历史正文为准，空正文不并进占位文本", () => {
    const existing: ChatMessage[] = [
      {
        id: "msg-1",
        role: "assistant",
        author: "Runtime stream",
        label: "streaming",
        segments: [{ type: "text", content: "结论：入口文件共 42 行。" }],
      },
    ];
    const history: SessionHistoryMessage[] = [
      {
        role: "assistant",
        content: "",
        metadata: { message_id: "msg-1" },
        tool_calls: [{ id: "call-1", name: "read_file" }],
      },
      { role: "tool", content: "42 行", tool_call_id: "call-1" },
    ];

    const [merged] = mapSessionHistoryToMessages("session-1", history, existing);
    const textSegments = merged.message.segments.filter(
      (segment) => segment.type === "text",
    );

    // 历史是权威来源：持久化正文为空时消息不含文本段，也不补占位文案。
    expect(textSegments).toEqual([]);
    expect(JSON.stringify(merged.message.segments)).not.toContain("[empty message]");

    const durableHistory: SessionHistoryMessage[] = [
      {
        role: "assistant",
        content: "结论：入口文件共 42 行。",
        metadata: { message_id: "msg-1" },
        tool_calls: [{ id: "call-1", name: "read_file" }],
      },
      { role: "tool", content: "42 行", tool_call_id: "call-1" },
    ];
    const [mergedDurable] = mapSessionHistoryToMessages(
      "session-1",
      durableHistory,
      existing,
    );

    expect(
      mergedDurable.message.segments.filter((segment) => segment.type === "text"),
    ).toEqual([{ type: "text", content: "结论：入口文件共 42 行。" }]);
  });

  it("有正文时文本段保持原样（回归）", () => {
    const history: SessionHistoryMessage[] = [
      { role: "assistant", content: "  结论：42 行。  " },
    ];

    const [assistant] = mapSessionHistoryToMessages("session-1", history, []);

    expect(assistant.message.segments).toEqual([
      { type: "text", content: "结论：42 行。" },
    ]);
  });
});
