// P1-7：workspace.panels.interactions 中文文案模块（审批/提问/计划评审统一呈现位）。
export const zhWorkspacePanelsInteractions = {
  approval: {
    title: "需要审批",
    unknownTool: "未知工具",
    approve: "批准",
    deny: "拒绝",
  },
  question: {
    title: "需要你的回答",
    required: "必答",
    placeholder: "输入回答…",
    submit: "提交回答",
  },
  planReview: {
    title: "计划待决策",
    hint: "审阅计划内容后选择批准、请求修改或退出计划模式。",
  },
  submitting: "提交中…",
} as const;
