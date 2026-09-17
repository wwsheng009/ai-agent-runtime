// C5：tool_result 指针行 `Full raw output artifact_id: art_<32位hex>` 的匹配工具。
// 与 artifact-output-link.tsx 分开存放，保持组件文件仅导出组件（react-refresh/only-export-components）。

// 指针行内任意位置出现即可命中（捕获 art_ 之后的完整 id token，仅限 32 位 hex）。
const ARTIFACT_OUTPUT_PATTERN = /Full raw output artifact_id:\s*(art_[0-9a-f]{32})/i;
// 整行就是指针行（用于段落/行内 code 整体替换判定；先 trim 再匹配）。
const ARTIFACT_OUTPUT_LINE_PATTERN = /^Full raw output artifact_id:\s*(art_[0-9a-f]{32})$/i;

/** 返回文本中第一处指针的完整 artifact id（含 art_ 前缀）；未命中返回 null。 */
export function findArtifactOutputId(text: string): string | null {
  return ARTIFACT_OUTPUT_PATTERN.exec(text)?.[1] ?? null;
}

/** 若整段（trim 后）就是指针行，返回完整 artifact id；否则返回 null。 */
export function findArtifactOutputLineId(text: string): string | null {
  return ARTIFACT_OUTPUT_LINE_PATTERN.exec(text.trim())?.[1] ?? null;
}