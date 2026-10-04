/**
 * Provider 扩展字段与草稿 `extraJson` 的合并语义。
 *
 * 背景：`model_capabilities` 现在由第一等的模型能力草稿承载
 * （见 `runtime-provider-domain-editor/model-capability-draft.ts`），本模块只保留
 * 「后端返回、但编辑表单没有建模的标量字段」（如 `max_tokens_limit`）的透传。
 *
 * 合并语义统一为「覆盖式写入」：字段写入草稿 extraJson，保存时原样透传回
 * provider 节点；无实际变化时返回 undefined，让调用方跳过这次草稿写入。
 */

import { isConfigRecord } from "./runtime-provider-config-utils";

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
