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
  // 字段分区标题：14 行元数据按「基本信息 / 时间 / 运行 / 内容」分段，
  // 同一张卡片内用小标题 + 细分隔线切分，避免平铺成一长条难以定位。
  sections: {
    basic: "基本信息",
    timing: "时间",
    runtime: "运行",
    content: "内容",
  },
  // 「网络详情」：SSE live 的实时观测块（传输层字节/帧 + 渲染闸门计数）。
  // 目的是把「页面不动」拆成两个可判定的结论：SSE 没有事件（服务端/网络），
  // 还是事件到了但没渲染（前端闸门/提交）。
  network: {
    title: "网络详情",
    channels: {
      runtime: "运行时流",
      chat: "直连回合",
    },
    state: {
      active: "连接中",
      inactive: "未连接",
    },
    fields: {
      lastFrame: "最近字节",
      lastEvent: "最近事件",
      lastKeepalive: "最近保活",
      events: "事件",
      keepalives: "保活",
      bytes: "字节",
      stalls: "静默超时",
      errors: "错误",
      cursor: "游标",
      opens: "建连/关闭",
      gap: "帧间隔",
      never: "尚无",
      none: "—",
    },
    gate: {
      title: "渲染闸门",
      open: "开",
      closed: "关",
      blockedDeltas: "被拦增量",
      unownedTurns: "未认领回合",
      snapshotRefreshes: "快照刷新",
      turn: "在途回合",
      none: "无",
    },
    // 会话订阅（Batch 4 §4.6）：注册表观测投影——本窗口有几条 live / 几条已降级
    // 轮询、页面是否触发后台降采样、live 预算多少。开关关闭时区块不渲染。
    subscriptions: {
      title: "会话订阅",
      live: "实时",
      poll: "轮询",
      idle: "空闲",
      none: "未订阅",
      total: "订阅数",
      session: "本会话",
      visibility: "页面可见性",
      foreground: "前台",
      background: "后台降采样",
      budget: "live 预算",
    },
    dom: {
      title: "DOM 活跃度",
      changes: "变更",
      lastChange: "最近变更",
      observing: "已挂载消息列",
      detached: "未找到消息列",
    },
    frames: {
      title: "最近帧",
      empty: "尚未收到任何帧",
      keepalive: "保活",
    },
    // 流量波动图：每秒吞吐（bytes/s）的迷你柱状图。
    // 计数网格回答「收过多少」，它回答「现在还收不收、是持续还是突刺」。
    traffic: {
      title: "近 60s 流量",
      empty: "近 60s 没有流量",
      peak: "峰值",
      total: "合计",
      currentSecond: "当前秒（进行中）",
      windowStart: "60s 前",
      windowNow: "现在",
    },
    verdict: {
      states: {
        healthy: "连接正常",
        renderBlocked: "到达未渲染",
        noEvents: "无事件",
        channelError: "通道异常",
        idle: "静默",
      },
      hints: {
        healthy: "SSE 正在推数据且闸门开启：渲染链路正常。",
        renderBlocked:
          "SSE 有增量到达，但被前端闸门拦下：问题在前端渲染侧，不在网络。",
        noEvents:
          "通道已建立但近期没有任何字节：问题在 SSE live / 服务端 / 代理链路，不是前端渲染。",
        channelError: "连接层报错或连续失败降级：先排查网络与运行时服务。",
        idle: "通道已建立，当前没有在途回合在推数据。",
      },
    },
  },
} as const;
