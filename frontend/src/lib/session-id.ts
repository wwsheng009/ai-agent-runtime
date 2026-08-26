/**
 * 会话 ID 的规范形式，与后端 chat.NormalizeSessionID（TrimSpace + filepath.Base）
 * 完全一致：去除首尾空白、去掉尾部路径分隔符后取路径最后一段。
 *
 * 前端所有把「URL/输入的会话 ID」与「后端返回的会话 ID」做精确匹配的地方
 * （pinned 会话匹配、线程选择）都必须先经过本函数，否则同一逻辑会话的
 * 字符串变体（"abc/"、" abc "、"dir/abc"）会失配并静默回退。
 *
 * 幂等：normalize(sessionId) 的输出不再含空白与路径分隔符。
 */
export function normalizeSessionId(
  sessionId: string | null | undefined,
): string {
  if (!sessionId) {
    return "";
  }
  const trimmed = sessionId.trim();
  if (!trimmed) {
    return "";
  }
  // filepath.Base 会先去掉尾部路径分隔符；兼容 Windows 的 "\" 与 POSIX 的 "/"。
  const withoutTrailingSeparators = trimmed.replace(/[\\/]+$/, "");
  if (!withoutTrailingSeparators) {
    return "";
  }
  const lastSeparator = Math.max(
    withoutTrailingSeparators.lastIndexOf("/"),
    withoutTrailingSeparators.lastIndexOf("\\"),
  );
  return lastSeparator >= 0
    ? withoutTrailingSeparators.slice(lastSeparator + 1)
    : withoutTrailingSeparators;
}
