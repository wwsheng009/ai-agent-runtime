// 方案C 回归：runtime/stream 上的 `chat.sse.*` 桥接帧。
//
// 症状：页面停在「推理过程」不动——模型随后执行了一串工具，但消息流里既没有
// 工具行、推理行也一直挂着运行态。根因是 /api/agent/chat 每帧都被持久化成
// `chat.sse.<name>` 并在会话 runtime/stream 上重放，而前端只认
// `assistant_delta` / `assistant.reasoning` / `assistant.image_progress`，
// `chat.sse.tool_*` 整类被丢弃（既不落消息段，也不收尾推理段）。
import { describe, expect, it } from "vitest";

import type { Thread } from "@/data/mock";
import {
  applyRuntimeDeltaToThread,
  applyRuntimeEventToThread,
  createStreamingAssistantMessage,
} from "@/lib/workspace-thread-state";
import type { MessageSegment } from "@/data/mock";
import type { SessionRuntimeEvent } from "@/types/runtime";

import { getRuntimeBridgeKind } from "./deltas";

const TOOL_CALL = {
  id: "observation_step_1_tool_0",
  name: "shell",
  arguments: { command: "go test ./..." },
};

function toolFrame(
  type: string,
  options: {
    withDelta?: boolean;
    content?: string;
    metadata?: Record<string, unknown>;
  } = {},
): SessionRuntimeEvent {
  return {
    type,
    timestamp: "2026-09-15T00:00:01Z",
    payload: {
      type,
      index: 1,
      content: options.content ?? "",
      ...(options.withDelta ? { delta: TOOL_CALL } : {}),
      tool: {
        ...TOOL_CALL,
        args: TOOL_CALL.arguments,
        status: type,
        content: options.content ?? "",
      },
      tool_call: TOOL_CALL,
      metadata: options.metadata ?? {},
      turn_id: "turn-1",
    },
  };
}

function createLiveThread(segments: MessageSegment[]): Thread {
  return {
    id: "thread-1",
    title: "Thread",
    summary: "",
    updatedAt: "2026-09-15T00:00:00Z",
    status: "active",
    tags: [],
    prompts: [],
    artifacts: [],
    messages: [
      {
        id: "user-1",
        role: "user",
        author: "You",
        label: "you",
        segments: [{ type: "text", content: "跑测试" }],
      },
      {
        ...createStreamingAssistantMessage("assistant-live", [], "turn-1"),
        segments,
      },
    ],
  };
}

function apply(thread: Thread, event: SessionRuntimeEvent) {
  return applyRuntimeEventToThread(thread, "session-1", [event], event);
}

function toolSegments(thread: Thread) {
  const message = thread.messages[thread.messages.length - 1];
  return message.segments.filter((segment) => segment.type === "tool");
}

function reasoningSegments(thread: Thread) {
  const message = thread.messages[thread.messages.length - 1];
  return message.segments.filter((segment) => segment.type === "reasoning");
}

describe("chat.sse 桥接帧分类", () => {
  it("工具生命周期按帧名映射到工具行状态", () => {
    expect(getRuntimeBridgeKind("chat.sse.tool_start")).toEqual({
      kind: "tool",
      status: "started",
    });
    expect(getRuntimeBridgeKind("chat.sse.tool_call")).toEqual({
      kind: "tool",
      status: "running",
    });
    expect(getRuntimeBridgeKind("chat.sse.tool_end")).toEqual({
      kind: "tool",
      status: "finished",
    });
    // observation 只带工具名，不建行，仅作阶段推进信号。
    expect(getRuntimeBridgeKind("chat.sse.observation")).toEqual({
      kind: "phase",
    });
    expect(getRuntimeBridgeKind("chat.sse.chunk")).toEqual({ kind: "phase" });
  });

  it("总线增量与未知事件都不算桥接帧", () => {
    expect(getRuntimeBridgeKind("assistant_delta")).toBeNull();
    expect(getRuntimeBridgeKind("assistant.reasoning")).toBeNull();
    // 推理帧是增量帧的孪生副本：它若参与收尾，推理行会被两路写成「跑/停」交替
    // （真实会话 156 对帧 → 312 次翻转）。收尾只由阶段出口负责。
    expect(getRuntimeBridgeKind("chat.sse.reasoning")).toBeNull();
    expect(getRuntimeBridgeKind("chat.sse.done")).toBeNull();
    expect(getRuntimeBridgeKind("runtime.step")).toBeNull();
  });
});

describe("chat.sse 工具帧渲染工具行", () => {
  it("tool_call → tool_end 收敛成同一行，带上入参与结果", () => {
    let thread = createLiveThread([
      { type: "reasoning", content: "先看失败原因", running: true },
    ]);

    thread = apply(thread, toolFrame("chat.sse.tool_call", { withDelta: true }));
    expect(toolSegments(thread)).toHaveLength(1);
    expect(toolSegments(thread)[0]).toMatchObject({
      type: "tool",
      name: "shell",
      toolCallId: TOOL_CALL.id,
      status: "running",
    });

    thread = apply(
      thread,
      toolFrame("chat.sse.tool_end", {
        content: "===== command 1/1 [ok] =====\nExit code: 0",
      }),
    );

    const tools = toolSegments(thread);
    expect(tools).toHaveLength(1);
    expect(tools[0]).toMatchObject({
      toolCallId: TOOL_CALL.id,
      name: "shell",
      status: "finished",
    });
    expect(tools[0].argsSummary).toContain("go test ./...");
    expect(tools[0].resultSummary).toContain("Exit code: 0");
    expect(thread.lastRuntimeEventType).toBe("tool_end:shell");
  });

  it("tool_end 带 metadata.error 时落成错误行", () => {
    const thread = apply(
      createLiveThread([]),
      toolFrame("chat.sse.tool_end", {
        content: "[TOOL_BROKER_FAILURE] denied",
        metadata: { error: "[TOOL_BROKER_FAILURE] denied" },
      }),
    );

    expect(toolSegments(thread)[0]).toMatchObject({
      status: "error",
      errorMessage: "[TOOL_BROKER_FAILURE] denied",
    });
  });

  it("缺少 id 与工具名的残缺帧不落假工具行", () => {
    const event: SessionRuntimeEvent = {
      type: "chat.sse.tool_start",
      timestamp: "2026-09-15T00:00:02Z",
      payload: { content: "", tool: "shell", turn_id: "turn-1" },
    };

    const thread = apply(createLiveThread([]), event);
    expect(toolSegments(thread)).toHaveLength(0);
  });
});

