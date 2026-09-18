// 键值行编辑器的纯逻辑模块（react-refresh/only-export-components：组件文件
// 只导出组件，行模型与粘贴解析放这里，可独立单测）。
//
// 约定：
// - id 只用于 React key，绝不进入请求体；
// - 粘贴解析优先使用调用方指定分隔符，找不到再回退另一个，因此 env（"="）
//   与 headers（":"）都能直接粘贴对方的常见写法。

export type KeyValueRow = {
  id: string;
  key: string;
  value: string;
};

export type KeyValuePasteEntry = {
  key: string;
  value: string;
};

let keyValueRowSeq = 0;

/** 生成稳定且全局唯一的行 id（仅用于 React key）。 */
export function createKeyValueRow(key = "", value = ""): KeyValueRow {
  keyValueRowSeq += 1;
  return { id: `kv-row-${keyValueRowSeq}`, key, value };
}

function findSeparatorIndex(line: string, separator: "=" | ":"): number {
  const preferred = line.indexOf(separator);
  if (preferred >= 0) {
    return preferred;
  }
  return line.indexOf(separator === "=" ? ":" : "=");
}

/** 把多行粘贴文本解析为键值行；无分隔符的行整体作为键，空行忽略。 */
export function parseKeyValuePaste(
  text: string,
  separator: "=" | ":",
): KeyValuePasteEntry[] {
  const entries: KeyValuePasteEntry[] = [];
  for (const rawLine of text.split(/\r?\n/)) {
    const line = rawLine.trim();
    if (!line) {
      continue;
    }
    const index = findSeparatorIndex(line, separator);
    if (index < 0) {
      entries.push({ key: line, value: "" });
      continue;
    }
    entries.push({
      key: line.slice(0, index).trim(),
      value: line.slice(index + 1).trim(),
    });
  }
  return entries;
}

/**
 * 多行粘贴落盘：首行替换目标行（保留其 id），其余行追加为新行。
 * 单行文本返回 null（保持浏览器默认粘贴行为）。
 */
export function applyKeyValuePaste(
  rows: KeyValueRow[],
  targetId: string,
  text: string,
  separator: "=" | ":",
): KeyValueRow[] | null {
  if (!/[\r\n]/.test(text)) {
    return null;
  }
  const entries = parseKeyValuePaste(text, separator);
  const index = rows.findIndex((row) => row.id === targetId);
  const current = rows[index];
  const first = entries[0];
  if (!first || !current) {
    return null;
  }
  const next = [...rows];
  next.splice(
    index,
    1,
    { ...current, key: first.key, value: first.value },
    ...entries.slice(1).map((entry) => createKeyValueRow(entry.key, entry.value)),
  );
  return next;
}
