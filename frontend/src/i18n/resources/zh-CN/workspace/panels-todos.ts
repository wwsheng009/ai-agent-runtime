// 任务面板（composer 上沿浮动列表）文案模块（按 feature 独占，勿跨模块写入）。
// 新增键请在本对象内按 feature 嵌套；en-US 同名模块需同步补齐（编译期 shape 对齐）。
export const zhWorkspacePanelsTodos = {
  ariaLabel: "当前任务面板",
  title: "当前任务",
  // 进度按「已完成 · 进行中 · 待处理」三段拼装，零值分段由组件省略（方案 §3.3）。
  counts: {
    completed: "已完成 {{count}}",
    in_progress: "进行中 {{count}}",
    pending: "待处理 {{count}}",
  },
  current: "进行中：{{text}}",
  expand: "展开任务列表",
  collapse: "收起任务列表",
  more: "还有 {{count}} 项",
  status: {
    pending: "待处理",
    in_progress: "进行中",
    completed: "已完成",
  },
} as const;
