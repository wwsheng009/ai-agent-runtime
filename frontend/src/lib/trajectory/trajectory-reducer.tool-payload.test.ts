// 回归：EventStore 里 `chat.sse.tool_*` 帧的原始载荷形态（对象入参 + content 结果
// + metadata.duration_ms）必须能投影出参数/结果/耗时——此前 readFirstString 只认
// 字符串、结果不读 content，轨迹工具行只剩一个名称。
import { describe, expect, it } from "vitest";

import { applyEvents, makeTrajectoryEvent } from "./trajectory-reducer";
import { createEmptyTrajectory, type TrajectoryItem } from "./types";

/** 取自 session_20260915154631_uhcab3Nx 的真实 tool_call 帧（seq 1074，节选）。 */
const TOOL_CALL_FRAME: Record<string, unknown> = {
  type: "tool_call",
  index: 1,
  content: "",
  delta: {
    arguments: { command: "git status", workdir: "E:\\temp" },
    id: "observation_step_1_tool_0",
    name: "shell",
  },
  tool: {
    id: "observation_step_1_tool_0",
    name: "shell",
    status: "running",
    content: "",
    args: { command: "git status", workdir: "E:\\temp" },
  },
  tool_call: {
    id: "observation_step_1_tool_0",
    name: "shell",
    arguments: { command: "git status", workdir: "E:\\temp" },
  },
  metadata: { step: 1, duration_ms: 12, success: true },
};

/** 同一会话的真实 tool_end 帧（seq 1075，节选）。 */
const TOOL_END_FRAME: Record<string, unknown> = {
  type: "tool_call",
  index: 1,
  content: "Exit code: 1\nShell: pwsh\n输出: fatal: not a git repository",
  delta: { id: "observation_step_1_tool_0", name: "shell" },
  tool: {
    id: "observation_step_1_tool_0",
    name: "shell",
    status: "finished",
    content: "Exit code: 1\nShell: pwsh\n输出: fatal: not a git repository",
    args: { command: "git status", workdir: "E:\\temp" },
  },
  tool_call: {
    id: "observation_step_1_tool_0",
    name: "shell",
    arguments: { command: "git status", workdir: "E:\\temp" },
  },
  // 真实形态：metadata.duration_ms 恒为 0（占位），真实耗时在
  // metadata.metrics.tool_metadata.duration_ms。
  metadata: {
    step: 1,
    duration_ms: 0,
    metrics: { tool_metadata: { duration_ms: 506, exit_code: 1 } },
  },
};

function toolItem(items: TrajectoryItem[]): TrajectoryItem {
  const item = items.find((entry) => entry.head.kind === "tool");
  if (!item) {
    throw new Error("missing tool item");
  }
  return item;
}

describe("轨迹工具行（chat.sse.tool_* 原始载荷）", () => {
  it("对象入参序列化为摘要，content 结果与 duration_ms 落进 head", () => {
    const { snapshot } = applyEvents(createEmptyTrajectory(), [
      makeTrajectoryEvent("tool_call", 1, TOOL_CALL_FRAME),
      makeTrajectoryEvent("tool_end", 2, TOOL_END_FRAME),
    ]);

    expect(snapshot.items).toHaveLength(1);
    const item = toolItem(snapshot.items);
    expect(item.id).toBe("tool:observation_step_1_tool_0");
    expect(item.status).toBe("completed");
    expect(item.head.kind).toBe("tool");
    if (item.head.kind !== "tool") {
      return;
    }
    expect(item.head.name).toBe("shell");
    expect(item.head.phase).toBe("finished");
    expect(item.head.argsSummary).toContain("git status");
    expect(item.head.argsSummary).toContain("E:\\\\temp");
    expect(item.head.resultSummary).toContain("Exit code: 1");
    expect(item.head.durationMs).toBe(506);
  });

  it("tool 上无入参时回退 tool_call.arguments / delta.arguments", () => {
    const { snapshot } = applyEvents(createEmptyTrajectory(), [
      makeTrajectoryEvent("tool_call", 1, {
        tool: { id: "call-9", name: "read_file" },
        tool_call: { id: "call-9", arguments: { file_path: "src/app.tsx" } },
      }),
    ]);

    const item = toolItem(snapshot.items);
    if (item.head.kind !== "tool") {
      return;
    }
    expect(item.head.argsSummary).toContain("src/app.tsx");
  });

  it("已收敛的 args_summary/output 原样透传，不做二次截断", () => {
    const longOutput = "y".repeat(1500);
    const { snapshot } = applyEvents(createEmptyTrajectory(), [
      makeTrajectoryEvent("tool_start", 1, {
        tool: { id: "call-3", name: "bash", args_summary: "ls -la" },
      }),
      makeTrajectoryEvent("tool_end", 2, {
        tool: { id: "call-3", name: "bash", output: longOutput },
      }),
    ]);

    const item = toolItem(snapshot.items);
    if (item.head.kind !== "tool") {
      return;
    }
    expect(item.head.argsSummary).toBe("ls -la");
    expect(item.head.resultSummary).toBe(longOutput);
  });
});
