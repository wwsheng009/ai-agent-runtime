// shell 类工具的命令文本解析：兼容 `command` 单命令与 `commands` 批量列表
// （toolargs 的 shell 参数表声明的形态：元素为字符串，或 `{command, workdir}` 对象）。
// 归一结果与后端 `shellCommandText` 一致，实时帧与历史帧才能得到同一份摘要。

const SHELL_COMMAND_KEYS = ["command", "cmd", "script", "shell_command"];
/** 批量命令是各自独立执行的，不能用 `&&` 冒充依赖关系。 */
const SHELL_COMMAND_SEPARATOR = " ; ";
/** 后端 `arg_preview` 逐值截断到 72 字，预览里的批量命令注定是有损的。 */
const PREVIEW_COMMAND_ITEM_PATTERN = /"(?:command|cmd|script|shell_command)"\s*:\s*"((?:[^"\\]|\\.)*)"/g;
const PREVIEW_STRING_ITEM_PATTERN = /"((?:[^"\\]|\\.)*)"/g;

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

function textValue(value: unknown) {
  return typeof value === "string" ? value.trim() : "";
}

function itemCommandText(item: unknown) {
  if (typeof item === "string") {
    return item.trim();
  }
  if (isRecord(item)) {
    for (const key of SHELL_COMMAND_KEYS) {
      const text = textValue(item[key]);
      if (text) {
        return text;
      }
    }
  }
  return "";
}

/** 从结构化入参取命令文本；`commands` 批量列表按 " ; " 连接成单行。 */
export function shellCommandTextFromArgs(args: Record<string, unknown> | undefined) {
  if (!args) {
    return "";
  }
  for (const key of SHELL_COMMAND_KEYS) {
    const text = textValue(args[key]);
    if (text) {
      return text;
    }
  }
  const items = args["commands"];
  if (!Array.isArray(items)) {
    return "";
  }
  return items.map(itemCommandText).filter(Boolean).join(SHELL_COMMAND_SEPARATOR);
}

/**
 * 从 `arg_preview` 的 `commands=` 值还原命令文本。预览有损（逐值截断），这里只做
 * 宽松提取：能认出 `"command":"…"` 就还原，认不出就放弃，绝不把半截 JSON 当命令。
 */
export function shellCommandTextFromPreview(value: string) {
  const text = value.trim();
  if (!text) {
    return "";
  }
  const commands = [...text.matchAll(PREVIEW_COMMAND_ITEM_PATTERN)]
    .map((match) => match[1]?.trim() ?? "")
    .filter(Boolean);
  if (commands.length > 0) {
    return commands.join(SHELL_COMMAND_SEPARATOR);
  }
  if (text.includes("{")) {
    // 对象数组但认不出命令字段：宁可没有摘要，也不展示结构符号。
    return "";
  }
  return [...text.matchAll(PREVIEW_STRING_ITEM_PATTERN)]
    .map((match) => match[1]?.trim() ?? "")
    .filter(Boolean)
    .join(SHELL_COMMAND_SEPARATOR);
}
