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

  it("已定稿消息被历史投影换 id 后，迟到桥接帧不再补建 streaming 占位", () => {
    // 回归（2026-09-18）：历史投影会重写消息 id（history-mapping），补建守卫
    // 只查 `turn-<turnId>-assistant` 时会漏掉「同回合已定稿」的消息，迟到工具帧
    // 于是新建一条永远无人收尾的 streaming 占位（实测 UI 永久「响应中」）。
    const thread = createLiveThread([]);
    const finalized: Thread = {
      ...thread,
      messages: thread.messages.map((message) =>
        message.role === "assistant"
          ? {
              ...message,
              id: "msg-assistant-1",
              label: "runtime",
              streaming: false,
              runtimeTurnId: "turn-1",
            }
          : message,
      ),
    };
    const frame = toolFrame("chat.sse.tool_end", { content: "out" });

    const next = applyRuntimeEventToThread(
      finalized,
      "session-1",
      [frame],
      frame,
      "turn-1",
    );

    expect(next.messages).toHaveLength(finalized.messages.length);
    expect(next.messages.some((message) => message.streaming === true)).toBe(false);
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

// 实时工具行回归（2026-09-15）：agent loop 在执行每个工具的当下就把
// tool.requested / tool.completed 发到 runtime 总线（internal/agent/loop.go），
// 事件存储把它们落成 tool_started / tool_finished。这条路过去被整类跳过，
// 工具行只能等回合末的证据尾巴补齐（实测 21 帧挤在末尾 18ms 内）——
// 「推理结束 → 工具执行」这段窗口里 UI 完全没有反馈，只能看着推理行挂着。
const LIVE_PROVIDER_ID = "call_00_live_a";

const LIVE_TOOL_PAYLOAD = {
  tool_call_id: LIVE_PROVIDER_ID,
  logical_tool: "shell",
  step: 1,
  trace_id: "trace-live-1",
  // 真实后端形态：`summarizeToolCallArgs` 的键值文本，不是 JSON。
  arg_preview: "command=go test ./...",
  command_text: "go test ./...",
};

function lifecycleFrame(
  type: string,
  overrides: Record<string, unknown> = {},
): SessionRuntimeEvent {
  // 真实生命周期载荷没有 turn_id（loop.go 只在 turnID 非空时注入）：这里刻意
  // 省掉，覆盖「增量/桥接帧的 turn 身份未知」这条最常见路径。
  return {
    type,
    timestamp: "2026-09-15T00:00:07Z",
    payload: { ...LIVE_TOOL_PAYLOAD, ...overrides },
  };
}

/** 回合末证据尾巴的 tool_end：行 id 已被后端改写为实时帧使用的 provider call id。 */
function tailToolEndFrame(content: string): SessionRuntimeEvent {
  const toolCall = {
    id: LIVE_PROVIDER_ID,
    name: "shell",
    arguments: { command: "go test ./..." },
  };
  return {
    type: "chat.sse.tool_end",
    timestamp: "2026-09-15T00:00:20Z",
    payload: {
      index: 1,
      type: "tool_end",
      content,
      metadata: { live: true, step: 1 },
      tool_call: toolCall,
      tool: { ...toolCall, args: toolCall.arguments, status: "tool_end", content },
      turn_id: "turn-1",
    },
  };
}

// 回归（2026-09-16 页面 bug）：一个回合里推理与工具是交替出现的
// （推理 → 工具 → 工具 → 推理 → 工具），页面上却只剩一段推理——工具之后到达的推理
// 增量被并回了上一段。段顺序就是到达顺序：推理段一旦被工具行顶下来就写完了，之后
// 到达的推理属于**新的一块**，live 层（lib/live-stream-text.ts）同样按块寻址。
describe("推理与工具交替：工具之后的推理新起一块", () => {
  function reasoningFrame(delta: string): SessionRuntimeEvent {
    return {
      type: "assistant.reasoning",
      timestamp: "2026-09-16T00:00:01Z",
      payload: {
        type: "reasoning",
        content: "",
        reasoning: { content: delta, summary: delta },
        turn_id: "turn-1",
      },
    };
  }

  it("工具帧之后的推理增量不再并回上一段，live 层收到 blockStart", () => {
    const live: Array<{ blockStart?: boolean; text: string }> = [];
    const sink = (delta: { blockStart?: boolean; text: string }) => {
      live.push(delta);
    };

    let thread = createLiveThread([]);
    thread = applyRuntimeDeltaToThread(thread, reasoningFrame("先看入口。"), "turn-1", sink);
    // 同一块内的连续增量：逐段拼接（不重置，也不新起一行）。
    thread = applyRuntimeDeltaToThread(
      thread,
      reasoningFrame("再确认调用链。"),
      "turn-1",
      sink,
    );
    thread = apply(thread, toolFrame("chat.sse.tool_start"));
    thread = apply(thread, lifecycleFrame("tool_started"));
    thread = applyRuntimeDeltaToThread(
      thread,
      reasoningFrame("测试失败了，看下报错。"),
      "turn-1",
      sink,
    );

    expect(
      thread.messages[thread.messages.length - 1].segments.map(
        (segment) => segment.type,
      ),
    ).toEqual(["reasoning", "tool", "tool", "reasoning"]);
    expect(reasoningSegments(thread).map((segment) => segment.content)).toEqual([
      "先看入口。再确认调用链。",
      "测试失败了，看下报错。",
    ]);
    // 旧块被工具行收尾（不再转圈），新块仍在运行。
    const rows = reasoningSegments(thread);
    expect(rows[0].running).toBe(false);
    expect(rows[1].running).toBe(true);
    // live 层按块寻址：只有新块的首个增量带 blockStart（写方据此覆盖而不是追加）。
    expect(live).toMatchObject([
      { blockStart: true, text: "先看入口。" },
      { blockStart: false, text: "再确认调用链。" },
      { blockStart: true, text: "测试失败了，看下报错。" },
    ]);
  });
});

describe("runtime 工具生命周期帧实时建行", () => {
  it("生命周期事件名归类为工具帧", () => {
    expect(getRuntimeBridgeKind("tool_started")).toEqual({
      kind: "tool",
      status: "started",
    });
    expect(getRuntimeBridgeKind("tool.requested")).toEqual({
      kind: "tool",
      status: "started",
    });
    expect(getRuntimeBridgeKind("tool_finished")).toEqual({
      kind: "tool",
      status: "finished",
    });
    expect(getRuntimeBridgeKind("tool.completed")).toEqual({
      kind: "tool",
      status: "finished",
    });
  });

  it("tool_started 在执行当下建行，tool_finished 收敛到同一行", () => {
    let thread = createLiveThread([
      { type: "reasoning", content: "先跑测试", running: true },
    ]);

    thread = apply(thread, lifecycleFrame("tool_started"));

    const started = toolSegments(thread);
    expect(started).toHaveLength(1);
    expect(started[0]).toMatchObject({
      type: "tool",
      name: "shell",
      toolCallId: LIVE_PROVIDER_ID,
      status: "started",
    });
    expect(started[0].argsSummary).toContain("go test ./...");
    // 摘要行渲染 details（argsSummary 只是兜底）：预览必须解析出 command。
    expect(started[0].details).toEqual({ command: "go test ./..." });
    // 工具帧同样是「阶段出口」：推理行随即收尾。
    expect(reasoningSegments(thread)[0].running).toBe(false);

    thread = apply(thread, lifecycleFrame("tool_finished", { summary: "Exit code: 0" }));

    const finished = toolSegments(thread);
    expect(finished).toHaveLength(1);
    expect(finished[0]).toMatchObject({
      toolCallId: LIVE_PROVIDER_ID,
      status: "finished",
    });
    expect(finished[0].resultSummary).toContain("Exit code: 0");
    // 完成帧同样带 arg_preview：参数不能被收尾擦掉。
    expect(finished[0].details).toEqual({ command: "go test ./..." });
  });

  it("回合末证据尾巴按同一 provider id 就地补全，不再新增行", () => {
    let thread = createLiveThread([]);
    thread = apply(thread, lifecycleFrame("tool_started"));
    thread = apply(thread, lifecycleFrame("tool_finished", { summary: "预览摘要" }));
    thread = apply(thread, tailToolEndFrame("===== command 1/1 [ok] =====\nExit code: 0"));

    const tools = toolSegments(thread);
    expect(tools).toHaveLength(1);
    expect(tools[0]).toMatchObject({
      toolCallId: LIVE_PROVIDER_ID,
      name: "shell",
      status: "finished",
    });
    // 尾巴的权威输出覆盖实时预览，且入参仍在同一行上。
    expect(tools[0].resultSummary).toContain("Exit code: 0");
    expect(tools[0].argsSummary).toContain("go test ./...");
    expect(tools[0].details).toEqual({ command: "go test ./..." });
  });

  it("重复的请求/完成帧幂等，不产生第二行", () => {
    let thread = createLiveThread([]);
    thread = apply(thread, lifecycleFrame("tool_started"));
    thread = apply(thread, lifecycleFrame("tool_started"));
    thread = apply(thread, lifecycleFrame("tool_finished", { summary: "ok" }));
    thread = apply(thread, lifecycleFrame("tool_finished", { summary: "ok" }));

    expect(toolSegments(thread)).toHaveLength(1);
  });

  it("tool.completed 带 error 时落成错误行", () => {
    let thread = createLiveThread([]);
    thread = apply(thread, lifecycleFrame("tool.requested"));
    thread = apply(
      thread,
      lifecycleFrame("tool.completed", { error: "[TOOL_BROKER_FAILURE] denied" }),
    );

    expect(toolSegments(thread)[0]).toMatchObject({
      toolCallId: LIVE_PROVIDER_ID,
      status: "error",
      errorMessage: "[TOOL_BROKER_FAILURE] denied",
    });
  });

  it("既无 id 也无工具名的生命周期帧不落假工具行", () => {
    const thread = apply(createLiveThread([]), {
      type: "tool_started",
      timestamp: "2026-09-15T00:00:08Z",
      payload: { step: 2 },
    });

    expect(toolSegments(thread)).toHaveLength(0);
  });
});
