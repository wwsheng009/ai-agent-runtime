/**
 * 轨迹恢复的「历史兜底」编排（P4）
 *
 * 常规恢复计划由 EventStore 事件序列决定（`chat.sse.*` 内容帧 + runtime 白名单
 * 生命周期帧）。当会话没有任何内容帧（见 `isTrajectoryContentEvent`）时，轨迹
 * 只剩 system 行——本模块把会话历史投影出的消息行（session-history.ts）插回
 * 该计划的正确位置，使轨迹读起来仍是完整的一轮：
 *
 *   session 开始行 → 压缩等中间行 → 该轮的用户/助手/工具消息 → 该轮收尾行（session_end）
 *
 * - 事件里没有出现的 turn（事件被裁剪/仅历史可见）与无 turn 归属的消息，
 *   按历史顺序追加在末尾，保证「所有消息都能看到」；
 * - skip 步（被白名单过滤事件的 seq 空洞）保持在原位置，续传游标语义不变。
 */
import type { SessionHistoryMessage, SessionRuntimeEvent } from "@/types/runtime";

import { getSessionHistory } from "@/api/runtime/sessions";
import { isTrajectoryContentEvent, trajectoryEventAction, type TrajectoryRecoveryPush } from "./recovery";
import { groupSessionHistoryTrajectoryPushes } from "./session-history";

/** 历史兜底分页大小（后端默认 100、上限 1000；游标 `before_seq` 由新到旧翻页）。 */
export const HISTORY_PAGE_SIZE = 1000;
/** 历史兜底页数上限（覆盖超长会话，避免异常游标导致无限翻页）。 */
export const HISTORY_MAX_PAGES = 40;

/**
 * 分页拉取整段会话历史（按时间顺序）。
 *
 * 后端按「新 → 旧」翻页（`before_seq` / `next_before_seq`），回放需要正序，
 * 因此逐页收集后反转拼接；轨迹恢复与导出共用同一份分页语义。
 * `isCancelled` 返回 true 时提前退出并返回空数组（调用方丢弃结果）。
 */
export async function fetchSessionHistoryMessages(
  sessionId: string,
  isCancelled: () => boolean = () => false,
): Promise<SessionHistoryMessage[]> {
  const pages: SessionHistoryMessage[][] = [];
  let beforeSeq: number | undefined;
  for (let page = 0; page < HISTORY_MAX_PAGES; page += 1) {
    const response = await getSessionHistory(sessionId, {
      limit: HISTORY_PAGE_SIZE,
      beforeSeq,
    });
    if (isCancelled()) {
      return [];
    }
    const history = Array.isArray(response.history) ? response.history : [];
    pages.push(history);
    const next = response.next_before_seq;
    if (
      !response.has_more ||
      history.length === 0 ||
      typeof next !== "number" ||
      !Number.isFinite(next) ||
      next <= 0
    ) {
      break;
    }
    beforeSeq = next;
  }
  return pages.reverse().flat();
}

/** 恢复计划的一步：push 轨迹帧，或 skip 前移续传游标（跳过被过滤事件的 seq 空洞）。 */
export type TrajectoryReplayStep =
  | {
      type: "push";
      push: TrajectoryRecoveryPush;
      /** 原始 runtime 事件类型（chat.sse.* 帧取 kind）；用于判断 turn 收尾行。 */
      runtimeType: string;
      turnId: string;
    }
  | {
      type: "skip";
      seq: number;
      runtimeType: string;
      turnId: string;
    };

/** 计划里每一步的墙钟时间（`_event.timestamp`，RFC3339；缺失为空串）。 */
function stepTimestamp(step: TrajectoryReplayStep): string {
  if (step.type !== "push") {
    return "";
  }
  const envelope = step.push.payload["_event"];
  if (!envelope || typeof envelope !== "object") {
    return "";
  }
  return readTrimmedString((envelope as Record<string, unknown>)["timestamp"]);
}

/**
 * 每一步「最近已知时间」：优先自身或之前最近的事件时间，之前都没有则取之后最近的。
 *
 * 会话历史（`/history`）只返回 role/content/metadata，没有墙钟时间；用相邻生命周期
 * 事件的时间回填后，历史兜底会话的时间线也能用真实时间轴（缺失则仍是序号轴）。
 */
