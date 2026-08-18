/**
 * Trajectory 渲染模型（web 侧）
 *
 * 对齐 TUI `render-model-spec.md`（Item/ChangeSet 语义）：
 * - 渲染顺序唯一来源 = Items 数组顺序；身份唯一来源 = Item.id（reducer 稳定分配）；
 * - 子事件经 CauseID 锚定父块；渲染层只读 Items，一切变更经 reducer。
 *
 * 事件源：/api/agent/chat SSE（13 类事件 + error），每帧 payload._event.sequence
 * 为 EventStore 持久化 seq（Phase 0 已落地）。
 */

/** SSE 事件名（与 /api/agent/chat 的 event: 行一一对应）。 */
export type TrajectoryEventKind =
  | "meta"
  | "chunk"
  | "reasoning"
  | "tool_start"
  | "tool_call"
  | "tool_end"
  | "planning"
  | "orchestration"
  | "route"
  | "observation"
  | "subagent"
  | "result"
  | "done"
  | "error"
  /** 非 chat SSE 的 runtime 生命周期事件（approval/compact/session 等，Q4）。 */
  | "runtime";

/** Item 生命周期状态（render-model-spec §4.1）。 */
export type TrajectoryItemStatus =
  | "pending"
  | "running"
  | "completed"
  | "failed"
  | "canceled";

/** Item 信息块类型。 */
export type TrajectoryItemKind =
  | "assistant"
  | "reasoning"
  | "tool"
  | "planning"
  | "orchestration"
  | "route"
  | "observation"
  | "subagent"
  | "result"
  | "system";

/** 工具块展示阶段（对齐 MessageSegment.tool.status）。 */
export type TrajectoryToolPhase = "started" | "running" | "finished" | "error";

/** Item 渲染内容头（增量 upsert 的落点）。 */
export type TrajectoryHead =
  | {
      kind: "text";
      content: string;
      index?: number;
      totalChars?: number;
    }
  | {
      kind: "reasoning";
      content: string;
      delta?: string;
    }
  | {
      kind: "tool";
      name: string;
      phase: TrajectoryToolPhase;
      toolCallId?: string;
      argsSummary?: string;
      resultSummary?: string;
      errorMessage?: string;
      durationMs?: number;
    }
  | {
      kind: "structured";
      /** planning/orchestration/route/observation/subagent 原始载荷。 */
      payload: Record<string, unknown>;
      summary?: string;
    }
  | {
      kind: "system";
      note: string;
    };

export interface TrajectoryItem {
  /** 稳定身份（reducer 分配，重放确定）。 */
  id: string;
  /** 提交序号（事件到达序；乱序事件按 seq 插入）。 */
  seq: number;
  kind: TrajectoryItemKind;
  /** 因果锚定：工具调用 id；"" = top-level。 */
  causeId: string;
  status: TrajectoryItemStatus;
  head: TrajectoryHead;
  /** 创建/最近更新的事件 seq（单调，审计与重放诊断）。 */
  createdAt: number;
  updatedAt: number;
}

/** reducer 输入：一条轨迹事件。 */
export interface TrajectoryEvent {
  kind: TrajectoryEventKind;
  /** SSE 帧 _event.sequence（EventStore 持久化 seq；0 = 未知/降级）。 */
  seq: number;
  payload: Record<string, unknown>;
}

export interface TrajectorySnapshot {
  items: TrajectoryItem[];
  /** item-{nextId} 分配游标。 */
  nextId: number;
  /** 已应用的最大事件 seq（断点续传；乱序缓冲的边界）。 */
  lastEventSeq: number;
  /** itemId -> revision（upsert 递增；ChangeSet 去重用）。 */
  revisions: Record<string, number>;
  /** 乱序缓冲：seq -> 事件（等待前序 seq 补齐）。 */
  pending: Record<number, TrajectoryEvent>;
}

export type TrajectoryChangeOp = "append" | "upsert" | "remove";

export interface TrajectoryChange {
  op: TrajectoryChangeOp;
  itemId: string;
  /** remove 时为空。 */
  item?: TrajectoryItem;
  revision: number;
}

export interface TrajectoryChangeSet {
  changes: TrajectoryChange[];
  snapshot: TrajectorySnapshot;
}

export function createEmptyTrajectory(): TrajectorySnapshot {
  return {
    items: [],
    nextId: 1,
    lastEventSeq: 0,
    revisions: {},
    pending: {},
  };
}
