/**
 * Trajectory reducer（纯函数）
 *
 * 对齐 TUI `encoder.go` / `render-model-spec.md` §4/§6 语义：
 * - 提交语义：append / upsert / remove，幂等规则见 spec §4.2；
 * - 状态机：pending → running → completed / failed / canceled；终态后仅允许 remove；
 * - 乱序免疫：事件带持久化 seq，未按序事件先缓冲，前序补齐后按序应用；
 * - upsert 退化规则：按 ID 找不到时退化为 append（输出先于调用到达时自成一个块）。
 *
 * 不变式：同一事件序列重放 → 相同快照（ID/Seq 由事件内容确定性派生）。
 */
import type {
  TrajectoryChange,
  TrajectoryChangeSet,
  TrajectoryEvent,
  TrajectoryHead,
  TrajectoryItem,
  TrajectoryItemKind,
  TrajectoryItemStatus,
  TrajectorySnapshot,
  TrajectoryToolPhase,
} from "./types";

export const TERMINAL_STATUSES: ReadonlySet<TrajectoryItemStatus> = new Set([
  "completed",
  "failed",
  "canceled",
]);

export function makeTrajectoryEvent(
  kind: TrajectoryEvent["kind"],
  seq: number,
  payload: Record<string, unknown>,
): TrajectoryEvent {
  return { kind, seq, payload };
}

/** runtime 生命周期事件 → 单行说明（Q4：approval/compact/session 等人类可读摘要）。 */
export function describeRuntimeEvent(
  payload: Record<string, unknown>,
): string {
  const runtimeType = readString(payload["runtime_type"]);
  const toolName = readFirstString(payload, ["tool_name", "tool"]);
  const reason = readFirstString(payload, ["reason", "error", "message"]);
  const tokenBefore = readNumber(payload["token_before"]);
  const tokenAfter = readNumber(payload["token_after"]);
  const messageCountAfter = readNumber(payload["message_count_after"]);

  switch (runtimeType) {
    case "approval_requested":
      return toolName
        ? `approval requested: ${toolName}`
        : "approval requested";
    case "approval_resolved": {
      // 后端 approvalResolvedEventPayload 用 allowed 字段（actor.go:3126）；
      // approved 仅为兼容兜底（旧数据/测试夹具）。
      const approvedValue = payload["allowed"] ?? payload["approved"];
      const verb = approvedValue === false ? "rejected" : "approved";
      return toolName ? `approval ${verb}: ${toolName}` : `approval ${verb}`;
    }
    case "session_compact_started":
      return tokenBefore !== undefined
        ? `context compaction started: ${tokenBefore} tokens`
        : "context compaction started";
    case "session_compact_completed":
      if (tokenBefore !== undefined && tokenAfter !== undefined) {
        return `context compacted: ${tokenBefore} → ${tokenAfter} tokens`;
      }
      if (messageCountAfter !== undefined) {
        return `context compacted: ${messageCountAfter} messages`;
      }
      return "context compacted";
    case "session_compact_skipped":
      return reason
        ? `context compaction skipped: ${reason}`
        : "context compaction skipped";
    case "session_compact_failed":
      return reason
        ? `context compaction failed: ${reason}`
        : "context compaction failed";
    case "session_start":
      return "session started";
    case "session_end":
      return "session ended";
    case "session_interrupted":
      return "session interrupted";
    case "context_reconciled":
      return "context reconciled";
    case "checkpoint_created":
      return "checkpoint created";
    default:
      return runtimeType || "runtime event";
  }
}

/** 从 SSE payload 提取持久化 seq（_event.sequence）。 */
export function eventSeqOf(payload: Record<string, unknown>): number {
  const envelope = payload["_event"];
  if (envelope && typeof envelope === "object") {
    const sequence = (envelope as Record<string, unknown>)["sequence"];
    if (typeof sequence === "number" && Number.isFinite(sequence)) {
      return sequence;
    }
    if (typeof sequence === "string") {
      const parsed = Number(sequence);
      if (Number.isFinite(parsed)) {
        return parsed;
      }
    }
  }
  return 0;
}

