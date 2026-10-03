// 由 pages/cache-analytics-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

export function formatCacheNumber(value: number) {
  if (!Number.isFinite(value)) return "0";
  return new Intl.NumberFormat().format(value);
}

/** 未上报（not_reported）时显示 "—" 而非 0，避免误读（§6.1 降级语义）。 */
export function formatCacheReportedNumber(value: number | undefined, reported: boolean | undefined) {
  if (!reported || value === undefined) return "—";
  return formatCacheNumber(value);
}

/**
 * 未缓存输入（输入总量中未命中缓存、真机处理的部分）。
 *
 * 新记录直接采用后端归一化值（uncached_input_tokens）；旧记录/旧库该字段缺失
 * 或为 0 时按可用字段降级推导：
 *   - 未上报缓存 → 输入全部视为未缓存；
 *   - cache_read > prompt 说明是不含式口径（Anthropic 的 input 不含 cache read），
 *     输入本身即未缓存；
 *   - 其余按包含式口径（OpenAI/DeepSeek/Gemini）做 prompt − cache_read。
 * 返回 undefined 表示无法判断（没有输入量），展示层应显示 "—"。
 */
export function cacheUncachedInputValue(
  usage: { prompt_tokens?: number; cache_read_tokens?: number; uncached_input_tokens?: number; cache_read_reported?: boolean } | undefined,
  cacheReportedOverride?: boolean,
): number | undefined {
  if (!usage) return undefined;
  const uncached = usage.uncached_input_tokens ?? 0;
  if (uncached > 0) return uncached;
  const prompt = usage.prompt_tokens ?? 0;
  if (prompt <= 0) return undefined;
  const reported = cacheReportedOverride ?? Boolean(usage.cache_read_reported);
  if (!reported) return prompt;
  const cached = usage.cache_read_tokens ?? 0;
  if (cached > prompt) return prompt;
  return Math.max(0, prompt - cached);
}

/** 未缓存输入的展示值；无法判断时返回 "—"。 */
export function formatCacheUncachedInput(
  usage: { prompt_tokens?: number; cache_read_tokens?: number; uncached_input_tokens?: number; cache_read_reported?: boolean } | undefined,
  cacheReportedOverride?: boolean,
) {
  const value = cacheUncachedInputValue(usage, cacheReportedOverride);
  return value === undefined ? "—" : formatCacheNumber(value);
}

export function formatCacheRatio(value: number | undefined) {
  if (value === undefined || value === null || !Number.isFinite(value)) return "—";
  return `${Math.round(value * 100)}%`;
}

export function formatCacheTime(value: string | undefined) {
  if (!value) return "-";
  const parsed = Date.parse(value);
  if (!Number.isFinite(parsed)) return value;
  return new Date(parsed).toLocaleString();
}

/**
 * 延迟展示（总耗时 / 首字时间）：0/缺省 = 未采集（非流式请求、历史记录或首个
 * 增量前就失败），返回 fallback 由调用方显示本地化「未采集」，不得回退成 0 ms。
 */
export function formatCacheLatency(value: number | undefined, fallback = "—") {
  if (value === undefined || value === null || !Number.isFinite(value) || value <= 0) return fallback;
  if (value < 1000) return `${value} ms`;
  if (value < 60_000) return `${(value / 1000).toFixed(1)} s`;
  return `${(value / 60_000).toFixed(1)} min`;
}
