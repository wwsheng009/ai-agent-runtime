// 查询类工具（grep / glob / search）的入参摘要是「多 pattern」语义：真实入参里
// `patterns` 是字符串数组，后端 `summarizeToolCallArgs` 把列表逐值渲染进 `arg_preview`
// （`patterns=["a","b"]`，200 字预算内还可能被截断）。只按单字符串读取会得到两种坏结果：
// 实时帧摘要显示 `["a","b"]` 这样的 JSON 标点，结构化入参则直接没有摘要。
// 这里统一还原成单行文本，OR 语义用 " | " 连接。

/** grep 的多 pattern 是 OR 语义；列表值统一按 " | " 连接成单行。 */
export const LIST_TEXT_SEPARATOR = " | ";

export const QUERY_ARG_KEYS = ["query", "pattern", "patterns", "regexp", "q", "search"];

const QUOTED_PREVIEW_ITEM_PATTERN = /"((?:[^"\\]|\\.)*)"/g;

function singleLineText(text: string) {
  return text.replace(/\s+/g, " ").trim();
}

/** 单个入参值 → 单行文本：字符串原样，字符串数组按分隔符连接。 */
function textFromArgValue(value: unknown) {
  if (typeof value === "string") {
    return singleLineText(value);
  }
  if (Array.isArray(value)) {
    return value
      .map((item) => (typeof item === "string" ? singleLineText(item) : ""))
      .filter(Boolean)
      .join(LIST_TEXT_SEPARATOR);
  }
  return "";
}

/** 结构化入参（回合末证据尾巴 / 历史消息）按别名顺序取第一个非空值。 */
export function queryTextFromArgs(args: Record<string, unknown> | undefined, keys: string[]) {
  if (!args) {
    return "";
  }
  for (const key of keys) {
    const text = textFromArgValue(args[key]);
    if (text) {
      return text;
    }
  }
  return "";
}

function tryParseStringList(text: string) {
  if (!text.startsWith("[")) {
    return undefined;
  }
  try {
    const parsed: unknown = JSON.parse(text);
    if (!Array.isArray(parsed)) {
      return undefined;
    }
    const items = parsed
      .filter((item): item is string => typeof item === "string")
      .map(singleLineText)
      .filter(Boolean);
    return items.length > 0 ? items : undefined;
  } catch {
    return undefined;
  }
}

/**
 * 预览里的列表值是 JSON 数组文本（后端逐值渲染，200 字预算内可能被截断）：
 * 完整数组直接解析，截断的数组只取完整引号项——绝不把 JSON 标点当查询展示。
 */
export function queryTextFromPreviewRaw(raw: string) {
  const text = raw.trim();
  if (!text) {
    return "";
  }
  const parsed = tryParseStringList(text);
  if (parsed) {
    return parsed.join(LIST_TEXT_SEPARATOR);
  }
  const quoted = [...text.matchAll(QUOTED_PREVIEW_ITEM_PATTERN)]
    .map((match) => singleLineText(match[1] ?? ""))
    .filter(Boolean);
  return quoted.length > 0 ? quoted.join(LIST_TEXT_SEPARATOR) : singleLineText(text);
}
