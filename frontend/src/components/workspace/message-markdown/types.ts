// 由 components/workspace/message-markdown.tsx 机械拆分而来（P0-2），仅搬迁不改语义。
// 对外 props 类型（仅内部使用，不上浮到入口导出面）

export type MessageMarkdownProps = {
  className?: string;
  content: string;
  interrupted?: boolean;
  streaming?: boolean;
  /**
   * C5：宿主可选的 artifact 详情打开回调。tool_result 中的
   * `Full raw output artifact_id: art_<32hex>` 指针行命中时，点击「查看完整原始输出」
   * 优先调用该回调（参数为 art_ 之后的完整 id）；未提供时回退为复制 id 到剪贴板。
   * 默认可选，不破坏现有调用方。
   */
  onSelectArtifact?: (artifactId: string) => void;
};
