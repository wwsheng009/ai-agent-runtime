/**
 * P1-1：Turn token 用量归一化。
 *
 * 运行时可能以 provider 原样键（`prompt_tokens` / `completion_tokens` /
 * `total_tokens`）或 runtime 前缀键（`usage_prompt_tokens` 等）上报用量。
 * 这里统一归一化为 `TurnUsage`；**用量不完整（缺输入或输出任一）时返回
 * null**，渲染层据此把整行隐藏，而不是显示半截数字。
 */

export type TurnUsage = {
  promptTokens: number;
  completionTokens: number;
  totalTokens: number;
};

const PROMPT_KEYS = [
  "prompt_tokens",
  "usage_prompt_tokens",
  "promptTokens",
] as const;
const COMPLETION_KEYS = [
  "completion_tokens",
  "usage_completion_tokens",
  "completionTokens",
] as const;
const TOTAL_KEYS = ["total_tokens", "usage_total_tokens", "totalTokens"] as const;

function readCount(
  source: Record<string, unknown>,
  keys: readonly string[],
): number | null {
  for (const key of keys) {
    const value = source[key];
    if (typeof value === "number" && Number.isFinite(value) && value >= 0) {
      return value;
    }
    if (typeof value === "string" && value.trim()) {
      const parsed = Number(value);
      if (Number.isFinite(parsed) && parsed >= 0) {
        return parsed;
      }
    }
  }
  return null;
}

/** 完整用量判定：渲染层只在通过时显示用量行。 */
export function isCompleteTurnUsage(value: unknown): value is TurnUsage {
  if (!value || typeof value !== "object") {
    return false;
  }
  const candidate = value as Partial<TurnUsage>;
  const { promptTokens, completionTokens, totalTokens } = candidate;
  return (
    typeof promptTokens === "number" &&
    Number.isFinite(promptTokens) &&
    promptTokens >= 0 &&
    typeof completionTokens === "number" &&
    Number.isFinite(completionTokens) &&
    completionTokens >= 0 &&
    typeof totalTokens === "number" &&
    Number.isFinite(totalTokens) &&
    totalTokens >= 0 &&
    promptTokens + completionTokens > 0
  );
}

/**
 * 从运行结果对象（`AgentChatResult`）读取归一化用量。
 * 输入/输出任一缺失即视为不完整 → null。
 */
export function readTurnUsage(
  source: { usage?: unknown } | null | undefined,
): TurnUsage | null {
  const raw = source?.usage;
  if (!raw || typeof raw !== "object") {
    return null;
  }
  const record = raw as Record<string, unknown>;
  const promptTokens = readCount(record, PROMPT_KEYS);
  const completionTokens = readCount(record, COMPLETION_KEYS);
  if (promptTokens === null || completionTokens === null) {
    return null;
  }
  const explicitTotal = readCount(record, TOTAL_KEYS);
  const usage: TurnUsage = {
    promptTokens,
    completionTokens,
    totalTokens:
      explicitTotal && explicitTotal > 0
        ? explicitTotal
        : promptTokens + completionTokens,
  };
  return isCompleteTurnUsage(usage) ? usage : null;
}
