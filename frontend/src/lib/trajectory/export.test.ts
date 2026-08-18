import { describe, expect, it } from "vitest";

import type { SessionRuntimeEvent } from "@/types/runtime";

import {
  buildTrajectoryExportFilename,
  chatSseEventToExportEntry,
  eventsToTrajectoryJsonl,
  redactExportPayload,
} from "./export";

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

describe("chatSseEventToExportEntry", () => {
  it("转换 kind 并剥离游标 seq", () => {
    const entry = chatSseEventToExportEntry(
      event("chat.sse.tool_start", 4, { name: "web_search" }),
    );
    expect(entry).toEqual({
      seq: 4,
      ts: "2026-08-16T00:00:10Z",
      kind: "tool_start",
      payload: { name: "web_search" },
    });
  });

  it("非 chat.sse.* 事件返回 null", () => {
    expect(chatSseEventToExportEntry(event("runtime.step", 1))).toBeNull();
  });
});

describe("eventsToTrajectoryJsonl", () => {
  it("按 seq 升序输出 JSONL 行（逐条与拉取结果一致）", () => {
    const jsonl = eventsToTrajectoryJsonl([
      event("chat.sse.done", 3, { content: "done" }),
      event("chat.sse.meta", 1, { kind: "chat" }),
      event("runtime.step", 99), // 过滤
      event("chat.sse.chunk", 2, { text: "hi" }),
    ]);
    const lines = jsonl.split("\n").map((line) => JSON.parse(line));
    expect(lines.map((line) => line.kind)).toEqual(["meta", "chunk", "done"]);
    expect(lines[1]).toMatchObject({ seq: 2, kind: "chunk" });
    expect(lines[1].payload).toEqual({ text: "hi" });
  });

  it("空输入返回空字符串", () => {
    expect(eventsToTrajectoryJsonl([])).toBe("");
  });

  it("redact 选项掩码工具参数与输出，保留身份字段与正文", () => {
    const jsonl = eventsToTrajectoryJsonl(
      [
        event("chat.sse.tool_start", 1, {
          tool: {
            id: "tool-1",
            name: "web_search",
            args: { query: "capital of France" },
          },
          tool_call: {
            id: "tool-1",
            function: { name: "web_search", arguments: '{"query":"paris"}' },
          },
        }),
        event("chat.sse.tool_end", 2, {
          tool: {
            id: "tool-1",
            name: "web_search",
            content: "Paris is the capital of France.",
          },
        }),
        event("chat.sse.chunk", 3, { type: "text", content: "The answer: Paris." }),
      ],
      { redact: true },
    );
    const lines = jsonl.split("\n").map((line) => JSON.parse(line));

    const toolStart = lines.find((line) => line.kind === "tool_start");
    expect(toolStart.payload.tool.args).toEqual({ query: "<redacted>" });
    expect(toolStart.payload.tool.id).toBe("tool-1");
    expect(toolStart.payload.tool.name).toBe("web_search");
    expect(toolStart.payload.tool_call.function.arguments).toBe("<redacted>");
    expect(toolStart.payload.tool_call.function.name).toBe("web_search");

    const toolEnd = lines.find((line) => line.kind === "tool_end");
    expect(toolEnd.payload.tool.content).toBe("<redacted>");

    // 正文 chunk 不脱敏。
    const chunk = lines.find((line) => line.kind === "chunk");
    expect(chunk.payload.content).toBe("The answer: Paris.");
  });
});

describe("buildTrajectoryExportFilename", () => {
  it("包含会话与时间戳", () => {
    const name = buildTrajectoryExportFilename(
      "session-1",
      new Date("2026-08-16T01:02:03Z"),
    );
    expect(name).toBe("trajectory-session-1-2026-08-16T01-02-03.jsonl");
  });

  it("脱敏导出带 -redacted 后缀", () => {
    const name = buildTrajectoryExportFilename(
      "session-1",
      new Date("2026-08-16T01:02:03Z"),
      true,
    );
    expect(name).toBe("trajectory-session-1-redacted-2026-08-16T01-02-03.jsonl");
  });
});

describe("redactExportPayload", () => {
  it("done 事件的 result.tool_events 走工具容器脱敏，usage/orchestration 保留", () => {
    const payload = redactExportPayload({
      status: "completed",
      result: {
        kind: "llm",
        output: "Hello",
        usage: { total_tokens: 42 },
        orchestration: { source: "llm_direct", success: true },
        tool_events: [
          {
            type: "tool_start",
            tool: { id: "t1", name: "lookup", args: { q: "secret" } },
            content: "raw args stream",
          },
        ],
        tool_calls: [{ id: "c1", function: { name: "lookup", arguments: "{}" } }],
      },
    });
    const result = payload.result as Record<string, unknown>;
    const toolEvents = result.tool_events as Array<Record<string, unknown>>;
    expect(toolEvents[0].tool).toEqual({
      id: "t1",
      name: "lookup",
      args: { q: "<redacted>" },
    });
    expect(toolEvents[0].content).toBe("<redacted>");
    expect(result.tool_calls).toEqual([
      { id: "c1", function: { name: "lookup", arguments: "<redacted>" } },
    ]);
    expect(result.output).toBe("Hello");
    expect(result.usage).toEqual({ total_tokens: 42 });
    expect(result.orchestration).toEqual({ source: "llm_direct", success: true });
  });
});
