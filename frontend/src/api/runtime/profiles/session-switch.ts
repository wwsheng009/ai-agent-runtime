// Batch 12：会话级 profile 切换的 REST 客户端（Web 路径）。
//
// 端点：`POST /api/runtime/sessions/{id}/runtime/commands`（`type=set_profile`）。
// 后端：`backend/internal/api/skills/session_runtime_handlers.go` 的 set_profile
// 分支 → `applySessionProfileSwitch`（`session_profile_switch.go`，与 CLI/TUI
// `/profile use` 同一执行核心）。风格对齐 `session-turn-control.ts`（interrupt）
// 与 `sessions.ts` 的 `resolveSessionToolApproval`（approve_tool）。
//
// 语义（如实回传，不做乐观改写）：
//   * 切换写会话身份（`sessionmeta.ProfileRef`）+ 删除 prompt 冻结锚点 +
//     驱逐空闲 actor ⇒ **下一轮生效**（V15/V19：server 侧没有别的权威路径）；
//   * 在途回合**不打断**（A3）：报告里的 `in_flight_turn` 为 true 时，下一轮
//     边界才按新 profile 重组；调用方应把这一点显式告诉用户；
//   * 校验失败（未知 profile / 空 ref）→ 400 + 错误信封，`RuntimeApiError`
//     保留后端 code；**绝不**把失败伪装成「已切换」。
//
// 归一化纪律：报告是切换的事实来源，`switch_report` / `changed` 形状不对即抛错
// （与 `./normalize` 同口径：契约回归不能被 UI 伪装成「切换成功」）；标量字段
// 缺失时按空值 / false 收敛，不影响已确认的切换事实。

import { buildRuntimeUrl, fetchRuntimeJson } from "../shared";
import { asRecord, readAliasedString, readBoolean, readStringArray } from "./normalize";

/** `changed` 投影（与 CLI `ProfileSwitchChanged` 逐字段同构，D23）。 */
export type SessionProfileSwitchChanged = {
  toolsAdded: string[];
  toolsRemoved: string[];
  skillsAdded: string[];
  skillsRemoved: string[];
  mcpAdded: string[];
  mcpRemoved: string[];
  promptChanged: boolean;
  providerChanged: boolean;
  modelChanged: boolean;
  permissionModeChanged: boolean;
};

/** 切换报告（后端 `sessionProfileSwitchReport`）。 */
export type SessionProfileSwitchReport = {
  /** 切换前的 profile 引用（基线会话为空串）。 */
  from: string;
  /** 切换后的 profile 引用（本次请求的目标）。 */
  to: string;
  changed: SessionProfileSwitchChanged;
  /** 生效时机；当前后端恒为 `next_turn`。 */
  effectiveAt: string;
  /** 缓存提示原文（后端给出，如「缓存前缀已失效」）。 */
  cacheNotice: string;
  /** 需显式告知用户的告警（provider/model/permission 差异等，D30）。 */
  warnings: string[];
  /** 切换发生时是否有在途回合（true = 本回合仍走旧面，下一轮生效）。 */
  inFlightTurn: boolean;
  /** prompt 冻结锚点是否已删除（下次 compose 重新组合）。 */
  anchorCleared: boolean;
  /** 稳定工具面是否已失效。 */
  toolSurfaceInvalidated: boolean;
  /** 失效作用域：`actor`（精确到该会话）/ `none`（无活体 actor）。 */
  toolSurfaceScope: string;
  /** 空闲 actor 是否已被驱逐（下一轮按新 sessionmeta 重建）。 */
  actorEvicted: boolean;
  /** 会话累计 token 计数是否已清零。 */
  contextTokenCountReset: boolean;
};

export type SessionProfileSwitchResponse = {
  ok: boolean;
  report: SessionProfileSwitchReport;
};

export type SetSessionProfileOptions = {
  /** 取消信号；中止投递时按 `fetchRuntimeJson` 约定抛错，调用方自行收敛。 */
  signal?: AbortSignal;
};

function normalizeChanged(raw: unknown): SessionProfileSwitchChanged {
  const record = asRecord(raw);
  if (!record) {
    throw new Error("invalid profile switch payload: changed must be an object");
  }
  return {
    toolsAdded: readStringArray(record.tools_added ?? record.toolsAdded),
    toolsRemoved: readStringArray(record.tools_removed ?? record.toolsRemoved),
    skillsAdded: readStringArray(record.skills_added ?? record.skillsAdded),
    skillsRemoved: readStringArray(record.skills_removed ?? record.skillsRemoved),
    mcpAdded: readStringArray(record.mcp_added ?? record.mcpAdded),
    mcpRemoved: readStringArray(record.mcp_removed ?? record.mcpRemoved),
    promptChanged: readBoolean(record.prompt_changed ?? record.promptChanged),
    providerChanged: readBoolean(record.provider_changed ?? record.providerChanged),
    modelChanged: readBoolean(record.model_changed ?? record.modelChanged),
    permissionModeChanged: readBoolean(
      record.permission_mode_changed ?? record.permissionModeChanged,
    ),
  };
}

/** 报告归一化；`switch_report` 缺失或形状不对时抛错（不伪造切换事实）。 */
export function normalizeSessionProfileSwitchReport(
  raw: unknown,
): SessionProfileSwitchReport {
  const record = asRecord(raw);
  if (!record) {
    throw new Error("invalid profile switch payload: switch_report must be an object");
  }
  return {
    from: readAliasedString(record, "from"),
    to: readAliasedString(record, "to"),
    changed: normalizeChanged(record.changed),
    effectiveAt: readAliasedString(record, "effectiveAt"),
    cacheNotice: readAliasedString(record, "cacheNotice"),
    warnings: readStringArray(record.warnings),
    inFlightTurn: readBoolean(record.in_flight_turn ?? record.inFlightTurn),
    anchorCleared: readBoolean(record.anchor_cleared ?? record.anchorCleared),
    toolSurfaceInvalidated: readBoolean(
      record.tool_surface_invalidated ?? record.toolSurfaceInvalidated,
    ),
    toolSurfaceScope: readAliasedString(record, "toolSurfaceScope"),
    actorEvicted: readBoolean(record.actor_evicted ?? record.actorEvicted),
    contextTokenCountReset: readBoolean(
      record.context_token_count_reset ?? record.contextTokenCountReset,
    ),
  };
}

/**
 * 切换指定会话的 profile（下一轮生效）。
 *
 * `profileRef` 必须是运行时目录里的真实引用（名称 / 路径）；空值在客户端
 * 即失败——后端对空 ref 返回 400，这里不把「空参数」发出去碰运气。
 */
export async function setSessionProfile(
  sessionId: string,
  profileRef: string,
  options: SetSessionProfileOptions = {},
): Promise<SessionProfileSwitchResponse> {
  const normalizedSessionId = sessionId.trim();
  if (normalizedSessionId.length === 0) {
    throw new Error("session id is required to switch profile");
  }
  const ref = profileRef.trim();
  if (ref.length === 0) {
    throw new Error("profile ref is required to switch profile");
  }
  const payload = await fetchRuntimeJson<Record<string, unknown>>(
    buildRuntimeUrl(
      `/api/runtime/sessions/${encodeURIComponent(normalizedSessionId)}/runtime/commands`,
    ),
    {
      method: "POST",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
      body: JSON.stringify({ type: "set_profile", profile: ref }),
      ...(options.signal ? { signal: options.signal } : {}),
    },
  );
  const record = asRecord(payload) ?? {};
  return {
    ok: record.ok !== false,
    report: normalizeSessionProfileSwitchReport(record.switch_report ?? record.switchReport),
  };
}
