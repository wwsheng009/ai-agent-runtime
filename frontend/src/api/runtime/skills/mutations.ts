// P0-2 拆分（原 skills.ts L347-L363、L458-L501、L525-L574）：写操作（热重载控制 + skill 执行）。

import type { RuntimeHotReloadStats } from "@/types/runtime";

import { buildRuntimeUrl, fetchRuntimeJson } from "../shared";

import { SKILLS_ADMIN_TOKEN_HEADER, SKILLS_HOT_RELOAD_PATH } from "./constants";
import { normalizeHotReloadStats } from "./normalize";
import type { SkillMutationRequestOptions } from "./types";

function withAdminToken(
  adminToken: string | undefined,
  extra: Record<string, string>,
): Record<string, string> {
  const headers: Record<string, string> = { ...extra };
  const token = adminToken?.trim();
  if (token) {
    headers[SKILLS_ADMIN_TOKEN_HEADER] = token;
  }
  return headers;
}

async function postHotReload(
  action: "start" | "stop" | "reload",
  body: Record<string, unknown> | null,
  options: SkillMutationRequestOptions,
): Promise<RuntimeHotReloadStats> {
  const payload = await fetchRuntimeJson<unknown>(
    buildRuntimeUrl(`${SKILLS_HOT_RELOAD_PATH}/${action}`),
    {
      method: "POST",
      headers: withAdminToken(options.adminToken, { "Content-Type": "application/json" }),
      ...(body ? { body: JSON.stringify(body) } : {}),
      ...(options.signal ? { signal: options.signal } : {}),
    },
  );
  return normalizeHotReloadStats(payload);
}

/** 启动热重载；`dirs` 为空直接抛错（后端也会 400）。 */
export async function startHotReload(
  dirs: string[],
  options: SkillMutationRequestOptions & { debounceMs?: number } = {},
): Promise<RuntimeHotReloadStats> {
  const cleaned = dirs.map((dir) => dir.trim()).filter((dir) => dir.length > 0);
  if (cleaned.length === 0) {
    throw new Error("hot reload requires at least one skill directory");
  }
  const body: Record<string, unknown> = { dirs: cleaned };
  if (typeof options.debounceMs === "number" && Number.isFinite(options.debounceMs)) {
    body.debounce_ms = Math.max(0, Math.floor(options.debounceMs));
  }
  return postHotReload("start", body, options);
}

export async function stopHotReload(
  options: SkillMutationRequestOptions = {},
): Promise<RuntimeHotReloadStats> {
  return postHotReload("stop", null, options);
}

export async function reloadHotReload(
  options: SkillMutationRequestOptions = {},
): Promise<RuntimeHotReloadStats> {
  return postHotReload("reload", null, options);
}

/**
 * 执行指定的 skill。
 *
 * POST /api/runtime/skills/{name}/execute
 *
 * @param name - skill 名称
 * @param params - 执行参数（可选）
 * @returns 执行结果
 */
export async function executeSkill(
  name: string,
  params?: {
    prompt?: string;
    sessionId?: string;
    params?: Record<string, unknown>;
    context?: Record<string, unknown>;
    options?: Record<string, unknown>;
  },
): Promise<unknown> {
  const url = buildRuntimeUrl(`/api/runtime/skills/${encodeURIComponent(name)}/execute`);
  // 线上契约是 snake_case（后端 `executeSkillRequest` 与 `pkg/skillsapi.ExecuteSkillRequest`
  // 同一口径）：调用方用 camelCase，这里显式映射。直接透传 `sessionId` 会被 Go json
  // 当作未知字段静默丢弃，导致 skill 执行落进新建会话而不是当前会话。
  const body: Record<string, unknown> = {};
  if (params) {
    if (params.prompt !== undefined) {
      body.prompt = params.prompt;
    }
    if (params.sessionId !== undefined) {
      body.session_id = params.sessionId;
    }
    if (params.params !== undefined) {
      body.params = params.params;
    }
    if (params.context !== undefined) {
      body.context = params.context;
    }
    if (params.options !== undefined) {
      body.options = params.options;
    }
  }
  const response = await fetchRuntimeJson<unknown>(url, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
    },
    body: JSON.stringify(body),
  });
  return response;
}
