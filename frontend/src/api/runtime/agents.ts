// P2-1A：子代理控制面客户端（AgentControl 身份图）。
//
// 后端契约：
//   * `GET  /api/runtime/agent-control/agents?root_session_id&session_id&path_prefix&include_closed&limit`
//       → `{agents:[AgentRecord], count, source, filters:{...}}`
//       （backend/internal/api/skills/agent_control_agent_handlers.go:23-69）
//   * `POST /api/runtime/sessions/{id}/agents/{agent_id}/close`  → `{agent:{...}}`
//   * `POST /api/runtime/sessions/{id}/agents/{agent_id}/resume` → `{agent:{...}}`
//       （backend/internal/api/skills/session_runtime_handlers.go:333-373；
//         未注入 agent session controller → 503，`agent_id` 为空 → 400，
//         控制器失败 → 500）
//
// 归一化纪律：
//   * `agents` 非数组 → 抛错（**不把结构异常伪装成「无子代理」**）；
//   * 单条记录缺 `agent_id` → 只丢该条（不整页失败）；
//   * `count` 保留后端上报值（可能大于数组长度：后端按 limit 截断），
//     仅当缺失 / 非法时才回退数组长度；
//   * 状态空值 / 未知值 → `unknown`（**不按 active 渲染**）；
//   * limit 由前端收口（默认 200 / 上限 500）：后端 `parseOptionalLimit`
//     （team_handlers.go:4646-4655）不设上限且 `0` 表示不限，超大 limit 会把
//     全量身份图一次拉回，故由前端兜底。
//
// 动作面纪律：本模块只提供「看 + 停 + 恢复」（list / close / resume）。
// spawn / wait / events / input 仍由 agent 侧工具调用完成，UI 不代发。

import { RuntimeApiError, buildRuntimeUrl, fetchRuntimeJson } from "./shared";

import type {
  RuntimeAgentCatalog,
  RuntimeAgentRecord,
  RuntimeAgentRuntimeState,
  RuntimeAgentStatus,
} from "@/types/runtime";

export const AGENT_CONTROL_AGENTS_PATH = "/api/runtime/agent-control/agents";

/** 前端收口的默认 / 上限 limit（后端不限，见文件头注释）。 */
export const DEFAULT_AGENT_LIMIT = 200;
export const AGENT_LIMIT_MAX = 500;

type RawRecord = Record<string, unknown>;

function asRecord(value: unknown): RawRecord | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as RawRecord)
    : null;
}

function readTrimmed(value: unknown): string | null {
  if (typeof value !== "string") {
    return null;
  }
  const trimmed = value.trim();
  return trimmed === "" ? null : trimmed;
}

function readTimestamp(value: unknown): string | null {
  return readTrimmed(value);
}

function readDepth(value: unknown): number | null {
  if (typeof value !== "number" || !Number.isFinite(value) || value < 0) {
    return null;
  }
  return Math.floor(value);
}

function readWarnings(value: unknown): string[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value
    .map((entry) => readTrimmed(entry))
    .filter((entry): entry is string => entry !== null);
}

/** 状态收口：只承认后端定义的 active / stale / closed，其余一律 `unknown`。 */
export function normalizeRuntimeAgentStatus(value: unknown): RuntimeAgentStatus {
  const status = readTrimmed(value)?.toLowerCase();
  if (status === "active" || status === "stale" || status === "closed") {
    return status;
  }
  return "unknown";
}

/**
 * 运行态收口：只承认 running / idle / stopped，其余（缺字段 / 未知取值）一律
 * `unknown`。**未知不得被当作「已结束」**，展示层据此回退到身份状态。
 */
export function normalizeRuntimeAgentRuntimeState(value: unknown): RuntimeAgentRuntimeState {
  const state = readTrimmed(value)?.toLowerCase();
  if (state === "running" || state === "idle" || state === "stopped") {
    return state;
  }
  return "unknown";
}

/** 单条身份归一化；缺 `agent_id` 返回 null（调用方丢弃该条）。 */
export function normalizeRuntimeAgent(raw: unknown): RuntimeAgentRecord | null {
  const record = asRecord(raw);
  if (!record) {
    return null;
  }
  const agentId = readTrimmed(record.agent_id);
  if (!agentId) {
    return null;
  }
  return {
    agentId,
    rootSessionId: readTrimmed(record.root_session_id),
    parentAgentId: readTrimmed(record.parent_agent_id),
    parentSessionId: readTrimmed(record.parent_session_id),
    sessionId: readTrimmed(record.session_id),
    agentPath: readTrimmed(record.agent_path),
    depth: readDepth(record.depth),
    agentType: readTrimmed(record.agent_type),
    nickname: readTrimmed(record.nickname),
    workflow: readTrimmed(record.workflow),
    teamId: readTrimmed(record.team_id),
    teammateId: readTrimmed(record.teammate_id),
    provider: readTrimmed(record.effective_provider) ?? readTrimmed(record.provider),
    model: readTrimmed(record.effective_model) ?? readTrimmed(record.model),
    difficulty: readTrimmed(record.difficulty),
    status: normalizeRuntimeAgentStatus(record.status),
    runtimeState: normalizeRuntimeAgentRuntimeState(record.runtime_state),
    createdAt: readTimestamp(record.created_at),
    updatedAt: readTimestamp(record.updated_at),
    closedAt: readTimestamp(record.closed_at),
    routeWarnings: readWarnings(record.route_warnings),
  };
}

