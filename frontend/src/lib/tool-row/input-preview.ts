// 工具行「输入」面板的展示统一。
//
// 同一次调用在两条链路上拿到的入参文本形态不同：
// - 历史回放（/history 的 `arguments`）：原始 JSON，例如 `{"path":"frontend/e2e","depth":2}`；
// - 实时帧（后端 `arg_preview`）：键值文本，例如 `path=frontend/e2e depth=2`。
//
// 若原样呈现，展开面板会出现「回放一坨 JSON、实时一行键值」的链路差异——折叠行已经
// 统一过（见 state.ts 的 orderSummaryParts），面板这层同样要对齐：JSON 形态归一成
// `key=value`，键按字典序输出（形态本身即两端唯一稳定口径）。
//
// 只做「信息不丢失」的归一：含嵌套结构、解析失败、或归一后仍过长时原样返回原始文本，
// 宁可保留 JSON 也不静默丢字段。

import { LIST_TEXT_SEPARATOR } from "./query-text";

/** 归一后仍超过该长度就退回原文：面板是只读展示，不做二次截断。 */
const MAX_INPUT_PREVIEW_LENGTH = 400;

function singleLineText(text: string) {
  return text.replace(/\s+/g, " ").trim();
}

/** 标量 → 单行文本；null 显式渲染成空串（`key=`），嵌套结构返回 null 表示不可归一。 */
function scalarText(value: unknown): string | null {
  if (typeof value === "string") {
    return singleLineText(value);
  }
  if (typeof value === "number" || typeof value === "boolean") {
    return String(value);
  }
  if (value === null) {
    return "";
  }
  return null;
}

/** 值 → 单行文本：标量原样，标量数组按折叠行同一分隔符连接。 */
function valueText(value: unknown): string | null {
  if (Array.isArray(value)) {
    const parts = value.map(scalarText);
    if (parts.some((part) => part === null)) {
      return null;
    }
    return parts.join(LIST_TEXT_SEPARATOR);
  }
  return scalarText(value);
}

/**
 * 入参文本 → 展开面板展示文本：
 * - JSON 对象（且全部值可归一）→ `key=value` 按字典序连接；
 * - 其余（已是键值预览、非对象 JSON、解析失败、过长）→ 原样返回。
 */
export function formatToolInputPreview(raw: string): string {
  const text = raw.trim();
  if (!text.startsWith("{")) {
    // 已是预览文本（或空串）：trim 后原样返回，与面板此前的 `argsSummary.trim()` 口径一致。
    return text;
  }

  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch {
    return text;
  }
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
    return text;
  }

  const record = parsed as Record<string, unknown>;
  const pairs: string[] = [];
  for (const key of Object.keys(record).sort()) {
    const rendered = valueText(record[key]);
    if (rendered === null) {
      return text;
    }
    pairs.push(`${key}=${rendered}`);
  }
  if (pairs.length === 0) {
    return text;
  }

  const display = pairs.join(" ");
  return display.length > MAX_INPUT_PREVIEW_LENGTH ? text : display;
}