function nearestKnownTimestamps(steps: TrajectoryReplayStep[]): string[] {
  const own = steps.map(stepTimestamp);
  const times = [...own];
  let previous = "";
  for (let index = 0; index < times.length; index += 1) {
    if (own[index]) {
      previous = own[index];
    }
    times[index] = previous;
  }
  let next = "";
  for (let index = times.length - 1; index >= 0; index -= 1) {
    if (own[index]) {
      next = own[index];
    }
    if (!times[index]) {
      times[index] = next;
    }
  }
  return times;
}

/** 把 `count` 条历史行均匀铺在 [startIso, endIso] 上（只有单点时间则共用同一时间）。 */
function spreadTimestamps(
  startIso: string,
  endIso: string,
  count: number,
): string[] {
  const times: string[] = [];
  if (count <= 0) {
    return times;
  }
  const start = Date.parse(startIso);
  const end = Date.parse(endIso);
  if (Number.isFinite(start) && Number.isFinite(end) && end > start && count > 1) {
    for (let index = 0; index < count; index += 1) {
      const ms = start + ((end - start) * (index + 1)) / count;
      times.push(new Date(Math.round(ms)).toISOString());
    }
    return times;
  }
  const single = Number.isFinite(end) ? endIso : Number.isFinite(start) ? startIso : "";
  for (let index = 0; index < count; index += 1) {
    times.push(single);
  }
  return times;
}

/** 给历史行补 `_event.timestamp`（保留既有 envelope；无可用时间时原样返回）。 */
function stampPushes(
  pushes: TrajectoryRecoveryPush[],
  times: string[],
): TrajectoryRecoveryPush[] {
  return pushes.map((push, index) => {
    const time = times[index] ?? "";
    if (!time) {
      return push;
    }
    const envelope = push.payload["_event"];
    const base =
      envelope && typeof envelope === "object"
        ? (envelope as Record<string, unknown>)
        : {};
    return {
      kind: push.kind,
      payload: { ...push.payload, _event: { ...base, timestamp: time } },
    };
  });
}

/** turn 收尾事件：该轮消息插在这些行之前，读起来才是完整一轮。 */
export const TURN_TAIL_EVENT_TYPES: ReadonlySet<string> = new Set([
  "session_end",
  "session_interrupted",
]);

function readTrimmedString(value: unknown): string {
  return typeof value === "string" ? value.trim() : "";
}

function pushStep(
  push: TrajectoryRecoveryPush,
  turnId: string,
): TrajectoryReplayStep {
  return {
    type: "push",
    push,
    runtimeType:
      readTrimmedString(push.payload["runtime_type"]) || push.kind,
    turnId,
  };
}

/** 事件序列 → 恢复计划（保持事件顺序；被过滤事件的空洞以 skip 步前移游标）。 */
export function trajectoryReplaySteps(
  events: SessionRuntimeEvent[],
): TrajectoryReplayStep[] {
  const steps: TrajectoryReplayStep[] = [];
  for (const event of events) {
    const action = trajectoryEventAction(event);
    const turnId = readTrimmedString(event.payload?.turn_id);
    if (action.kind === "push") {
      steps.push(pushStep(action.push, turnId));
    } else if (action.kind === "skip") {
      steps.push({
        type: "skip",
        seq: action.seq,
        runtimeType: event.type,
        turnId,
      });
    }
  }
  return steps;
}

/** 会话是否有可渲染的内容帧（false → 需要历史兜底，否则轨迹只剩 system 行）。 */
export function hasTrajectoryContentFrames(
  events: SessionRuntimeEvent[],
): boolean {
  return events.some(isTrajectoryContentEvent);
}

/**
 * 把会话历史投影插回恢复计划（按 turn 归并）。
 *
 * 插入点：该 turn 的收尾事件（session_end/session_interrupted）之前。两个通道：
 *
 * 1. turn_id 相等匹配（HTTP chat 会话：事件与消息共享同一 turn id）；
 * 2. 位置对齐（aicli 进程内 chat 会话：生命周期事件的 `turn_id` 是 UUID、
 *    消息元数据里是另一套 hash id，永远不相等；但两侧都按时间有序，
 *    因此按顺序一一对应）。正在进行的最后一轮还没有 `session_end`，
 *    所以历史组数通常多于收尾行数——多出的组按历史顺序追加在末尾。
 *
 * 没有命中收尾事件的 turn 与无 turn 归属的消息，按历史顺序追加在末尾，
 * 保证「所有消息都能看到」。
 */
