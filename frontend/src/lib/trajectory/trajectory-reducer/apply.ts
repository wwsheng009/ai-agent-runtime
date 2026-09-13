// 由 lib/trajectory/trajectory-reducer.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import { type TrajectoryChange, type TrajectoryEvent, type TrajectoryHead, type TrajectoryItemStatus, type TrajectorySnapshot, type TrajectoryToolPhase } from "../types";

import { describeRuntimeEvent, readFirstString, readNumber, readString, textDeltaOf, toolArgsSummaryOf, toolCallIdOf, toolErrorOf, toolNameOf, toolResultSummaryOf } from "./event-readers";
import { appendChange, cloneItem, findItem, upsertItem } from "./snapshot-ops";

/** 应用单条事件（事件序已由调用方保证：seq 单调或经缓冲对齐）。 */
export function applySequencedEvent(
  snapshot: TrajectorySnapshot,
  event: TrajectoryEvent,
): TrajectoryChange[] {
  const changes: TrajectoryChange[] = [];
  const seq = event.seq;

  // A transport duplicate may have a different durable EventStore sequence
  // from the original provider delta. It still has to advance the global
  // cursor, but must not append the content a second time.
  if (event.payload["__trajectory_skip"] === true) {
    return changes;
  }

  switch (event.kind) {
    case "meta":
    case "done":
      // meta：流启动信息，无渲染块；done：终态收尾。
      if (event.kind === "done") {
        finalizeOpenItems(snapshot, changes, event);
      }
      break;

    case "chunk": {
      const type = readString(event.payload["type"]);
      if (type === "reasoning") {
        applyReasoningEvent(snapshot, changes, event);
      } else if (type === "tool_call" || event.payload["tool_call"]) {
        applyToolEvent(snapshot, changes, event, "tool_call");
      } else if (type === "image") {
        // 图像进度在 chat 投影中由独立 placeholder 处理；轨迹层记录为 system note。
        upsertItem(
          snapshot,
          changes,
          `image-${seq}`,
          "system",
          { kind: "system", note: "image progress" },
          "completed",
          seq,
        );
      } else {
        const delta = textDeltaOf(event.payload);
        const existing = findItem(snapshot, "assistant");
        const nextHead: TrajectoryHead = {
          kind: "text",
          content: (existing?.head.kind === "text"
            ? existing.head.content
            : "") + delta,
          index: readNumber(event.payload["index"]),
          totalChars: readNumber(event.payload["total_chars"]),
        };
        upsertItem(
          snapshot,
          changes,
          "assistant",
          "assistant",
          nextHead,
          "running",
          seq,
        );
      }
      break;
    }

    case "reasoning":
      applyReasoningEvent(snapshot, changes, event);
      break;

    case "tool_start":
    case "tool_call":
    case "tool_end":
      applyToolEvent(snapshot, changes, event, event.kind);
      break;

    case "planning":
      upsertItem(
        snapshot,
        changes,
        "planning",
        "planning",
        { kind: "structured", payload: event.payload },
        "running",
        seq,
      );
      break;

    case "orchestration":
      upsertItem(
        snapshot,
        changes,
        "orchestration",
        "orchestration",
        { kind: "structured", payload: event.payload },
        "running",
        seq,
      );
      break;

    case "route":
      upsertItem(
        snapshot,
        changes,
        "route",
        "route",
        { kind: "structured", payload: event.payload },
        "running",
        seq,
      );
      break;

    case "observation":
      upsertItem(
        snapshot,
        changes,
        `observation-${seq}`,
        "observation",
        { kind: "structured", payload: event.payload },
        "completed",
        seq,
      );
      break;

    case "subagent":
      upsertItem(
        snapshot,
        changes,
        `subagent-${seq}`,
        "subagent",
        { kind: "structured", payload: event.payload },
        "running",
        seq,
      );
      break;

    case "result":
      upsertItem(
        snapshot,
        changes,
        "result",
        "result",
        { kind: "structured", payload: event.payload },
        "completed",
        seq,
      );
      break;

    case "runtime":
      // P1-5 方案 2：live-only 进度镜像（`subagent.progress`，seq=0）需要就地
      // 更新，故保持非终态（`upsertItem` 会冻结终态行，后续镜像将被丢弃）；
      // 持久化生命周期事件仍是一次性 completed 行。
      upsertItem(
        snapshot,
        changes,
        `runtime-${seq}`,
        "system",
        { kind: "system", note: describeRuntimeEvent(event.payload) },
        event.payload["live"] === true ? "running" : "completed",
        seq,
      );
      break;

    case "error": {
      const message = readFirstString(event.payload, ["message", "error"]);
      // 失败时冻结仍在运行中的块（保留部分内容，对齐 TUI failed 语义）。
      freezeOpenItems(snapshot, changes, "failed");
      upsertItem(
        snapshot,
        changes,
        `error-${seq}`,
        "system",
        { kind: "system", note: message || "runtime stream error" },
        "failed",
        seq,
      );
      break;
    }

    default:
      upsertItem(
        snapshot,
        changes,
        `unknown-${seq}`,
        "system",
        { kind: "system", note: `unknown event kind: ${event.kind}` },
        "completed",
        seq,
      );
  }

  return changes;
}

