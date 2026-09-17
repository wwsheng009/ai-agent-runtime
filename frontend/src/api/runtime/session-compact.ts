import { buildRuntimeUrl, fetchRuntimeJson } from "./shared";

/**
 * 会话上下文压缩（手动 compact）的服务端一侧。
 *
 * 为什么需要它：自动压缩只在 token 触发阈值命中时发生（后端 compactruntime），
 * 但用户常常想「将满未满」时就主动收缩历史（省 token、避免后续回合被窗口截断）。
 * 后端 `POST /sessions/{id}/runtime/commands` 的 `compact` 命令复用同一条压缩管线
 * （Force=true，按 mode 选 local/remote），这里只负责投递命令并把响应归一化成
 * 面板可消费的稳定形状。
 *
 * 归一化动机与其它 runtime 客户端一致：代理或旧后端可能返回 `200 {}`，
 * 面板不该因为缺字段而崩；缺什么就如实给空值/0，不伪造 token 数。
 */

/** 压缩模式：auto 交运行时按能力解析，local/remote 显式指定压缩执行方。 */
export type SessionCompactMode = "auto" | "local" | "remote";

export const SESSION_COMPACT_MODES: readonly SessionCompactMode[] = [
  "auto",
  "local",
  "remote",
];

export type SessionCompactStatus = {
  mode: SessionCompactMode | "";
  phase: string;
  reason: string;
  provider: string;
  model: string;
  triggerTokenLimit: number;
  maxContextTokens: number;
  tokenBefore: number;
};

export type SessionCompactResult = SessionCompactStatus & {
  tokenAfter: number;
  compactedMessages: number;
  checkpointIds: string[];
  usageSource: string;
};

export type SessionCompactOutcome = {
  status: SessionCompactStatus;
  /**
   * null = 本次没有替换历史（未达阈值 / 无可用压缩器 / 会话空闲）。
   * 这不是失败：真实原因在 `status.reason`，面板应如实展示而不是报错。
   */
  result: SessionCompactResult | null;
};

export type CompactSessionOptions = {
  /** 缺省 = 交运行时按会话能力解析（等价于 auto）。 */
  mode?: SessionCompactMode;
  signal?: AbortSignal;
};

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function pickText(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function pickNumber(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) ? value : 0;
}

function pickStringArray(value: unknown): string[] {
  if (!Array.isArray(value)) {
    return [];
  }

  return value.filter((item): item is string => typeof item === "string");
}

/** 只认契约内的三种 mode；未知/缺失一律回落到空串（= 交运行时解析）。 */
export function resolveSessionCompactMode(value: unknown): SessionCompactMode | "" {
  if (typeof value !== "string") {
    return "";
  }

  const normalized = value.trim().toLowerCase();
  return SESSION_COMPACT_MODES.includes(normalized as SessionCompactMode)
    ? (normalized as SessionCompactMode)
    : "";
}

export function normalizeSessionCompactStatus(raw: unknown): SessionCompactStatus {
  const source = isRecord(raw) ? raw : {};
  return {
    mode: resolveSessionCompactMode(source.mode),
    phase: pickText(source.phase),
    reason: pickText(source.reason),
    provider: pickText(source.provider),
    model: pickText(source.model),
    triggerTokenLimit: pickNumber(source.trigger_token_limit),
    maxContextTokens: pickNumber(source.max_context_tokens),
    tokenBefore: pickNumber(source.token_before),
  };
}

export function normalizeSessionCompactResult(raw: unknown): SessionCompactResult | null {
  if (!isRecord(raw)) {
    return null;
  }

  return {
    ...normalizeSessionCompactStatus(raw),
    tokenAfter: pickNumber(raw.token_after),
    compactedMessages: pickNumber(raw.compacted_messages),
    checkpointIds: pickStringArray(raw.checkpoint_ids),
    usageSource: pickText(raw.usage_source),
  };
}

export function normalizeSessionCompactOutcome(raw: unknown): SessionCompactOutcome {
  const source = isRecord(raw) ? raw : {};
  return {
    status: normalizeSessionCompactStatus(source.status),
    result: normalizeSessionCompactResult(source.result),
  };
}

/**
 * 手动压缩当前会话的上下文历史。
 *
 * 非 2xx（会话不存在 / 会话忙 / 压缩失败）按 `fetchRuntimeJson` 约定抛错；
 * 返回 2xx 但 `result === null` 表示「本次未触发压缩」，由调用方按 skipped 展示。
 */
export async function compactSessionContext(
  sessionId: string,
  options: CompactSessionOptions = {},
): Promise<SessionCompactOutcome> {
  const mode = resolveSessionCompactMode(options.mode);
  const raw = await fetchRuntimeJson<unknown>(
    buildRuntimeUrl(
      `/api/runtime/sessions/${encodeURIComponent(sessionId)}/runtime/commands`,
    ),
    {
      method: "POST",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        type: "compact",
        ...(mode ? { mode } : {}),
      }),
      ...(options.signal ? { signal: options.signal } : {}),
    },
  );

  return normalizeSessionCompactOutcome(raw);
}
