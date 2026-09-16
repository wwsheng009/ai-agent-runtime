// P0-6：workspace.panels.messages 文案模块（并行批次独占，勿跨模块写入）。
// 新增键请在本对象内按 feature 嵌套；en-US 同名模块需同步补齐（编译期对齐）。
export const zhWorkspacePanelsMessages = {
  messageList: {
    emptyTitle: "会话时间线为空",
    emptyHint:
      "发起一轮对话即可填充工作区时间线。运行时证据、相关条目与流式输出会回挂到产生它们的消息上。",
    emptyInfraOnlyHint:
      "这个会话没有落库的对话消息：CLI / 子代理批次驱动的会话只保留提示基础设施行。这一轮的执行记录请到「轨迹」页签查看。",
    backtrackNavHint:
      "回溯导航已激活 — 用 ↑/↓（或 j/k）选择用户轮次，Enter 打开确认对话框，Esc 退出。",
    loadEarlier: "加载更早的消息",
    loadingEarlier: "正在加载更早的消息…",
    streamStalledNotice: "服务端回合可能仍在运行 — 本页已停止接收数据。",
  },
  userBubble: {
    editAriaLabel: "在回溯前编辑该用户轮次",
    edit: "编辑",
    backtrackAriaLabel: "回溯到该用户轮次",
    backtrack: "回溯",
    editPromptAriaLabel: "编辑用户轮次提示词",
    editPromptPlaceholder: "编辑这条用户提示词，然后继续回溯…",
    cancel: "取消",
    continue: "继续回溯",
    copy: "复制",
    copyAriaLabel: "复制这条用户消息",
    selected: "已选中这条用户轮次",
    editHint:
      "内联编辑会作为回溯对话框的初始内容。在那里确认后即可截断后续轮次并预填输入框。",
  },
  messageCard: {
    streamingBadge: "响应流式输出中",
  },
  collapsedSummary: {
    tools: "{{count}} 个工具调用",
    replies: "{{count}} 条带回复",
    subagents: "{{count}} 个子代理",
    empty: "思考了一会儿",
    expand: "展开过程证据",
    collapse: "折叠过程证据",
  },
  systemPrompt: {
    title: "系统提示词",
    expand: "展开系统提示词",
    collapse: "折叠系统提示词",
  },
  contextRow: {
    title: "上下文注入",
    expand: "展开上下文",
    collapse: "折叠上下文",
  },
  toolReceipt: {
    title: "工具调用",
  },
  turnUsage: {
    summary: "Token 用量：输入 {{prompt}} · 输出 {{completion}} · 合计 {{total}}",
  },
  turnTail: {
    copy: "复制",
    copyAriaLabel: "复制这条回复",
    copied: "已复制",
    retryAriaLabel: "重试这一回合",
    statsLabel: "回合统计",
  },
  // 文案口径对齐参照实现（deepseek-harness locale.ts:71-72），不自创说法。
  branch: {
    label: "在新对话中分支",
    failed: "分支失败，请稍后重试",
    pending: "正在创建分支会话…",
  },
  flowFallback: {
    title: "未知事件类型",
    hint: "已保留原始载荷，只读展示。",
  },
  segmentFallback: {
    loading: "正在加载 {{label}}…",
  },
  relatedArtifactsFallback: {
    loading: "正在加载 {{count}} 条相关证据…",
  },
  markdown: {
    stoppedAriaLabel: "响应已停止",
    stopped: "已停止",
  },
  reasoningRow: {
    title: "推理过程",
    trimmed: "已截断 {{chars}} 个前导字符",
    expandLabel: "展开推理过程",
    collapseLabel: "折叠推理过程",
  },
  toolRow: {
    inputLabel: "输入",
    outputLabel: "输出",
    status: {
      started: "已开始",
      running: "执行中",
      finished: "已完成",
      failed: "失败",
    },
    announcement: "工具 {{name}} 状态：{{status}}",
    expandLabel: "展开工具详情",
    collapseLabel: "折叠工具详情",
    exitCode: "退出码 {{code}}",
    diffAdditions: "+{{value}}",
    diffRemovals: "−{{value}}",
    openFile: "打开 {{path}}",
    diff: {
      label: "差异（行级视图）",
      ariaLabel: "工具补丁的行级 diff：{{path}}",
      expand: "放大差异视图",
      filesAriaLabel: "补丁涉及 {{count}} 个文件",
      truncated: "补丁文本已截断（保留前 {{count}} 行）：行级视图只覆盖保留部分。",
      partial: "补丁文本不完整（末尾 hunk 行数不足），行号仍取自补丁本身，未补行。",
    },
  },
  richContent: {
    relatedEvidence: "相关证据",
  },
  backtrackDialog: {
    title: "回溯到这条用户消息",
    description: "截断该轮次之后的对话，并可选择还原后续文件变更。",
    closeLabel: "关闭回溯对话框",
    anchor: "锚点",
    planning: "正在规划回溯…",
    willRemove: "将移除",
    removedMessagesSuffix: "条消息",
    laterUserTurnsSuffix: "轮后续用户轮次",
    historyKeepsFirst: "历史记录保留前",
    historyKeepsMessagesSuffix: "条消息。",
    codeRestoreBaseCheckpoint: "代码还原可使用基准检查点",
    noCheckpoint: "该轮次尚未映射到任何变更检查点。",
    restoreMode: "还原模式",
    modeConversation: "仅还原对话",
    modeBoth: "还原对话与文件",
    modeCode: "仅还原文件（高级）",
    modeCodeHint: "只回写该轮次之后的文件变更，对话保持截断后的状态。",
    modeSelectHint: "切换选项只会刷新下方的预览，点确认按钮后才会执行回溯。",
    editPromptLabel: "在预填前编辑提示词",
    editPromptAriaLabel: "编辑回溯提示词",
    editPromptPlaceholder: "编辑原始用户提示词…",
    editPromptHint:
      "保持不变即可沿用原文。编辑内容会作为 edit_prompt 发送，并在应用后预填到输入框。",
    prefillToggle: "用原始（或编辑后的）提示词预填输入框",
    cancel: "取消",
    confirm: "确认回溯",
    working: "处理中…",
  },
} as const;
