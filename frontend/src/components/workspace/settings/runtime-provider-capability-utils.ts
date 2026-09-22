/**
 * Provider 扩展字段与草稿 `extraJson` 的合并语义：
 *   * `model_capabilities`：fetch-models 的元数据重匹配结果 / auto-import 的全量能力声明；
 *   * `max_tokens_limit` 等后端返回、但编辑表单没有建模的标量字段。
 *
 * 这些字段不在 KNOWN_PROVIDER_KEYS 里，保存时由 `extraJson` 原样透传回 provider
 * 节点，所以「重新拉取模型列表 / 自动导入后能力声明会不会刷新」只取决于这里：
 * micro web client 用同一份 metadata 重建 reasoning 编辑行，frontend 没有能力
 * 编辑器，若不在此处合并，后端刚匹配出来的能力声明就会在保存时丢掉。
 *
 * 合并语义统一为「非空覆盖」：
 *   * 只对本次响应涉及的模型生效，其余模型的能力声明原样保留；
 *   * 只写本次响应真正提供的（非空）字段，空值不覆盖已保存值，绝不删除 / 清空；
 *   * 草稿 extraJson 不是合法 JSON 对象时不改动，避免把坏值洗成看似合法的内容；
 *   * 无实际变化时返回 undefined，让调用方跳过这次草稿写入。
 */

import { isConfigRecord } from "./runtime-provider-config-utils";

/** 单个模型的能力声明（`model_capabilities.<model>` 的原样 JSON 对象）。 */
export type ProviderCapabilityFields = Record<string, unknown>;

/**
 * 能力字段的「空值」判据：空串 / 0 / false / 空数组 / 空对象 / null 都算
 * 「本次响应没有提供该字段」。
 *
 * 后端 auto-import 返回的是 `agentconfig.ModelCapabilitySpec` 全量结构（字段
 * 没有 omitempty），未匹配字段会以 Go 零值出现（`input_modalities: null`、
 * `native_tools: {image_generation: false, ...}`、`max_tokens: 0`）；fetch-models
 * 的 metadata 同样只在命中时给字段。把零值当权威写回会抹平手写的能力声明，
 * 正是本次修复要避免的「保存丢失 model_capabilities」。
 */
function isBlankCapabilityValue(value: unknown): boolean {
  if (value === null || value === undefined) {
    return true;
  }
  if (typeof value === "string") {
    return value.trim() === "";
  }
  if (typeof value === "number") {
    return !Number.isFinite(value) || value === 0;
  }
  if (typeof value === "boolean") {
    return !value;
  }
  if (Array.isArray(value)) {
    return value.length === 0;
  }
  if (isConfigRecord(value)) {
    return Object.keys(value).length === 0;
  }
  return false;
}

/**
 * 递归合并单个能力字段：incoming 非空即覆盖；对象按子字段递归，
 * 避免抹掉本次响应没提到的兄弟字段（如 native_tools 里的另一个开关）。
 */
function mergeCapabilityValue(current: unknown, incoming: unknown): unknown {
  if (isBlankCapabilityValue(incoming)) {
    return current;
  }
  if (!isConfigRecord(incoming)) {
    return incoming;
  }
  const base = isConfigRecord(current) ? current : {};
  const next: Record<string, unknown> = { ...base };
  for (const [key, value] of Object.entries(incoming)) {
    const merged = mergeCapabilityValue(base[key], value);
    if (merged !== undefined) {
      next[key] = merged;
    }
  }
  if (Object.keys(next).length === 0 && !isConfigRecord(current)) {
    // 全为空值且没有既有对象：不新增空对象条目。
    return current;
  }
  return next;
}

/** 读取草稿扩展字段对象：空串按空对象处理（新建草稿的初值），非法 JSON 返回 null。 */
function parseExtraJsonRecord(extraJson: string): Record<string, unknown> | null {
  const raw = extraJson.trim();
  if (!raw) {
    return {};
  }
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return null;
  }
  return isConfigRecord(parsed) ? { ...parsed } : null;
}

/** 与解析后的当前值逐字比较：没有实际变化时返回 undefined，避免无谓的草稿写入。 */
function serializeExtraJsonChange(
  current: Record<string, unknown>,
  next: Record<string, unknown>,
): string | undefined {
  const nextText = JSON.stringify(next, null, 2);
  return nextText === JSON.stringify(current, null, 2) ? undefined : nextText;
}

/**
 * 把模型能力声明合并进 `extraJson.model_capabilities`。
 *
 * @param extraJson 当前草稿的扩展字段 JSON 文本。
 * @param incoming 本次响应涉及的模型 → 能力字段（fetch-models 的 metadata /
 *   auto-import 的 `model_capabilities`）。
 * @returns 合并后的 extraJson 文本；无变化（或无法安全合并）时返回 undefined。
 */
export function mergeModelCapabilitiesIntoExtraJson(
  extraJson: string,
  incoming: Record<string, ProviderCapabilityFields> | undefined | null,
): string | undefined {
  const entries = Object.entries(incoming ?? {}).filter(
    ([model]) => model.trim() !== "",
  );
  if (entries.length === 0) {
    return undefined;
  }
  const current = parseExtraJsonRecord(extraJson);
  if (!current) {
    return undefined;
  }
  const existing = isConfigRecord(current.model_capabilities)
    ? current.model_capabilities
    : {};
  const next: Record<string, unknown> = { ...existing };
  for (const [rawModel, fields] of entries) {
    const model = rawModel.trim();
    const merged = mergeCapabilityValue(existing[model], fields);
    if (merged !== undefined) {
      next[model] = merged;
    }
  }
  return serializeExtraJsonChange(current, { ...current, model_capabilities: next });
}

/** 覆盖式写入 `extraJson` 的单个顶层字段；无变化（或无法安全合并）时返回 undefined。 */
export function mergeExtraJsonField(
  extraJson: string,
  key: string,
  value: unknown,
): string | undefined {
  const current = parseExtraJsonRecord(extraJson);
  if (!current) {
    return undefined;
  }
  return serializeExtraJsonChange(current, { ...current, [key]: value });
}
