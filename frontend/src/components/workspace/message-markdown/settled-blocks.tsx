// 冻结前缀（已定稿块 + 尾块之前的续块）的渲染容器。

import { memo } from "react";
import { type Components } from "react-markdown";
import { StableMarkdownFragment } from "./stable-fragment";

export type SettledMarkdownBlock = {
  /** 块内容派生的稳定 key（见 block-key.ts），内容不变则跨帧不变。 */
  key: string;
  content: string;
};

/**
 * 为什么要单独成一个 memo 组件（而不是在 `MessageMarkdown` 里就地 map）：
 *
 * 流式期间打字机每 ~32ms 改一次揭示文本（它只驱动**尾块**），`MessageMarkdown`
 * 于是必须重跑一次 `splitStreamingMarkdown` —— 但**冻结前缀没变**。前缀元素若在
 * 父组件里就地生成，每拍都会重建 N 个 `StableMarkdownFragment` 元素：memo 能拦下
 * 子树的重新解析，却拦不下元素创建本身（dev 下 jsxDEV 每次 0.05–0.3ms，实测这段
 * map 占 141.8ms/12s，是流式期除尾块外的最大单点应用开销）。
 *
 * 收进 memo 组件后，每拍只剩一次 props 比较：`blocks` 数组当然每拍都是新对象
 * （解析结果逐拍重建），所以用自定义比较器按 **key 身份** 逐项比对 —— 内容没变的
 * 块 key 相同（`block-key.ts` 的 FNV 散列 + 出现次序），比较即短路，React 直接
 * 跳过整棵前缀子树（既不建元素，也不重解析 markdown）。
 */
export const SettledMarkdownBlocks = memo(
  function SettledMarkdownBlocks({
    blocks,
    components,
  }: {
    blocks: SettledMarkdownBlock[];
    components: Components;
  }) {
    return (
      <>
        {blocks.map((block) => (
          <StableMarkdownFragment
            key={block.key}
            components={components}
            content={block.content}
          />
        ))}
      </>
    );
  },
  (previous, next) =>
    previous.components === next.components &&
    areSameBlockKeys(previous.blocks, next.blocks),
);

/** 逐项比 key（字符串身份比较，命中即 O(1)）；长度不同直接判否。 */
function areSameBlockKeys(
  previous: readonly SettledMarkdownBlock[],
  next: readonly SettledMarkdownBlock[],
): boolean {
  if (previous.length !== next.length) {
    return false;
  }
  for (let index = 0; index < previous.length; index += 1) {
    if (previous[index].key !== next[index].key) {
      return false;
    }
  }
  return true;
}