export function mergeTrajectoryHistoryFallback(
  steps: TrajectoryReplayStep[],
  history: SessionHistoryMessage[],
): TrajectoryReplayStep[] {
  const groups = groupSessionHistoryTrajectoryPushes(history);
  const pushesByTurn = new Map<string, TrajectoryRecoveryPush[]>();
  /** 历史里 turn 首次出现的顺序（= 时间顺序，供位置对齐使用）。 */
  const orderedTurnIds: string[] = [];
  for (const group of groups) {
    if (!group.turnId) {
      continue;
    }
    const existing = pushesByTurn.get(group.turnId);
    if (existing) {
      existing.push(...group.pushes);
    } else {
      pushesByTurn.set(group.turnId, [...group.pushes]);
      orderedTurnIds.push(group.turnId);
    }
  }

  const tailStepIndexes = steps
    .map((step, index) =>
      TURN_TAIL_EVENT_TYPES.has(step.runtimeType) ? index : -1,
    )
    .filter((index) => index >= 0);

  // 主通道：事件与消息的 turn_id 可直接相等匹配。
  const idMatched = tailStepIndexes.some((index) =>
    pushesByTurn.has(steps[index].turnId),
  );
  // 备用通道：组数 ≥ 收尾行数时从最早的组开始按位置配对（每一轮的消息插在该轮
  // session_end 之前），多出的组追加在末尾。收尾行更多说明事件里有历史看不到的
  // 轮次（历史被裁剪），此时宁可退化为「追加在末尾」也不把消息错插到别的轮次。
  const usePositional =
    !idMatched &&
    orderedTurnIds.length > 0 &&
    orderedTurnIds.length >= tailStepIndexes.length;

  /** 收尾行的下标 → 插在它之前的历史行。 */
  const pushesBeforeTail = new Map<number, TrajectoryRecoveryPush[]>();
  const emittedTurns = new Set<string>();
  tailStepIndexes.forEach((index, order) => {
    const turnId = usePositional ? orderedTurnIds[order] : steps[index].turnId;
    if (!turnId || emittedTurns.has(turnId)) {
      return;
    }
    const pushes = pushesByTurn.get(turnId);
    if (!pushes) {
      return;
    }
    emittedTurns.add(turnId);
    pushesBeforeTail.set(index, pushes);
  });

  const out: TrajectoryReplayStep[] = [];
  const appendPushes = (pushes: TrajectoryRecoveryPush[], turnId: string) => {
    for (const push of pushes) {
      out.push(pushStep(push, turnId));
    }
  };
  const stepTimes = nearestKnownTimestamps(steps);
  steps.forEach((step, index) => {
    const pushes = pushesBeforeTail.get(index);
    if (pushes) {
      appendPushes(
        stampPushes(
          pushes,
          spreadTimestamps(stepTimes[index - 1] ?? "", stepTimes[index] ?? "", pushes.length),
        ),
        step.turnId,
      );
    }
    out.push(step);
  });

  // 兜底：事件里没有收尾行的 turn、无 turn 归属的消息，按历史顺序追加。
  const appendedTurns = new Set<string>();
  const pendingAppends: Array<{ turnId: string; pushes: TrajectoryRecoveryPush[] }> = [];
  for (const group of groups) {
    if (group.turnId) {
      if (emittedTurns.has(group.turnId) || appendedTurns.has(group.turnId)) {
        continue;
      }
      appendedTurns.add(group.turnId);
    }
    pendingAppends.push({ turnId: group.turnId, pushes: group.pushes });
  }
  const appendedCount = pendingAppends.reduce(
    (sum, entry) => sum + entry.pushes.length,
    0,
  );
  const appendedTimes = spreadTimestamps(
    stepTimes[0] ?? "",
    stepTimes[stepTimes.length - 1] ?? "",
    appendedCount,
  );
  let appendedOffset = 0;
  for (const entry of pendingAppends) {
    const times = appendedTimes.slice(appendedOffset, appendedOffset + entry.pushes.length);
    appendedOffset += entry.pushes.length;
    appendPushes(stampPushes(entry.pushes, times), entry.turnId);
  }

  return out;
}
