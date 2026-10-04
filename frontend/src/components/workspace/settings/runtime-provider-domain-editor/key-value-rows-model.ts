/**
 * Headers / 模型映射行编辑器的纯数据转换（与组件分离，便于单测与 Fast Refresh）。
 */

export type KeyValueEntry = { key: string; value: string };

/** JSON 对象 → 行数组；非对象/解析失败返回空（保留原 JSON 草稿不动）。 */
export function parseKeyValueEntries(json: string): KeyValueEntry[] {
  try {
    const parsed: unknown = JSON.parse(json);
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
      return [];
    }
    return Object.entries(parsed as Record<string, unknown>).map(
      ([key, value]) => ({
        key,
        value: typeof value === "string" ? value : String(value ?? ""),
      }),
    );
  } catch {
    return [];
  }
}

/** 行数组 → JSON 对象字符串；空键行（编辑态）不写入。 */
export function serializeKeyValueEntries(entries: KeyValueEntry[]): string {
  const record: Record<string, string> = {};
  for (const entry of entries) {
    const key = entry.key.trim();
    if (!key) {
      continue;
    }
    record[key] = entry.value;
  }
  return JSON.stringify(record, null, 2);
}
