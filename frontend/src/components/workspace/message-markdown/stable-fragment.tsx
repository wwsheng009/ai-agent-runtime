// 由 components/workspace/message-markdown.tsx 机械拆分而来（P0-2），仅搬迁不改语义。
// 稳定区冻结渲染片段（memo 前缀复用）

import { memo } from "react";
import ReactMarkdown, { type Components } from "react-markdown";
import remarkBreaks from "remark-breaks";
import remarkGfm from "remark-gfm";
import { normalizeMarkdown } from "@/components/workspace/message-markdown-streaming";
import { rehypeCollapseBreakNewlines } from "./rehype-collapse-break-newlines";

/**
 * 稳定区渲染片段：content 在流式追加期间保持冻结（前缀复用），
 * memo 使 React 在 stableContent 未变化时跳过整棵子树的重渲染与重解析。
 */
export const StableMarkdownFragment = memo(function StableMarkdownFragment({
  components,
  content,
}: {
  components: Components;
  content: string;
}) {
  return (
    <ReactMarkdown
      components={components}
      rehypePlugins={[rehypeCollapseBreakNewlines]}
      remarkPlugins={[remarkGfm, remarkBreaks]}
    >
      {normalizeMarkdown(content, false)}
    </ReactMarkdown>
  );
});