function readString(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function readFirstString(
  container: Record<string, unknown> | undefined | null,
  keys: string[],
): string {
  if (!container || typeof container !== "object") {
    return "";
  }
  for (const key of keys) {
    const value = readString(container[key]);
    if (value.trim()) {
      return value;
    }
  }
  return "";
}

function readNumber(value: unknown): number | undefined {
  if (typeof value === "number" && Number.isFinite(value)) {
    return value;
  }
  return undefined;
}

/** 从 chunk 载荷中提取文本 delta（对齐 workspace-thread-state.getStreamTextDelta）。 */
export function textDeltaOf(payload: Record<string, unknown>): string {
  const type = readString(payload["type"]);
  if (type && type !== "text") {
    return "";
  }
  const content = readString(payload["content"]);
  if (content) {
    return content;
  }
  const text = payload["text"];
  if (text && typeof text === "object") {
    return readString((text as Record<string, unknown>)["content"]);
  }
  return "";
}

/** 从 chunk 载荷中提取工具调用 id（对齐 getToolCallId）。 */
export function toolCallIdOf(payload: Record<string, unknown>): string {
  const toolCall = payload["tool_call"];
  if (toolCall && typeof toolCall === "object") {
    const id = readFirstString(toolCall as Record<string, unknown>, [
      "id",
      "tool_call_id",
    ]);
    if (id) {
      return id;
    }
  }
  const tool = payload["tool"];
  if (tool && typeof tool === "object") {
    return readFirstString(tool as Record<string, unknown>, [
      "id",
      "tool_call_id",
    ]);
  }
  return "";
}

/** 从 chunk 载荷中提取工具名（对齐 getToolName）。 */
export function toolNameOf(payload: Record<string, unknown>): string {
  const tool = payload["tool"];
  if (tool && typeof tool === "object") {
    const name = readString((tool as Record<string, unknown>)["name"]);
    if (name) {
      return name;
    }
  }
  const toolCall = payload["tool_call"];
  if (toolCall && typeof toolCall === "object") {
    const name = readString((toolCall as Record<string, unknown>)["name"]);
    if (name) {
      return name;
    }
  }
  return "tool";
}

function toolErrorOf(payload: Record<string, unknown>): string {
  const tool = payload["tool"];
  if (tool && typeof tool === "object") {
    const error = readFirstString(tool as Record<string, unknown>, [
      "error",
      "error_message",
      "message",
    ]);
    if (error) {
      return error;
    }
  }
  const result = payload["result"];
  if (result && typeof result === "object") {
    return readFirstString(result as Record<string, unknown>, [
      "error",
      "error_message",
      "message",
    ]);
  }
  return "";
}

function toolArgsSummaryOf(payload: Record<string, unknown>): string {
  const tool = payload["tool"];
  if (tool && typeof tool === "object") {
    return readFirstString(tool as Record<string, unknown>, [
      "args_summary",
      "arguments_summary",
      "args",
      "arguments",
    ]);
  }
  const toolCall = payload["tool_call"];
  if (toolCall && typeof toolCall === "object") {
    return readFirstString(toolCall as Record<string, unknown>, [
      "args_summary",
      "arguments_summary",
      "args",
      "arguments",
    ]);
  }
  return "";
}

function toolResultSummaryOf(payload: Record<string, unknown>): string {
  const tool = payload["tool"];
  if (tool && typeof tool === "object") {
    return readFirstString(tool as Record<string, unknown>, [
      "output_summary",
      "result_summary",
      "output",
      "result",
    ]);
  }
  return readFirstString(payload, ["output_summary", "result_summary"]);
}

function cloneSnapshot(snapshot: TrajectorySnapshot): TrajectorySnapshot {
  return {
    items: snapshot.items.map((item) => ({ ...item })),
    nextId: snapshot.nextId,
    lastEventSeq: snapshot.lastEventSeq,
    revisions: { ...snapshot.revisions },
    pending: { ...snapshot.pending },
  };
}

function cloneItem(item: TrajectoryItem): TrajectoryItem {
  return { ...item, head: { ...item.head } };
}

function appendChange(
  changes: TrajectoryChange[],
  op: TrajectoryChange["op"],
  itemId: string,
  item: TrajectoryItem | undefined,
  revision: number,
) {
  changes.push({ op, itemId, item, revision });
}

function headsEqual(a: TrajectoryHead, b: TrajectoryHead): boolean {
  if (a.kind !== b.kind) {
    return false;
  }
  switch (a.kind) {
    case "text":
      return b.kind === "text" && a.content === b.content;
    case "reasoning":
      return b.kind === "reasoning" && a.content === b.content;
    case "tool":
      return (
        b.kind === "tool" &&
        a.name === b.name &&
        a.phase === b.phase &&
        a.argsSummary === b.argsSummary &&
        a.resultSummary === b.resultSummary &&
        a.errorMessage === b.errorMessage
      );
    case "structured":
      return (
        b.kind === "structured" &&
        JSON.stringify(a.payload) === JSON.stringify(b.payload)
      );
    case "system":
      return b.kind === "system" && a.note === b.note;
  }
}

function findItem(
  snapshot: TrajectorySnapshot,
  itemId: string,
): TrajectoryItem | undefined {
  return snapshot.items.find((item) => item.id === itemId);
}

/**
 * upsert：按 itemId 更新既有 Item；找不到则退化为 append（spec §4.2 幂等规则）。
 * 同 ID 同内容重复 upsert → 跳过（幂等）；终态后 upsert → 拒绝（仅允许 remove）。
 */
function upsertItem(
  snapshot: TrajectorySnapshot,
  changes: TrajectoryChange[],
  itemId: string,
  kind: TrajectoryItemKind,
  head: TrajectoryHead,
  status: TrajectoryItemStatus,
  eventSeq: number,
  causeId = "",
) {
  const existing = findItem(snapshot, itemId);
  if (existing) {
    if (TERMINAL_STATUSES.has(existing.status)) {
      return; // 终态冻结：拒绝 upsert
    }
    if (headsEqual(existing.head, head) && existing.status === status) {
      return; // 幂等跳过
    }
    const next = cloneItem(existing);
    next.head = head;
    next.status = status;
    next.updatedAt = eventSeq;
    const revision = (snapshot.revisions[itemId] ?? 0) + 1;
    snapshot.revisions[itemId] = revision;
    snapshot.items = snapshot.items.map((item) =>
      item.id === itemId ? next : item,
    );
    appendChange(changes, "upsert", itemId, next, revision);
    return;
  }

  const item: TrajectoryItem = {
    id: itemId,
    seq: eventSeq,
    kind,
    causeId,
    status,
    head,
    createdAt: eventSeq,
    updatedAt: eventSeq,
  };
  snapshot.items = [...snapshot.items, item];
  snapshot.nextId += 1;
  const revision = (snapshot.revisions[itemId] ?? 0) + 1;
  snapshot.revisions[itemId] = revision;
  appendChange(changes, "append", itemId, item, revision);
}

/** 应用单条事件（事件序已由调用方保证：seq 单调或经缓冲对齐）。 */
function applySequencedEvent(
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
      upsertItem(
        snapshot,
        changes,
        `runtime-${seq}`,
        "system",
        { kind: "system", note: describeRuntimeEvent(event.payload) },
        "completed",
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

/** remove：按 ID 移除；不存在则忽略（幂等）。 */
export function removeItem(
  snapshot: TrajectorySnapshot,
  itemId: string,
): TrajectoryChangeSet {
  const next = cloneSnapshot(snapshot);
  const changes: TrajectoryChange[] = [];
  const existing = findItem(next, itemId);
  if (!existing) {
    return { changes, snapshot: next };
  }
  next.items = next.items.filter((item) => item.id !== itemId);
  const revision = (next.revisions[itemId] ?? 0) + 1;
  next.revisions[itemId] = revision;
  appendChange(changes, "remove", itemId, undefined, revision);
  return { changes, snapshot: next };
}

/**
 * 应用单条事件：seq 幂等（<= lastEventSeq 跳过）、乱序缓冲、按序应用。
 * 返回变更集与推进后的快照。
 */
export function applyEvent(
  snapshot: TrajectorySnapshot,
  event: TrajectoryEvent,
): TrajectoryChangeSet {
  const next = cloneSnapshot(snapshot);
  const changes: TrajectoryChange[] = [];

  if (event.seq <= 0) {
    // 无持久化 seq（降级帧）：按到达序直接应用，不参与乱序/续传边界。
    changes.push(...applySequencedEvent(next, event));
    return { changes, snapshot: next };
  }

  if (event.seq <= next.lastEventSeq) {
    return { changes, snapshot: next }; // 幂等：重复事件跳过
  }

  if (event.seq > next.lastEventSeq + 1) {
    next.pending[event.seq] = event; // 乱序：缓冲等待前序
    return { changes, snapshot: next };
  }

  // 顺序就绪：应用本事件，再消费缓冲中的后续事件。
  const queue: TrajectoryEvent[] = [event];
  let cursor = next.lastEventSeq;
  while (queue.length > 0) {
    const current = queue.shift()!;
    cursor = current.seq;
    changes.push(...applySequencedEvent(next, current));
    const nextPending = next.pending[cursor + 1];
    if (nextPending) {
      delete next.pending[cursor + 1];
      queue.push(nextPending);
    }
  }
  next.lastEventSeq = cursor;
  return { changes, snapshot: next };
}

/**
 * 前移游标跳过"已知永久缺失"的持久化 seq 空洞。
 *
 * 背景：后端把 chat.sse.* 事件与 runtime 生命周期事件（tool_started/
 * tool_finished/context.profile.injected 等）写入同一 EventStore，
 * 在同一个全局 seq 序列上自增；前端渲染轨迹时按白名单过滤部分事件
 * （不重复呈现工具生命周期、不呈现 profile 注入等）。被过滤的事件在
 * seq 链上留下永久空洞——没有任何来源会再投递它们——若只在 applyEvent
 * 中等待 lastEventSeq+1，空洞之后（含新一轮 turn 的实时事件）将永远
 * 卡在 pending，轨迹只剩 seq=0 的降级事件（system 行）。
 *
 * 恢复/轮询链路持有完整事件列表，对每个被过滤事件的 seq 调用本函数：
 * 安全边界是绝不跳过已缓冲的真实事件（只跳过第一个 pending 之前的洞），
 * 跳过后再按与 applyEvent 相同的顺序消费循环续接 pending。
 */
export function advanceSeqCursor(
  snapshot: TrajectorySnapshot,
  targetSeq: number,
): TrajectoryChangeSet {
  const next = cloneSnapshot(snapshot);
  const changes: TrajectoryChange[] = [];

  if (targetSeq <= next.lastEventSeq) {
    return { changes, snapshot: next };
  }

  const pendingKeys = Object.keys(next.pending)
    .map((key) => Number(key))
    .filter((seq) => Number.isFinite(seq))
    .sort((left, right) => left - right);
  const firstPending = pendingKeys.length > 0 ? pendingKeys[0] : Infinity;
  const effective = Math.min(targetSeq, firstPending - 1);
  if (effective <= next.lastEventSeq) {
    return { changes, snapshot: next };
  }

  next.lastEventSeq = effective;

  // 与 applyEvent 相同的顺次消费循环：跳过空洞后立即续接可用的 pending。
  const queue: TrajectoryEvent[] = [];
  let cursor = next.lastEventSeq;
  const first = next.pending[cursor + 1];
  if (first) {
    delete next.pending[cursor + 1];
    queue.push(first);
  }
  while (queue.length > 0) {
    const current = queue.shift()!;
    cursor = current.seq;
    changes.push(...applySequencedEvent(next, current));
    const following = next.pending[cursor + 1];
    if (following) {
      delete next.pending[cursor + 1];
      queue.push(following);
    }
  }
  next.lastEventSeq = cursor;
  return { changes, snapshot: next };
}

/**
 * 批量应用（rAF 帧内一批）：逐个应用并合并 ChangeSet——
 * 同一 Item 的多次变更合并为最新快照（对齐 spec §6 去重合并）。
 */
export function applyEvents(
  snapshot: TrajectorySnapshot,
  events: TrajectoryEvent[],
): TrajectoryChangeSet {
  let current = snapshot;
  const merged = new Map<string, TrajectoryChange>();
  for (const event of events) {
    const result = applyEvent(current, event);
    current = result.snapshot;
    for (const change of result.changes) {
      merged.set(change.itemId, change);
    }
  }
  return { changes: [...merged.values()], snapshot: current };
}
