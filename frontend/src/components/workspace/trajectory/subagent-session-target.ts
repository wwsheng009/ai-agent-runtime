/**
 * 子会话下钻目标解析（P1-5 方案 1）。
 *
 * 与对话框组件分文件：组件文件只导出组件，保持 Fast Refresh 生效。
 */
import type { TrajectoryItem } from "@/lib/trajectory/types";

/** 下钻目标：子会话 ID + 标题栏展示用的来源信息。 */
export type SubagentSessionTarget = {
  sessionId: string;
  agentId?: string;
  role?: string;
  /** 父流终态镜像里的状态（running/completed/failed…），仅作标题栏兜底。 */
  status?: string;
};

function readText(value: unknown): string | undefined {
  if (typeof value !== "string") {
    return undefined;
  }
  const trimmed = value.trim();
  return trimmed === "" ? undefined : trimmed;
}

/**
 * 从父轨迹的 `subagent` item 解析下钻目标。
 *
 * payload 契约（两处来源同一 session 语义）：
 * - chat SSE `subagent`：`{ role, session_id, ... }`（`handler.go` buildSubagentEventPayloads）；
 * - 终态镜像 `subagent.completed`：`{ agent_id, session_id, ... }`（childSessionID）。
 * 缺少 session 标识（例如仅有 role 的占位事件）时返回 null，UI 不显示入口。
 */
export function subagentSessionTarget(
  item: TrajectoryItem | null,
): SubagentSessionTarget | null {
  if (!item || item.kind !== "subagent" || item.head.kind !== "structured") {
    return null;
  }
  const payload = item.head.payload ?? {};
  const sessionId =
    readText(payload.session_id) ??
    readText(payload.sessionId) ??
    readText(payload.agent_id) ??
    readText(payload.agentId);
  if (!sessionId) {
    return null;
  }
  return {
    sessionId,
    agentId: readText(payload.agent_id) ?? readText(payload.agentId) ?? readText(payload.id),
    role: readText(payload.role) ?? readText(payload.agent_type),
    status: readText(payload.status),
  };
}
