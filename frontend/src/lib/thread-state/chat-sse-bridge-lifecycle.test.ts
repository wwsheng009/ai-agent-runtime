// P0-2 拆分：实时工具生命周期帧回归（原 chat-sse-bridge.test.ts L367-L590）。

import { describe, expect, it } from "vitest";

import { applyRuntimeDeltaToThread } from "@/lib/workspace-thread-state";
import type { SessionRuntimeEvent } from "@/types/runtime";

import {
  apply,
  createLiveThread,
  reasoningSegments,
  toolFrame,
  toolSegments,
} from "./chat-sse-bridge.test-fixtures";
import { getRuntimeBridgeKind } from "./deltas";

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
