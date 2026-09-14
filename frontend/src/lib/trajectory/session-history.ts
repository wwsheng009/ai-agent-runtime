/**
 * 会话历史 → 轨迹投影（P4 兜底）
 *
 * 背景：轨迹视图的常规数据源是 EventStore 里的 `chat.sse.*` 帧（live + 恢复
 * 双通道）。但存在一类会话没有任何内容帧——由 aicli 进程内 chat 运行时执行
 * （HTTP /api/agent/chat 之外的入口）的会话只落 `session_start`/`session_end`/
 * `session_compact_*` 生命周期事件与 `aicli.chat.user_submitted`/
 * `assistant_message` 诊断事件，轨迹因此只剩 system 行。
 *
 * 这类会话的消息本体都在会话历史里（messages 持久化表，重启后仍在），所以
 * 本模块把历史消息投影成「降级轨迹帧」（seq = 0）：
 *
 * - user 消息 → kind "user"（一条消息一行）；
 * - assistant 正文 → kind "chunk"（type=text）；推理 → kind "reasoning"；
 * - assistant 工具调用 → kind "tool_start"；tool 结果 → kind "tool_end"
 *   （沿用 tool_call_id 作为 item 身份，与 live 帧共用同一工具状态机）；
 * - system/developer 消息是提示词脚手架，不属于会话消息，跳过。
 *
 * 契约：
 * - 所有行都带稳定身份（`TRAJECTORY_ITEM_ID_KEY` 或 tool_call_id）——降级帧
 *   没有 EventStore 游标可依赖，无稳定身份时多条消息会互相覆盖；
 * - seq = 0：按到达序应用，不参与续传游标（同 subagent 进度镜像的处理）。
 */
import type { SessionHistoryMessage, SessionHistoryToolCall } from "@/types/runtime";

import type { TrajectoryRecoveryPush } from "./recovery";
import { TRAJECTORY_ITEM_ID_KEY } from "./types";

/** 正文截断上限（超大历史消息不撑爆轨迹内存；聊天视图仍显示完整内容）。 */
export const HISTORY_TEXT_LIMIT = 20000;
export const HISTORY_REASONING_LIMIT = 8000;
export const HISTORY_TOOL_ARGS_LIMIT = 600;
export const HISTORY_TOOL_RESULT_LIMIT = 2000;

/** 历史兜底行的 item id 前缀（与 SSE 帧的固定 id 语义区分开）。 */
export const HISTORY_ITEM_PREFIX = "history:";

function readTrimmedString(value: unknown): string {
  return typeof value === "string" ? value.trim() : "";
}

function truncate(text: string, limit: number): string {
  if (text.length <= limit) {
    return text;
  }
  return `${text.slice(0, limit)}…`;
}

/** 消息所属 turn（后端 `metadata.turn_id`；缺失返回空串）。 */
export function historyMessageTurnId(message: SessionHistoryMessage): string {
  return readTrimmedString(message.metadata?.["turn_id"]);
}

/**
 * 历史兜底行的稳定 item id。
 *
 * 优先用后端下发的 `metadata.message_id`（重启后不变）；旧数据缺失身份时退化
 * 为「本页下标」——同一份历史投影结果仍然稳定，不会随轮询漂移。
 */
export function historyMessageItemId(
  message: SessionHistoryMessage,
  index: number,
  suffix: string,
): string {
  const messageId = readTrimmedString(message.metadata?.["message_id"]);
  return `${HISTORY_ITEM_PREFIX}${messageId || `msg-${index}`}:${suffix}`;
}

/** 消息正文：content 优先，缺失时拼接 text 分片（多模态消息）。 */
export function historyMessageText(message: SessionHistoryMessage): string {
  const content = typeof message.content === "string" ? message.content : "";
  if (content.trim()) {
    return content;
  }
  const parts = Array.isArray(message.content_parts) ? message.content_parts : [];
  return parts
    .filter((part) => part && (part.type === "text" || part.type === undefined))
    .map((part) => (typeof part.text === "string" ? part.text : ""))
    .join("")
    .trim();
}

/** 助手推理内容（`metadata.reasoning_content`，兼容 `reasoning_details` 数组）。 */
function historyReasoningText(message: SessionHistoryMessage): string {
  const metadata = message.metadata ?? {};
  const direct = readTrimmedString(metadata["reasoning_content"]);
  if (direct) {
    return direct;
  }
  const details = metadata["reasoning_details"];
  if (Array.isArray(details)) {
    return details
      .map((entry) => {
        if (typeof entry === "string") {
          return entry;
        }
        if (entry && typeof entry === "object") {
          const record = entry as Record<string, unknown>;
          return (
            readTrimmedString(record["text"]) ||
            readTrimmedString(record["content"]) ||
            readTrimmedString(record["summary"])
          );
        }
        return "";
      })
      .join("");
  }
  return "";
}

/** 工具调用入参摘要（后端落 `arguments` 映射或 `input` 原始 JSON 串）。 */
function historyToolArgsSummary(call: SessionHistoryToolCall): string {
  const raw = readTrimmedString(call?.input);
  if (raw) {
    return truncate(raw, HISTORY_TOOL_ARGS_LIMIT);
  }
  const args = call?.arguments;
  if (args && typeof args === "object") {
    try {
      return truncate(JSON.stringify(args), HISTORY_TOOL_ARGS_LIMIT);
    } catch {
      return "";
    }
  }
  return "";
}

