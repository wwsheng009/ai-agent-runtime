// P2-1A：workspace.panels.filePreview 文案模块（运行时文件预览弹层）。
// 新增键请在本对象内按 feature 嵌套；en-US 同名模块需同步补齐（编译期对齐）。
export const zhWorkspaceFilePreview = {
  ariaLabel: "运行时文件预览",
  eyebrow: "Runtime · 文件预览",
  title: "运行时文件预览",
  close: "关闭文件预览",
  description:
    "内容来自运行时进程可见的本地文件（只读，POST /api/runtime/fs/read-file），不做本地缓存与改写。",
  tabs: {
    ariaLabel: "预览方式",
    raw: "原始",
    preview: "预览",
  },
  meta: {
    requestedPath: "请求路径",
    resolvedPath: "运行时解析路径",
    bytes: "{{count}} 字节",
    size: "{{size}}",
    lines: "{{count}} 行",
  },
  body: {
    loading: "正在读取文件…",
    empty: "文件为空（0 字节），没有可显示的内容。",
    binary: "判定为二进制内容（{{reason}}），不渲染文本，仅呈现真实字节数。",
    binaryNul: "含 NUL 字节",
    binaryUtf8: "非 UTF-8 文本",
    markdownTooLarge:
      "文档 {{size}} 字符，超过弹层内 Markdown 渲染上限（{{limit}} 字符），未渲染预览；请切到「原始」查看全文。",
    tooLarge:
      "文件 {{size}} 超过预览上限 {{limit}}，未渲染内容（避免拖垮页面）。",
  },
  error: {
    title: "文件读取失败",
    retry: "重试",
    unavailable:
      "运行时文件读取端点不可用（HTTP {{status}}）；文件内容未获取，不做本地替代。",
    unavailableUnknown:
      "运行时文件读取端点不可用；文件内容未获取，不做本地替代。",
  },
} as const;
