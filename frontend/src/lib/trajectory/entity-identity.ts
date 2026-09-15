/**
 * 统一实体身份（P1-2，批次 20）。
 *
 * 背景（批次 19 风险 b2 / a3+c4 / b4）：轨迹里的行身份此前由各处分别拼装——
 * reducer 用 `tool:${call_id}`，缺 id 时退化成 `tool-${seq}`，runtime 行用
 * `runtime-${seq}`，子代理行用 `subagent-${seq}`。同一实体在不同通道拿到不同
 * 名字，于是「同一次调用两行」「多个子代理一行」既无法合并，也**不可观测**：
 * 用户只看到重复行或互相覆盖，没有任何信号说明身份是猜出来的。
 *
 * 本模块把命名收敛到一处：`<kind>:<id>`（与 reducer 既有 `tool:<id>` 方案
 * 兼容），并给出随帧身份对（后端 `entity: { kind, id, degraded }`）的读取契约：
 *
 * - 有权威 id ⇒ `tool:<call_id>` / `subagent:<child_session_id>`，可合并；
 * - 无权威 id ⇒ 兜底身份必须显式 `degraded`，UI 与导出可据此提示「无法合并」。
 */
import type { TrajectoryItemStatus } from "./types";

/** 参与身份合并的实体种类（工具调用、子代理会话）。 */
export type TrajectoryEntityKind = "tool" | "subagent";

/** 帧上携带的实体身份（后端 entity 对象 / 前端推导结果同形）。 */
export type TrajectoryEntity = {
  kind: TrajectoryEntityKind;
  id: string;
  /** true = 兜底身份（由 seq 派生），无法与权威帧合并。 */
  degraded?: boolean;
};

/** 帧 payload 上的实体身份键（后端 buildStaticToolEventPayload 等写入）。 */
export const TRAJECTORY_ENTITY_KEY = "entity";

/** `<kind>:<id>`：轨迹 item 身份的唯一拼装点。 */
export function entityItemId(kind: TrajectoryEntityKind, id: string): string {
  return `${kind}:${id}`;
}

export function toolItemId(callId: string): string {
  return entityItemId("tool", callId);
}

export function subagentItemId(childSessionId: string): string {
  return entityItemId("subagent", childSessionId);
}

function readText(value: unknown): string {
  return typeof value === "string" ? value.trim() : "";
}

/**
 * 读取帧上的实体身份。
 *
 * 只接受完整对（kind 合法 + id 非空）；`degraded` 缺省为 false——后端只在
 * 兜底身份时才带该键，其余情况按可合并处理。
 */
export function readTrajectoryEntity(
  payload: Record<string, unknown> | undefined,
): TrajectoryEntity | null {
  if (!payload) {
    return null;
  }
  const raw = payload[TRAJECTORY_ENTITY_KEY];
  if (!raw || typeof raw !== "object") {
    return null;
  }
  const record = raw as Record<string, unknown>;
  const kind = readText(record.kind);
  if (kind !== "tool" && kind !== "subagent") {
    return null;
  }
  const id = readText(record.id);
  if (!id) {
    return null;
  }
  return record.degraded === true ? { kind, id, degraded: true } : { kind, id };
}

/** 子代理行身份来源：子会话 id（`session_id` 优先，其次 `agent_id`）。 */
export function subagentChildIdOf(payload: Record<string, unknown>): string {
  for (const key of ["session_id", "sessionId", "agent_id", "agentId"]) {
    const value = readText(payload[key]);
    if (value) {
      return value;
    }
  }
  return "";
}

/**
 * 子代理行状态：尾帧 `success` 布尔是唯一权威终态信号（`handler.go`
 * buildSubagentEventPayloads 恒写入）；live 进度镜像没有该键 ⇒ 保持 running
 * （终态行会被 upsertItem 冻结，镜像就无法就地更新）。
 */
export function subagentStatusOf(
  payload: Record<string, unknown>,
): TrajectoryItemStatus {
  const success = payload["success"];
  if (success === true) {
    return "completed";
  }
  if (success === false) {
    return "failed";
  }
  return "running";
}

/**
 * 子代理行身份：优先 `subagent:<child_session_id>`（与实时进度镜像共用同一 id，
 * 于是「镜像更新」与「终态结果」落在同一行）；缺 session 标识时才退回 seq 兜底
 * （`subagent-<seq>`，一行一条事件，属降级形态）。
 */
export function subagentRowIdOf(
  payload: Record<string, unknown>,
  seq: number,
): string {
  const childId = subagentChildIdOf(payload);
  return childId ? subagentItemId(childId) : `subagent-${seq}`;
}

/**
 * 消息行身份（正文 / 思考）：`assistant:<turn_id>` / `reasoning:<turn_id>`。
 *
 * 背景（用户实测上报：live 渲染「没有顺序概念」，已渲染的消息在下一次渲染时
 * 被重排）：正文与思考此前共用**全局固定 id**（`assistant` / `reasoning`），
 * 于是
 *
 * - 行的位置被钉死在**创建它的那一帧**（本批实测：整窗 66 行里正文行恒为第 0
 *   项，其后 31 个工具行 + 30 个观察行全部追加在它之后）——同一行跨越所有轮次，
 *   新内容不会出现在它所属的时间位置上；
 * - `done` 的 `finalizeOpenItems` 会把仍在 running 的行冻结为终态，而
 *   `upsertItem` 对终态行**拒绝 upsert**：下一轮的增量帧命中同一 id 时被静默
 *   丢弃（要么不渲染，要么只能落到别处），表现为「已经渲染的消息被重新排」。
 *
 * 因此把身份按轮次收敛（与工具 `tool:<call_id>`、子代理 `subagent:<child>`
 * 同一套 `<kind>:<id>` 命名）：每轮各自的正文/思考成行、按到达序追加，
 * 已渲染行不再被后续轮次复用或冻结。
 *
 * 无 turn 标识（历史兜底帧 / 旧帧 / 降级帧）时退回既有全局 id，语义不变。
 */
export function messageRowIdOf(
  payload: Record<string, unknown>,
  kind: "assistant" | "reasoning",
): string {
  const turnId = readText(payload["turn_id"]) || readText(payload["turnId"]);
  return turnId ? `${kind}:${turnId}` : kind;
}