function historyToolCallPushes(
  calls: SessionHistoryToolCall[],
  index: number,
): TrajectoryRecoveryPush[] {
  const pushes: TrajectoryRecoveryPush[] = [];
  calls.forEach((call, callIndex) => {
    // 工具 item 身份与 live 帧一致（`tool:<tool_call_id>`）；缺 id 的历史行
    // 由下标兜底，保证仍是一条调用一行。
    const id = readTrimmedString(call?.id) || `history-tool-${index}-${callIndex}`;
    const name = readTrimmedString(call?.name) || "tool";
    const argsSummary = historyToolArgsSummary(call);
    pushes.push({
      kind: "tool_start",
      payload: {
        tool: {
          id,
          name,
          ...(argsSummary ? { args_summary: argsSummary } : {}),
        },
      },
    });
  });
  return pushes;
}

function historyToolResultPush(
  message: SessionHistoryMessage,
): TrajectoryRecoveryPush | null {
  const metadata = message.metadata ?? {};
  const id =
    readTrimmedString(message.tool_call_id) ||
    readTrimmedString(metadata["tool_call_id"]);
  if (!id) {
    return null;
  }
  const name = readTrimmedString(metadata["tool_name"]) || "tool";
  const content = historyMessageText(message);
  const tool: Record<string, unknown> = { id, name };
  if (metadata["ok"] === false) {
    tool["error"] = truncate(content, HISTORY_TOOL_RESULT_LIMIT) || "tool failed";
  } else if (content) {
    tool["output"] = truncate(content, HISTORY_TOOL_RESULT_LIMIT);
  }
  return { kind: "tool_end", payload: { tool } };
}

/**
 * 单条历史消息 → 轨迹降级帧（顺序：推理 → 正文 → 工具调用）。
 *
 * 空消息（如仅含工具调用的助手消息正文为空）不产生行，避免轨迹里出现空块。
 */
export function sessionHistoryMessageToTrajectoryPushes(
  message: SessionHistoryMessage,
  index = 0,
): TrajectoryRecoveryPush[] {
  const role = (message.role ?? "").trim().toLowerCase();

  if (role === "user") {
    const content = historyMessageText(message);
    if (!content) {
      return [];
    }
    return [
      {
        kind: "user",
        payload: {
          content: truncate(content, HISTORY_TEXT_LIMIT),
          [TRAJECTORY_ITEM_ID_KEY]: historyMessageItemId(message, index, "user"),
        },
      },
    ];
  }

  if (role === "assistant") {
    const pushes: TrajectoryRecoveryPush[] = [];
    const reasoning = historyReasoningText(message);
    if (reasoning) {
      pushes.push({
        kind: "reasoning",
        payload: {
          content: truncate(reasoning, HISTORY_REASONING_LIMIT),
          [TRAJECTORY_ITEM_ID_KEY]: historyMessageItemId(message, index, "reasoning"),
        },
      });
    }
    const content = historyMessageText(message);
    if (content) {
      pushes.push({
        kind: "chunk",
        payload: {
          type: "text",
          content: truncate(content, HISTORY_TEXT_LIMIT),
          [TRAJECTORY_ITEM_ID_KEY]: historyMessageItemId(message, index, "text"),
        },
      });
    }
    if (Array.isArray(message.tool_calls) && message.tool_calls.length > 0) {
      pushes.push(...historyToolCallPushes(message.tool_calls, index));
    }
    return pushes;
  }

  if (role === "tool") {
    const push = historyToolResultPush(message);
    return push ? [push] : [];
  }

  // system / developer / 未知角色：提示词脚手架，不进入轨迹消息流。
  return [];
}

/** 整段历史 → 有序降级帧序列（按历史顺序，seq = 0）。 */
export function sessionHistoryToTrajectoryPushes(
  history: SessionHistoryMessage[],
): TrajectoryRecoveryPush[] {
  const pushes: TrajectoryRecoveryPush[] = [];
  history.forEach((message, index) => {
    pushes.push(...sessionHistoryMessageToTrajectoryPushes(message, index));
  });
  return pushes;
}

/** 按 turn 分组的历史降级帧（同一 turn 的连续消息合并为一组，保持历史顺序）。 */
export type HistoryTrajectoryGroup = {
  /** 所属 turn（"" = 历史里没有 turn 归属的游离消息）。 */
  turnId: string;
  pushes: TrajectoryRecoveryPush[];
};

export function groupSessionHistoryTrajectoryPushes(
  history: SessionHistoryMessage[],
): HistoryTrajectoryGroup[] {
  const groups: HistoryTrajectoryGroup[] = [];
  let current: HistoryTrajectoryGroup | null = null;
  history.forEach((message, index) => {
    const pushes = sessionHistoryMessageToTrajectoryPushes(message, index);
    if (pushes.length === 0) {
      return;
    }
    const turnId = historyMessageTurnId(message);
    if (!current || current.turnId !== turnId) {
      current = { turnId, pushes: [] };
      groups.push(current);
    }
    current.pushes.push(...pushes);
  });
  return groups;
}
