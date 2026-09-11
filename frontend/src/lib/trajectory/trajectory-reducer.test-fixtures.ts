// P0-2 随源拆分：trajectory reducer 测试共享夹具（由 trajectory-reducer.test.ts 抽出）。
import { makeTrajectoryEvent } from "./trajectory-reducer";

export function chunk(
  seq: number,
  type: string,
  content: string,
  extra: Record<string, unknown> = {},
) {
  return makeTrajectoryEvent("chunk", seq, { type, content, ...extra });
}

export function reasoning(seq: number, content: string) {
  return makeTrajectoryEvent("reasoning", seq, { content });
}

export function toolEvent(
  kind: "tool_start" | "tool_call" | "tool_end",
  seq: number,
  toolCallId: string,
  name = "bash",
  extra: Record<string, unknown> = {},
) {
  return makeTrajectoryEvent(kind, seq, {
    type: "tool_call",
    tool_call: { id: toolCallId, name },
    ...extra,
  });
}