/** limit 收口：非有限值 / 非正数回退默认值，超过上限按下限截断。 */
export function resolveAgentLimit(limit: number | undefined): number {
  if (typeof limit !== "number" || !Number.isFinite(limit) || limit <= 0) {
    return DEFAULT_AGENT_LIMIT;
  }
  return Math.min(Math.floor(limit), AGENT_LIMIT_MAX);
}

export function normalizeAgentCatalog(
  payload: unknown,
  request: { limit: number; includeClosed: boolean },
): RuntimeAgentCatalog {
  const record = asRecord(payload);
  if (!record) {
    throw new Error("runtime agent catalog payload is not an object");
  }
  const rawAgents = record.agents;
  if (!Array.isArray(rawAgents)) {
    throw new Error("runtime agent catalog payload is missing `agents`");
  }

  const agents = rawAgents
    .map((entry) => normalizeRuntimeAgent(entry))
    .filter((entry): entry is RuntimeAgentRecord => entry !== null);

  const rawCount = record.count;
  const count =
    typeof rawCount === "number" && Number.isFinite(rawCount) && rawCount >= 0
      ? rawCount
      : agents.length;

  return {
    agents,
    count,
    source: readTrimmed(record.source),
    limit: request.limit,
    includeClosed: request.includeClosed,
  };
}

export function normalizeAgentMutation(payload: unknown): RuntimeAgentRecord {
  const agent = normalizeRuntimeAgent(asRecord(payload)?.agent);
  if (!agent) {
    throw new Error("runtime agent mutation payload is missing `agent`");
  }
  return agent;
}

export type ListRuntimeAgentsOptions = {
  rootSessionId?: string;
  sessionId?: string;
  pathPrefix?: string;
  includeClosed?: boolean;
  limit?: number;
  signal?: AbortSignal;
};

/** 列出 AgentControl 身份图；三类过滤可任意组合（后端逐项 AND）。 */
export async function listRuntimeAgents(
  options: ListRuntimeAgentsOptions = {},
): Promise<RuntimeAgentCatalog> {
  const limit = resolveAgentLimit(options.limit);
  const includeClosed = options.includeClosed === true;

  const params = new URLSearchParams();
  const rootSessionId = options.rootSessionId?.trim();
  const sessionId = options.sessionId?.trim();
  const pathPrefix = options.pathPrefix?.trim();
  if (rootSessionId) {
    params.set("root_session_id", rootSessionId);
  }
  if (sessionId) {
    params.set("session_id", sessionId);
  }
  if (pathPrefix) {
    params.set("path_prefix", pathPrefix);
  }
  if (includeClosed) {
    params.set("include_closed", "true");
  }
  params.set("limit", String(limit));

  const url = `${buildRuntimeUrl(AGENT_CONTROL_AGENTS_PATH)}?${params.toString()}`;
  const payload = await fetchRuntimeJson<unknown>(url, {
    ...(options.signal ? { signal: options.signal } : {}),
  });
  return normalizeAgentCatalog(payload, { limit, includeClosed });
}

export type AgentMutationOptions = {
  signal?: AbortSignal;
};

export function agentControlCommandPath(
  sessionId: string,
  agentId: string,
  action: "close" | "resume",
): string {
  return `/api/runtime/sessions/${encodeURIComponent(sessionId)}/agents/${encodeURIComponent(agentId)}/${action}`;
}

async function runAgentMutation(
  sessionId: string,
  agentId: string,
  action: "close" | "resume",
  options: AgentMutationOptions,
): Promise<RuntimeAgentRecord> {
  const parent = sessionId.trim();
  const agent = agentId.trim();
  if (!parent) {
    throw new Error("session id is required");
  }
  if (!agent) {
    throw new Error("agent id is required");
  }
  const payload = await fetchRuntimeJson<unknown>(
    buildRuntimeUrl(agentControlCommandPath(parent, agent, action)),
    {
      method: "POST",
      ...(options.signal ? { signal: options.signal } : {}),
    },
  );
  return normalizeAgentMutation(payload);
}

/** 独立停止一个子代理（`POST /sessions/{id}/agents/{agent_id}/close`）。 */
export function closeRuntimeAgent(
  sessionId: string,
  agentId: string,
  options: AgentMutationOptions = {},
): Promise<RuntimeAgentRecord> {
  return runAgentMutation(sessionId, agentId, "close", options);
}

/** 恢复一个已关闭的子代理（`POST /sessions/{id}/agents/{agent_id}/resume`）。 */
export function resumeRuntimeAgent(
  sessionId: string,
  agentId: string,
  options: AgentMutationOptions = {},
): Promise<RuntimeAgentRecord> {
  return runAgentMutation(sessionId, agentId, "resume", options);
}

/**
 * 控制面不可用的降级判据：路由缺失（404/405）、未实现（501）、
 * 未注入 agent session controller（503）。其余错误（含 500）按真实失败呈现。
 */
export function isAgentControlUnavailable(error: unknown): boolean {
  if (!(error instanceof RuntimeApiError)) {
    return false;
  }
  return (
    error.status === 404 ||
    error.status === 405 ||
    error.status === 501 ||
    error.status === 503
  );
}
