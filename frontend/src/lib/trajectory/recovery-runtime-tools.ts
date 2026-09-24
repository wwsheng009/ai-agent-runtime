// P0-2 拆分（原 recovery.ts L431-L535）：ACP 运行时工具生命周期 → 轨迹工具 push。
// 拆出原因：单函数（含契约注释）≈100 非空行，独立成件后 recovery.ts 回到门禁以内。

import type { SessionRuntimeEvent } from "@/types/runtime";

import type { TrajectoryEventKind } from "./types";
import { chatSseEventSeq, readTrimmedString } from "./recovery-event-readers";
import type { TrajectoryRecoveryPush } from "./recovery-types";

/**
 * ACP 协议宿主会话（及旧版存档）的工具生命周期以 runtime 事件
 * `tool_started` / `tool_finished` / `tool_receipt_recorded` /
 * `tool_receipt_replayed` 的形式落库，而非 `chat.sse.tool_start/tool_end`
 * 桥接帧。恢复/实时链路原先把它们判为 `skip(seq)`（不在
 * RUNTIME_EVENT_TYPES 白名单），导致轨迹视图在恢复后丢失全部工具行。
 *
 * 这里把它们映射为轨迹工具 push（按 `tool_call_id` 折叠进同一
 * `tool:<call_id>` 行），补全工具信息：
 * - tool_started → kind="tool_start"（phase=started）；
 * - tool_finished → kind="tool_end"（phase=finished/error）；
 * - tool_receipt_recorded / tool_receipt_replayed → kind="tool_end"
 *   （回执承载完成证据：ok / message_bytes / sha256）；作为 tool_end
 *   的补充，折叠进既有行（upsertItem 幂等）。
 *
 * 载荷以 `tool_call: { id, name }` + `tool: { name, arguments?, output_summary?,
 * error?, duration_ms? }` 形态透传，event-readers 的
 * toolCallIdOf/toolNameOf/toolArgsSummaryOf/toolResultSummaryOf/
 * toolErrorOf/toolDurationMsOf 能直接读取。
 *
 * 后端补齐 chat.sse.tool_* 框架（ACP bridge）后，二者 tool_call_id
 * 一致 → upsertItem 折叠为一行：richer chat.sse 数据优先、runtime
 * 事件作为兜底/补充（reducer 在已有字段非空时不覆写）。
 *
 * seq 顺序前提：ACP 运行时按调用顺序落库（tool_started 早于
 * tool_finished/tool_receipt_recorded），因此 tool_start 建行时该调用
 * 尚无已完成行，无相位回退风险。纯回执（无 tool_started）直接建
 * 已完成行——既是该调用的全部可观测证据。
 */
export function runtimeToolEventToTrajectoryPush(
  event: SessionRuntimeEvent,
): TrajectoryRecoveryPush | null {
  const runtimeType = event.type;
  if (
    runtimeType !== "tool_started" &&
    runtimeType !== "tool_finished" &&
    runtimeType !== "tool_receipt_recorded" &&
    runtimeType !== "tool_receipt_replayed"
  ) {
    return null;
  }
  const payload = event.payload ?? {};
  const receipt =
    payload["receipt"] && typeof payload["receipt"] === "object"
      ? (payload["receipt"] as Record<string, unknown>)
      : undefined;
  const callId =
    readTrimmedString(payload["tool_call_id"]) ??
    readTrimmedString(receipt?.["tool_call_id"]) ??
    "";
  const name =
    readTrimmedString(event.tool_name) ??
    readTrimmedString(payload["tool_name"]) ??
    readTrimmedString(receipt?.["tool_name"]) ??
    "tool";
  const seq = chatSseEventSeq(event);
  const envelope: Record<string, unknown> = { sequence: seq };
  if (event.timestamp) {
    envelope.timestamp = event.timestamp;
  }

  // tool_started → tool_start（开始阶段）；其余 → tool_end（终态）。
  const kind: TrajectoryEventKind =
    runtimeType === "tool_started" ? "tool_start" : "tool_end";

  const tool: Record<string, unknown> = { name };
  // 入参：ACP runtime 载荷把参数放在 input/params/args/arguments。
  const args =
    payload["input"] ??
    payload["params"] ??
    payload["args"] ??
    payload["arguments"];
  if (args !== undefined) {
    tool["arguments"] = args;
  }

  if (kind === "tool_end") {
    // 完成/回执：输出、错误、耗时。
    const output = payload["output"] ?? receipt?.["output"];
    if (output !== undefined) {
      tool["output_summary"] = output;
    }
    const ok = payload["ok"] !== undefined ? payload["ok"] : receipt?.["ok"];
    if (ok === false) {
      const err =
        readTrimmedString(payload["error"]) ??
        readTrimmedString(receipt?.["error"]) ??
        readTrimmedString(payload["failure_category"]);
      tool["error"] = err || "tool failed";
    }
    const duration = payload["duration_ms"] ?? receipt?.["duration_ms"];
    if (typeof duration === "number" && duration > 0) {
      tool["duration_ms"] = duration;
    }
  }

  return {
    kind,
    payload: {
      tool_call: { id: callId, name },
      tool,
      _event: envelope,
    },
  };
}
