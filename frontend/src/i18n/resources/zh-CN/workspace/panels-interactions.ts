// P1-7：workspace.panels.interactions 中文文案模块（审批/提问/计划评审统一呈现位）。
export const zhWorkspacePanelsInteractions = {
  approval: {
    title: "需要审批",
    unknownTool: "未知工具",
    approve: "批准",
    deny: "拒绝",
    explain: "解释",
    explaining: "正在生成解释…",
    explanationFailed: "解释生成失败：{{message}}",
    explanationSourceModel: "由 {{model}} 生成",
    explanationSourceRules: "规则解释（未调用模型）",
    remember: "记住此授权",
    rememberScopeLabel: "记忆范围",
    rememberScopeSession: "仅本会话",
    rememberScopeProject: "本项目（新会话仍生效）",
    rememberPattern: "将记住：{{pattern}}",
    feedbackPlaceholder: "说明（可选，会转达给模型）",
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