function applyReasoningEvent(
  snapshot: TrajectorySnapshot,
  changes: TrajectoryChange[],
  event: TrajectoryEvent,
) {
  const existing = findItem(snapshot, "reasoning");
  let delta = readString(event.payload["content"]);
  if (!delta && event.payload["reasoning"] && typeof event.payload["reasoning"] === "object") {
    delta = readString((event.payload["reasoning"] as Record<string, unknown>)["content"]);
  }
  if (!delta) {
    return; // 空 delta 不产生变更（保留 live reasoning 边界由 batch 层处理）
  }
  const nextHead: TrajectoryHead = {
    kind: "reasoning",
    content: (existing?.head.kind === "reasoning"
      ? existing.head.content
      : "") + delta,
    delta,
  };
  upsertItem(
    snapshot,
    changes,
    "reasoning",
    "reasoning",
    nextHead,
    "running",
    event.seq,
  );
}

/** 工具状态机：tool_start(started) → tool_call(running) → tool_end(finished/error)。 */
function applyToolEvent(
  snapshot: TrajectorySnapshot,
  changes: TrajectoryChange[],
  event: TrajectoryEvent,
  kind: TrajectoryEvent["kind"],
) {
  const toolCallId = toolCallIdOf(event.payload);
  const itemId = toolCallId ? `tool:${toolCallId}` : `tool-${event.seq}`;
  const name = toolNameOf(event.payload);
  const existing = findItem(snapshot, itemId);
  const currentPhase: TrajectoryToolPhase = existing?.head.kind === "tool"
    ? existing.head.phase
    : "started";

  let nextPhase: TrajectoryToolPhase = currentPhase;
  let nextStatus: TrajectoryItemStatus = "running";
  let errorMessage = existing?.head.kind === "tool"
    ? existing.head.errorMessage
    : undefined;

  if (kind === "tool_start") {
    nextPhase = "started";
  } else if (kind === "tool_call") {
    nextPhase = "running";
  } else if (kind === "tool_end") {
    const error = toolErrorOf(event.payload);
    if (error) {
      nextPhase = "error";
      nextStatus = "failed";
      errorMessage = error;
    } else {
      nextPhase = "finished";
      nextStatus = "completed";
    }
  }

  const head: TrajectoryHead = {
    kind: "tool",
    name,
    phase: nextPhase,
    toolCallId: toolCallId || undefined,
    argsSummary:
      toolArgsSummaryOf(event.payload) ||
      (existing?.head.kind === "tool" ? existing.head.argsSummary : undefined),
    resultSummary:
      toolResultSummaryOf(event.payload) ||
      (existing?.head.kind === "tool" ? existing.head.resultSummary : undefined),
    errorMessage,
  };
  upsertItem(
    snapshot,
    changes,
    itemId,
    "tool",
    head,
    nextStatus,
    event.seq,
    toolCallId,
  );
}

/** done 收尾：仍在 running/pending 的块置 completed（孤儿 final 直接终态）。 */
function finalizeOpenItems(
  snapshot: TrajectorySnapshot,
  changes: TrajectoryChange[],
  event: TrajectoryEvent,
) {
  for (const item of snapshot.items) {
    if (item.status === "running" || item.status === "pending") {
      const revision = (snapshot.revisions[item.id] ?? 0) + 1;
      snapshot.revisions[item.id] = revision;
      const next = cloneItem(item);
      next.status = "completed";
      next.updatedAt = event.seq;
      snapshot.items = snapshot.items.map((entry) =>
        entry.id === item.id ? next : entry,
      );
      appendChange(changes, "upsert", item.id, next, revision);
    }
  }
}

/** error 收尾：仍在运行中的块置 failed（保留部分内容）。 */
function freezeOpenItems(
  snapshot: TrajectorySnapshot,
  changes: TrajectoryChange[],
  status: "failed",
) {
  for (const item of snapshot.items) {
    if (item.status === "running" || item.status === "pending") {
      const revision = (snapshot.revisions[item.id] ?? 0) + 1;
      snapshot.revisions[item.id] = revision;
      const next = cloneItem(item);
      next.status = status;
      snapshot.items = snapshot.items.map((entry) =>
        entry.id === item.id ? next : entry,
      );
      appendChange(changes, "upsert", item.id, next, revision);
    }
  }
}
