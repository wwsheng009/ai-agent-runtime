// P0-2 拆分：chat.sse 桥接帧测试的共用夹具（原 chat-sse-bridge.test.ts L21-L94）。
// 桥接帧渲染与实时工具生命周期两个测试文件共用同一套线程构造与断言助手。

import type { MessageSegment, Thread } from "@/data/mock";
import {
  applyRuntimeEventToThread,
  createStreamingAssistantMessage,
} from "@/lib/workspace-thread-state";
import type { SessionRuntimeEvent } from "@/types/runtime";

export const TOOL_CALL = {
  id: "observation_step_1_tool_0",
  name: "shell",
  arguments: { command: "go test ./..." },
};

export function toolFrame(
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

export function createLiveThread(segments: MessageSegment[]): Thread {
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

export function apply(thread: Thread, event: SessionRuntimeEvent) {
  return applyRuntimeEventToThread(thread, "session-1", [event], event);
}

export function toolSegments(thread: Thread) {
  const message = thread.messages[thread.messages.length - 1];
  return message.segments.filter((segment) => segment.type === "tool");
}

export function reasoningSegments(thread: Thread) {
  const message = thread.messages[thread.messages.length - 1];
  return message.segments.filter((segment) => segment.type === "reasoning");
}