describe("chat.sse 阶段帧推进渲染", () => {
  it("首个正文分片终结仍在跑的推理段，且不重复写入正文", () => {
    const thread = apply(
      createLiveThread([{ type: "reasoning", content: "先看失败原因", running: true }]),
      {
        type: "chat.sse.chunk",
        timestamp: "2026-09-15T00:00:03Z",
        payload: {
          type: "text",
          content: "工作区已清空",
          stream_id: "stream-1",
          sequence: 1,
          turn_id: "turn-1",
        },
      },
    );

    const message = thread.messages[thread.messages.length - 1];
    expect(reasoningSegments(thread)).toEqual([
      { type: "reasoning", content: "先看失败原因", running: false },
    ]);
    // 正文由 assistant_delta 承载，桥接帧只做阶段推进。
    expect(
      message.segments.filter((segment) => segment.type === "text"),
    ).toHaveLength(0);
  });

  it("工具帧同样终结推理段", () => {
    const thread = apply(
      createLiveThread([{ type: "reasoning", content: "要跑测试", running: true }]),
      toolFrame("chat.sse.tool_start"),
    );

    expect(reasoningSegments(thread)[0].running).toBe(false);
  });

  it("chat.sse.reasoning 孪生帧不与推理增量抢状态", () => {
    let thread = createLiveThread([
      { type: "reasoning", content: "先看失败原因", running: true },
    ]);

    thread = applyRuntimeDeltaToThread(
      thread,
      {
        type: "assistant.reasoning",
        timestamp: "2026-09-15T00:00:06Z",
        payload: {
          type: "reasoning",
          content: "再确认工具",
          stream_id: "stream-1",
          sequence: 12,
          turn_id: "turn-1",
        },
      },
      "turn-1",
    );
    expect(reasoningSegments(thread)[0].running).toBe(true);
    const messagesAfterDelta = thread.messages;

    // 同一段推理的孪生帧紧随其后到达（真实日志间隔恒为 1）：既不改文本，
    // 也不把推理行判成已结束——messages 身份不变，React 侧可整块跳过。
    const next = apply(thread, {
      type: "chat.sse.reasoning",
      timestamp: "2026-09-15T00:00:06.100Z",
      payload: {
        type: "reasoning",
        content: " ",
        reasoning: { content: " ", delta: " ", length: 1 },
        stream_id: "stream-1",
        sequence: 388,
        turn_id: "turn-1",
      },
    });

    expect(next.messages).toBe(messagesAfterDelta);
    expect(reasoningSegments(next)[0]).toMatchObject({
      content: "先看失败原因再确认工具",
      running: true,
    });
  });

  it("没有在跑的推理段时不重建消息段（逐帧不换身份）", () => {
    const thread = createLiveThread([
      { type: "reasoning", content: "已完成", running: false },
    ]);

    const next = apply(thread, {
      type: "chat.sse.chunk",
      timestamp: "2026-09-15T00:00:04Z",
      payload: { content: "增量", turn_id: "turn-1" },
    });

    // thread 自身必然变化（事件 artifact / lastRuntimeEventType / updatedAt 由
    // applyRuntimeEventToThread 统一推进）；这里守的是「桥接帧不改消息」，
    // 引用稳定才能让 React 侧跳过无意义重渲染。
    expect(next.messages).toBe(thread.messages);
  });
});

describe("chat.sse 桥接帧的回放安全", () => {
  it("已定稿的助手消息不再被桥接帧改写", () => {
    const thread = createLiveThread([]);
    const finalized: Thread = {
      ...thread,
      messages: thread.messages.map((message) =>
        message.role === "assistant"
          ? {
              ...message,
              streaming: false,
              segments: [
                { type: "reasoning" as const, content: "历史推理", running: true },
              ],
            }
          : message,
      ),
    };

    const next = apply(finalized, toolFrame("chat.sse.tool_end", { content: "out" }));

    expect(next.messages[next.messages.length - 1].segments).toEqual([
      { type: "reasoning", content: "历史推理", running: true },
    ]);
  });

  it("turn 身份明确不一致时拒绝写入", () => {
    const thread = createLiveThread([
      { type: "reasoning", content: "另一个回合", running: true },
    ]);

    const next = apply(thread, {
      type: "chat.sse.tool_start",
      timestamp: "2026-09-15T00:00:05Z",
      payload: {
        content: "",
        tool_call: TOOL_CALL,
        tool: { ...TOOL_CALL, args: TOOL_CALL.arguments },
        turn_id: "turn-other",
      },
    });

    expect(toolSegments(next)).toHaveLength(0);
    expect(reasoningSegments(next)[0].running).toBe(true);
  });
});
