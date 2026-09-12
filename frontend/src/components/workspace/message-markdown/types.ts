// 由 components/workspace/message-markdown.tsx 机械拆分而来（P0-2），仅搬迁不改语义。
// 对外 props 类型（仅内部使用，不上浮到入口导出面）

export type MessageMarkdownProps = {
  className?: string;
  content: string;
  interrupted?: boolean;
  streaming?: boolean;
};
