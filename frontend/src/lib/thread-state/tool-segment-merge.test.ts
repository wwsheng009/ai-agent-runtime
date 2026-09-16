import { describe, expect, it } from "vitest";

import { type MessageSegment } from "@/data/mock";
import { type AgentChatStreamChunkPayload } from "@/types/runtime";

import { type ToolMessageSegment } from "./messages";
import { buildToolSegmentFromPayload, upsertToolSegment } from "./tools";

function onlyTool(segments: MessageSegment[]): ToolMessageSegment[] {
  return segments.filter((segment): segment is ToolMessageSegment => segment.type === "tool");
}

// 实时行只有 `arg_preview` 键值文本（桥接层把它塞进 `tool.args` / `tool_call.arguments`），
// 回合末尾巴才补完整入参；同一行的明细必须**单调累积**——后到帧没赋值的键不能把已显示
// 出来的参数覆盖成 undefined，否则工具收尾的瞬间摘要行只剩工具名。
function liveStartedPayload(): AgentChatStreamChunkPayload {
  return {
    tool: {
      id: "call_live_1",
      name: "shell",
      status: "tool_started",
      args: "command=ls -la",
      command_text: "ls -la",
    },
    tool_call: { id: "call_live_1", name: "shell", arguments: "command=ls -la" },
  };
}

describe("工具行明细单调累积", () => {
  it("实时帧的 arg_preview 解析成明细（摘要行不只剩工具名）", () => {
    const started = buildToolSegmentFromPayload(liveStartedPayload(), "started");

    expect(started.argsSummary).toContain("ls -la");
    expect(started.details).toEqual({ command: "ls -la" });
  });

  it("收尾帧缺入参时保留先前解析的明细与摘要原文", () => {
    const [row] = onlyTool(upsertToolSegment(
      [buildToolSegmentFromPayload(liveStartedPayload(), "started")],
      buildToolSegmentFromPayload(
        {
          content: "Exit code: 0",
          tool: { id: "call_live_1", name: "shell", status: "tool_finished" },
        },
        "finished",
      ),
    ));

    expect(row).toMatchObject({ type: "tool", status: "finished" });
    expect(row.argsSummary).toContain("ls -la");
    expect(row.details).toEqual({ command: "ls -la" });
    expect(row.resultSummary).toContain("Exit code: 0");
  });

  it("尾巴带来结构化入参时按字段合并，保留行级明细", () => {
    const [row] = onlyTool(upsertToolSegment(
      [buildToolSegmentFromPayload(liveStartedPayload(), "started")],
      buildToolSegmentFromPayload(
        {
          content: "Exit code: 0",
          tool_call: { id: "call_live_1", name: "shell", arguments: { command: "ls -la" } },
          tool: { id: "call_live_1", name: "shell", status: "tool_end", content: "Exit code: 0" },
        },
        "finished",
      ),
    ));

    expect(row.details).toEqual({ command: "ls -la" });
    expect(row.argsSummary).toContain("ls -la");
  });

  // 回归（2026-09-16）：shell 的真实入参是批量 `commands` 列表；实时帧的
  // `command_text` 与尾巴的结构化 `commands` 必须归一成同一份命令文本。
  it("批量 commands 帧同样得到命令明细（摘要行不只剩工具名）", () => {
    const started = buildToolSegmentFromPayload(
      {
        tool: {
          id: "call_batch_1",
          name: "shell",
          status: "tool_started",
          args: "command=go test ./... ; git status --short",
          command_text: "go test ./... ; git status --short",
        },
        tool_call: {
          id: "call_batch_1",
          name: "shell",
          arguments: {
            commands: [{ command: "go test ./..." }, { command: "git status --short" }],
          },
        },
      },
      "started",
    );

    expect(started.details).toEqual({ command: "go test ./... ; git status --short" });
    expect(started.argsSummary).toContain("go test ./...");
  });
});
