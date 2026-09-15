// 冻结块（已定稿 markdown 块）的渲染 key 计算。
//
// 为什么 key 不能带 block.start / 冻结代（generation）：
// 流式过程中块边界会随解析重排（末尾未闭合 fence 截断前一块的 end、尾部不稳定区
// 回退、块合并）而前后移动，冻结前缀的长度与各块 offset 因而每帧都可能变；
// 一旦 key 里带 offset 或全局代，**整段前缀会在同一帧里集体 remount** ——
// 实测单帧 300~600 次 childList（copy 容器直接换子节点）、直接换出 90ms 级长任务，
// 就是「流式时整页卡住」的主因之一。
//
// 只按「块内容」定 key：内容没变的块跨帧得到同一个 key，React 复用 fiber、
// `StableMarkdownFragment` 的 memo 直接跳过重解析；内容真的变了才换 key
//（重新生成 / 改写），且只影响那一个块。重复内容用出现次序消歧。

/**
 * FNV-1a 32 位散列（base36）+ 长度：把块内容压成短 key，避免长文本直接进 fiber key。
 * 非加密用途，仅用于在同一段流式文本内区分块身份。
 */
function hashBlockContent(content: string): string {
  let hash = 0x811c9dc5;
  for (let index = 0; index < content.length; index += 1) {
    hash ^= content.charCodeAt(index);
    hash = Math.imul(hash, 0x01000193);
  }
  return `${(hash >>> 0).toString(36)}.${content.length.toString(36)}`;
}

/** 块内容 + 出现次序 → 稳定 key（同一内容第 n 次出现用 `#n` 区分）。 */
export function markdownBlockKey(content: string, occurrence: number): string {
  return occurrence === 0
    ? hashBlockContent(content)
    : `${hashBlockContent(content)}#${occurrence}`;
}
