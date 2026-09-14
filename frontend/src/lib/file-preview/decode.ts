// P2-1A：运行时文件读取（fs/read-file）结果的解码层。
//
// 纪律：只做「字节 → 可展示内容」，不伪造内容。
//   * base64 非法或不完整 → 抛错（结构异常不伪装成空内容）；
//   * 空文件如实标记 empty，不渲染 0 行文本假装读过；
//   * 含 NUL 字节或非 UTF-8 序列 → 判定二进制，UI 只呈现字节数，不硬解成乱码文本。

export type FilePreviewBody =
  | { kind: "empty" }
  | { kind: "text"; text: string; lineCount: number }
  | { kind: "binary"; reason: "nul-byte" | "invalid-utf8" };

const BASE64_PATTERN = /^[A-Za-z0-9+/]*={0,2}$/;

/** 标准 base64 → 字节。空白字符容忍；长度不合法或字符越界即抛错。 */
export function decodeBase64Bytes(dataBase64: string): Uint8Array {
  const normalized = dataBase64.replace(/\s+/g, "");
  if (!normalized) {
    return new Uint8Array(0);
  }
  if (normalized.length % 4 !== 0 || !BASE64_PATTERN.test(normalized)) {
    throw new Error("file payload is not valid base64");
  }
  const binary = atob(normalized);
  const bytes = new Uint8Array(binary.length);
  for (let index = 0; index < binary.length; index += 1) {
    bytes[index] = binary.charCodeAt(index);
  }
  return bytes;
}

/** 行数口径：末行换行不计入新行；空文本为 0 行。 */
export function countPreviewLines(text: string): number {
  if (!text) {
    return 0;
  }
  const normalized = text.endsWith("\n") ? text.slice(0, -1) : text;
  return normalized === "" ? 0 : normalized.split(/\r?\n/).length;
}

export function decodeFilePreview(dataBase64: string): FilePreviewBody {
  const bytes = decodeBase64Bytes(dataBase64);
  if (bytes.length === 0) {
    return { kind: "empty" };
  }
  if (bytes.includes(0)) {
    return { kind: "binary", reason: "nul-byte" };
  }

  let text: string;
  try {
    text = new TextDecoder("utf-8", { fatal: true }).decode(bytes);
  } catch {
    return { kind: "binary", reason: "invalid-utf8" };
  }
  if (text.startsWith("\uFEFF")) {
    text = text.slice(1);
  }
  return { kind: "text", text, lineCount: countPreviewLines(text) };
}

/** 人类可读字节数；单位换算只用于展示，原始字节数仍由 UI 明示。 */
export function formatByteSize(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) {
    return "";
  }
  if (bytes < 1024) {
    return `${bytes} B`;
  }
  const units = ["KB", "MB", "GB"];
  let value = bytes / 1024;
  let unitIndex = 0;
  while (value >= 1024 && unitIndex < units.length - 1) {
    value /= 1024;
    unitIndex += 1;
  }
  return `${value >= 10 ? value.toFixed(0) : value.toFixed(1)} ${units[unitIndex]}`;
}
