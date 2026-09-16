// P0-6：workspace.panels.sessionDetail 文案模块（右侧栏「会话详情」面）。
// 新增键请在本对象内按 feature 嵌套；en-US 同名模块需同步补齐（编译期对齐）。
export const zhWorkspacePanelsSessionDetail = {
  ariaLabel: "会话详情面板",
  title: "会话详情",
  untitled: "未命名会话",
  refresh: "刷新会话详情",
  retry: "重试",
  loading: "正在加载会话详情…",
  errorTitle: "会话详情加载失败",
  empty: "尚未选择会话",
  emptyHint: "选中一个会话后，这里会显示它的状态、工作目录与运行信息。",
  states: {
    active: "活跃",
    idle: "空闲",
    closed: "已关闭",
    archived: "已归档",
    unknown: "未知状态",
  },
  // 本地线程 ↔ 运行时会话的关联状态；判据由 shell 侧给定（见 workspace-shell-shared）。
  // 名称沿用侧栏同一套词汇，解释同时作为徽标 tooltip 与区块正文。
  relation: {
    label: "关联状态",
    transportLabel: "传输通道",
    states: {
      attached: "已附着运行时会话",
      restored: "已恢复运行时会话",
      error: "会话同步异常",
      pending: "尚未附着运行时会话",
    },
    details: {
      attached: "已附着到当前工作区流程中的运行时会话。",
      restored: "已从运行时会话历史中恢复，可继续推进。",
      error: "会话已存在，但最新同步失败，需要再次尝试恢复。",
      pending: "尚未附着运行时会话。",
    },
  },
  fields: {
    id: "会话 ID",
    userId: "用户",
    createdBy: "创建者",
    workspace: "工作目录",
    workspaceUnbound: "未绑定工作目录",
    createdAt: "创建时间",
    updatedAt: "更新时间",
    expiresAt: "过期时间",
    totalTurns: "回合数",
    lastAgent: "最近 Agent",
    lastSkill: "最近技能",
    lastModel: "最近模型",
    titleSource: "标题来源",
    tags: "标签",
    summary: "摘要",
  },
} as const;
