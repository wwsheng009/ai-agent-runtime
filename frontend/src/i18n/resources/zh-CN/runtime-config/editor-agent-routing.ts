// 由 src/i18n/resources/zh-CN.ts 机械拆分而来（P0-6），仅搬迁不改语义。

export const zhRuntimeConfigEditorAgentRouting = {
  agentRouting: {
    title: "子 Agent / Team 难度路由",
    description:
      "为新创建的子 Agent 和 Team task 设置难度到 provider、模型和 reasoning effort 的映射。已运行任务不会被改写。",
    scopes: {
      subagents: "子 Agent",
      teams: "Team",
    },
    inheritTeam: {
      label: "Team 沿用子 Agent 路由",
      description: "开启时不保存独立 Team 策略，Team task 使用子 Agent 的难度映射。",
    },
    enabled: {
      label: "启用难度路由",
      description: "关闭时继续使用父 Agent 当前 provider 和模型。",
    },
    defaultDifficulty: {
      label: "默认难度",
      description: "任务没有显式难度时使用此档位。",
    },
    inheritParent: {
      label: "缺失配置时继承父 Agent",
      description: "某个档位未填写 provider 或模型时回退父 Agent。",
    },
    validateModels: {
      label: "校验模型能力",
      description: "根据 provider 模型目录校验模型和 reasoning effort。",
    },
    columns: {
      difficulty: "难度",
      provider: "Provider",
      model: "模型",
      reasoning: "Reasoning",
    },
    difficulties: {
      easy: "简单",
      normal: "常规",
      hard: "困难",
      expert: "专家",
    },
    defaultBadge: "默认",
    inheritPlaceholder: "继承父 Agent",
    modelPlaceholder: "输入模型名称",
    noProviders: "当前没有启用的 provider。请先在 Provider 配置中启用至少一个 provider。",
    missingRequiredRoutes:
      "已关闭父 Agent 回退，请补全这些难度的 Provider 和模型：{{difficulties}}。",
    health: {
      teamInherited: "来源：子 Agent",
      configuredCount: "{{count}} 档独立配置",
      inheritedCount: "{{count}} 档继承",
      errorCount: "{{count}} 个阻断问题",
      warningCount: "{{count}} 个检查项",
      ready: "配置可用",
      errorsPreventRouting:
        "当前有 {{count}} 个问题会导致对应难度无法按此策略路由。请修正标记为“需修正”的档位后再保存。",
      configured: "独立路由",
      providerDefault: "Provider 默认模型",
      routingDisabled: "路由关闭",
      parentInherited: "继承父级",
      routeError: "需修正",
      routeWarning: "需检查",
      issues: {
        ambiguousProviderAlias:
          "Provider “{{provider}}”同时是多个 Provider 的模型别名，运行时解析结果不稳定；请选择明确的 Provider 名称。",
        disabledProvider:
          "Provider “{{provider}}”已禁用，运行时可能回退父 Agent 或拒绝该路由。",
        missingRequiredRoute:
          "此档位缺少完整的 Provider / 模型，且父 Agent 回退已关闭，运行时会拒绝该路由。",
        modelNotListed:
          "模型 “{{model}}”不在 Provider “{{resolvedProvider}}”的 supported_models 中；目录可能非穷举，请确认模型名称和能力配置。",
        modelWithoutProvider:
          "仅配置了模型 “{{model}}”，它会与父 Agent 的 Provider 组合；请确认两者兼容。",
        providerAlias:
          "“{{provider}}”会作为模型别名解析到 Provider “{{resolvedProvider}}”；使用明确的 Provider 名称可避免别名冲突。",
        providerWithoutModel:
          "Provider “{{provider}}”没有可识别的默认模型，运行时将尝试父级模型，可能与该 Provider 不兼容。",
        reasoningUnsupported:
          "模型 “{{model}}”的能力声明不支持 reasoning effort “{{effort}}”，运行时将按当前策略降档、清空或拒绝。",
        unknownProvider:
          "找不到 Provider “{{provider}}”，运行时可能回退父 Agent 或拒绝该路由。",
      },
    },
    maxExpertConcurrency: {
      label: "专家任务最大并发",
      description:
        "限制 expert 档位的同时执行数；正数为上限，-1 表示显式不限（0 与 -1 同义，保存时会写成 -1）。",
    },
    reasoningPolicy: {
      label: "不支持 Reasoning 时",
      description: "ignore 清空，downgrade 降档，fail 拒绝该路由。",
    },
    reasoningPolicies: {
      ignore: "忽略并清空",
      downgrade: "自动降档",
      fail: "拒绝路由",
    },
    preview: {
      title: "有效路由试算",
      difficulty: "任务难度",
      role: "任务角色",
      rolePlaceholder: "例如 researcher",
      goal: "任务目标",
      goalPlaceholder: "输入用于难度推断的任务目标",
      run: "运行试算",
      running: "试算中",
      failed: "路由试算失败",
      documentUnavailable: "当前配置草稿尚未加载，无法执行路由试算。",
      routingEnabled: "路由已启用",
      routingDisabled: "路由已关闭",
      provider: "Provider",
      model: "模型",
      reasoning: "Reasoning",
      parent: "父 Agent",
      fallback: "发生回退",
      routingSources: {
        subagent: "子 Agent 策略",
        team_independent: "Team 独立策略",
        subagent_inherited: "继承子 Agent 策略",
      },
      sources: {
        disabled: "父 Agent 直传",
        explicit_override: "显式覆盖",
        role_override: "角色覆盖",
        difficulty_level: "难度档位",
        parent_inherit: "继承父 Agent",
        fallback: "回退结果",
      },
      difficulties: {
        easy: "简单",
        normal: "常规",
        hard: "困难",
        expert: "专家",
      },
      warnings: {
        difficulty_missing_defaulted: "任务未指定难度，已使用默认难度。",
        difficulty_invalid_defaulted: "任务难度无效，已使用默认难度。",
        difficulty_promoted_by_heuristic: "任务内容触发难度提升规则。",
        explicit_provider_override_not_allowed: "显式 Provider 不在允许列表中。",
        explicit_provider_override_denied: "当前策略不允许显式覆盖 Provider。",
        explicit_model_override_not_allowed: "显式模型不在允许列表中。",
        explicit_model_override_denied: "当前策略不允许显式覆盖模型。",
        explicit_reasoning_override_denied: "当前策略不允许显式覆盖 Reasoning。",
        provider_missing_inherited_parent: "路由未配置 Provider，已继承父 Agent。",
        provider_unresolved: "路由 Provider 无法解析。",
        provider_fallback_parent: "Provider 已回退到父 Agent。",
        model_default_provider: "路由未配置模型，已使用 Provider 默认模型。",
        model_missing_inherited_parent: "路由未配置模型，已继承父 Agent。",
        model_unsupported: "模型不在 Provider 的已知能力目录中。",
        model_fallback_parent: "模型已回退到父 Agent。",
        reasoning_effort_capability_unknown: "模型未声明 Reasoning 能力，保留当前 effort。",
        reasoning_effort_unsupported_downgraded: "Reasoning effort 不受支持，已自动降档。",
        reasoning_effort_unsupported_downgrade_unavailable: "Reasoning effort 不受支持，且没有可用降档。",
        reasoning_effort_unsupported_ignored: "Reasoning effort 不受支持，已清空。",
        budget_tokens_capped_by_route: "任务 token 预算超过路由上限，已按路由上限执行。",
      },
    },
  },
} as const;
