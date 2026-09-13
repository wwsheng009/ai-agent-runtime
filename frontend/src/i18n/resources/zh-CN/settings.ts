// 由 src/i18n/resources/zh-CN.ts 机械拆分而来（P0-6），仅搬迁不改语义。

export const zhSettings = {
  dialog: {
    eyebrow: "Workspace settings",
    title: "前端工作区设置",
    description:
      "这里只保留前端本地设置。后端 config.yaml 已迁移到独立的 Runtime Config 页面，避免和前端配置混在同一个对话框里。",
    backendConfig: "后端配置页",
    resetFrontendDefaults: "恢复前端默认",
    close: "关闭设置",
    localStorageFooter:
      "前端设置会立即写入当前浏览器的 localStorage。后端配置请使用独立的 Runtime Config 页面。工作区中可用 Ctrl/Cmd + , 快速再次打开此面板。",
  },
  sections: {
    appearance: {
      label: "外观",
      description: "强调色、主题、字体与动效",
    },
    workspace: {
      label: "工作区",
      description: "布局与文件栏行为",
    },
    chat: {
      label: "聊天默认值",
      description: "provider、model、推理强度",
    },
    notifications: {
      label: "通知",
      description: "桌面提醒与权限",
    },
    harness: {
      label: "Harness",
      description: "权限、记忆与本地插件",
    },
    about: {
      label: "关于",
      description: "运行时摘要与本地存储",
    },
  },
  localization: {
    title: "语言与区域",
    description: "控制界面语言、日期时间和相对时间格式。",
    system: "跟随系统",
    simplifiedChinese: "简体中文",
    english: "English",
  },
  appearance: {
    theme: "主题",
    themeDescription:
      "控制整个前端界面的深浅色模式，包括工作区、设置面板和首页。",
    themeApplied: "实际生效",
    currentlySetTo: "当前设置为",
    themeSystemResolved: "跟随系统（当前解析为 {{resolved}}）",
    themeOptions: {
      system: {
        label: "跟随系统",
        description: "监听系统深浅色偏好，在系统切换时自动同步。",
      },
      light: {
        label: "浅色",
        description: "适合白天、文档阅读和明亮环境。",
      },
      dark: {
        label: "深色",
        description: "适合终端式工作流、夜间和低光环境。",
      },
    },
    accent: "强调色",
    accentDescription:
      "影响设置弹窗、主操作按钮以及工作区里已接入变量的高亮色。",
    accentOptions: {
      gold: {
        label: "Amber relay",
        description: "保持当前工作区的金色高亮基调。",
      },
      cyan: {
        label: "Cool signal",
        description: "把主强调色切换成更偏系统状态的冷色。",
      },
      violet: {
        label: "Route focus",
        description: "使用更靠近路由与规划语义的紫色高亮。",
      },
    },
    fontFamily: "字体族",
    fontFamilyDescription:
      "在界面正文和代码表面之间统一切换字体族。",
    bodyFont: "界面与正文",
    bodyFontDescription:
      "会统一影响首页、工作区、设置面板，以及使用 `font-serif` 的展示标题。",
    codeFont: "代码与日志",
    codeFontDescription:
      "会应用到代码块、日志 JSON 预览，以及使用单宽编辑样式的文本区域。",
    fontFamilyOptions: {
      system: {
        label: "System UI",
        description: "Segoe UI / Helvetica Neue / system-ui",
        sample: "长会话中操作细节仍保持清晰可读。",
      },
      humanist: {
        label: "Readable humanist",
        description: "Trebuchet MS / Verdana / Palatino",
        sample: "适合工作区密集文案和设置项阅读。",
      },
      editorial: {
        label: "Editorial modern",
        description: "Aptos / Cambria / Georgia",
        sample: "让首页文案与长段阅读拥有更柔和的节奏。",
      },
    },
    codeFontOptions: {
      jetbrains: {
        label: "JetBrains stack",
        description: "JetBrains Mono / Cascadia Code / Consolas",
        sample: 'const traceId = receipts.latest()?.trace_id ?? "";',
      },
      cascadia: {
        label: "Cascadia stack",
        description: "Cascadia Code / JetBrains Mono / Consolas",
        sample: "await runtime.follow({ requestId, sessionId });",
      },
      classic: {
        label: "Classic console",
        description: "Consolas / SFMono / Menlo / Monaco",
        sample: 'if (event.level === "error") return halt(event);',
      },
    },
    size: "字号",
    sizeDescription:
      "整体字号控制全站基础文字节奏；聊天和代码字号会覆盖各自的高频阅读区域。",
    sizeHint: "支持直接输入具体字号，范围 {{min}}-{{max}}px。",
    customPixels: "自定义像素值",
    customPixelsUnit: "px",
    restoreDefaultWithValue: "恢复默认 {{value}}",
    motion: "动效",
    motionDescription:
      "适合在远程桌面、录屏或你希望界面更克制时开启。",
    reducedMotion: "减少动画",
    reducedMotionDescription:
      "关闭脉冲、漂浮和大部分过渡动画，同时让滚动行为回到即时模式。",
    preview: "实时预览",
    previewDescription:
      "下面这段示例会跟随你当前选择的字体族和字号即时刷新。",
    workspaceSample: "工作区正文预览",
    chatSample: "聊天正文预览",
    codeSample: "代码与字体栈预览",
    previewSampleText: "长会话中操作细节仍保持清晰可读。",
    previewWorkspaceBody: "任务线程、运行时上下文与工件会在这个区域里保持可读。",
    currentUIStack: "当前界面字体栈：",
    currentSerifStack: "当前标题衬线栈：",
    currentCodeStack: "当前代码字体栈：",
    currentScope: "当前生效范围",
    immediate: "即时生效",
    browserPersistence: "浏览器持久化",
    uiTypography: "界面与正文",
    codeTypography: "代码与日志",
  },
  workspace: {
    density: "界面密度",
    densityDescription:
      "影响侧栏、消息区域和输入区的垂直留白。",
    densityOptions: {
      comfortable: {
        label: "舒展",
        description: "保留更大的段落间距和侧栏留白，适合长时间阅读。",
      },
      compact: {
        label: "紧凑",
        description: "减少消息和导航区间距，适合在较小屏幕上查看更多内容。",
      },
    },
    compact: "紧凑",
    comfortable: "舒展",
    fileBar: "文件栏行为",
    fileBarDescription:
      "控制运行时产生新工件时右侧文件面板的默认打开方式。",
    autoOpenArtifacts: "自动打开文件面板",
    autoOpenArtifactsDescription:
      "开启后，运行时把新工件自动选中时会联动展开右侧栏；关闭后只记录选择，不强制展开。",
  },
  chat: {
    title: "默认模型路由",
    description:
      "这里修改的是工作区发送新回合时默认附带的 provider 和 model。",
    defaultProvider: "Provider",
    defaultModel: "Model",
    loadingProvider: "正在加载 provider...",
    noProvider: "暂无 provider",
    loadingModel: "正在加载模型...",
    noModel: "当前 provider 没有可选模型",
    summaryLoading: "运行时模型目录加载中。",
    summaryTemplate:
      "当前已识别 {{providerCount}} 个 provider，当前默认会话路由到 {{provider}} / {{model}}。",
    openBackendConfig: "打开后端配置页",
    manageProviders: "管理 Provider 列表",
    executionMode: "执行模式",
    executionModeDescription:
      "控制工作区聊天是否进入后端 ReAct 工具循环。",
    enableReact: "启用 ReAct 工具循环",
    enableReactDescription:
      "开启后，请求会携带 <code>enable_react: true</code>，后端会把工具定义暴露给模型并进入工具调用循环。关闭后，仍可做 skill route 或直接 LLM fallback，但模型本身不会触发工具调用。",
    currentMode: "当前模式",
    reactMode: "ReAct 工具模式",
    routeDirectMode: "路由 / 直连模式",
    reasoning: "推理强度",
    reasoningDescription:
      "选择默认推理强度，影响新回合的规划深度和工具预算。",
    reasoningOptions: {
      default: {
        label: "运行时默认",
        description: "把推理强度完全交给后端默认策略处理。",
      },
      minimal: {
        label: "Minimal",
        description: "最省推理预算，适合简单追问和极短回合。",
      },
      low: {
        label: "Low",
        description: "更快返回，适合普通问答与小改动。",
      },
      medium: {
        label: "Medium",
        description: "兼顾速度和质量，适合绝大多数日常任务。",
      },
      high: {
        label: "High",
        description: "更偏向复杂拆解和多步推理。",
      },
    },
    maxSteps: "最大步骤数",
    maxStepsDescription:
      "用于限制单轮里最多允许的规划 / 路由 / 工具执行步数；设为 0 表示不限制。",
    currentMaxSteps: "当前值为 {{count}}。",
    maxStepsAdvice:
      "默认 0（不限制），由模型自行决定何时结束；需要约束时通常 8 到 12 足够覆盖常见工作区任务。",
  },
  notifications: {
    title: "工作区通知",
    description:
      "仅在当前标签页不可见时，用于提醒一轮响应完成或运行时出错。",
    desktop: "桌面提醒",
    desktopDescription:
      "关闭后，即便浏览器权限已授权，也不会弹出桌面提醒。",
    permission: "权限状态",
    permissionDescription: "需要浏览器授权。若当前页面可见，则不会打断你。",
    permissionStates: {
      granted: "已授权，可以在后台收到完成或错误通知。",
      denied: "已被浏览器阻止，需要在站点权限中手动重新允许。",
      default: "尚未授权，点击右侧按钮向浏览器申请通知权限。",
      unsupported: "当前环境不支持浏览器桌面通知。",
    },
    currentConfig: "当前配置",
    effectiveCondition: "实际生效条件",
    effectiveConditionDescription:
      "只有在总开关开启、桌面提醒开启、权限为 granted 且页面处于后台时，通知才会真正弹出。",
    requestPermission: "请求权限",
    enableDesktop: "开启桌面提醒",
    disableDesktop: "关闭桌面提醒",
    enabled: "开启",
    disabled: "关闭",
    currentConfigMasterSwitch: "通知总开关",
    currentConfigDesktopSwitch: "桌面提醒",
  },
  harness: {
    title: "项目 Harness",
    description:
      "查看当前工作区的项目权限规则、持久化授权、项目记忆笔记，以及本地插件目录。",
    workspaceTitle: "当前工作区",
    workspaceMissing: "当前 runtime client 尚未设置工作区路径。",
    loading: "加载中",
    ready: "就绪",
    refresh: "刷新",
    notAvailable: "不可用",
    permissionsTitle: "权限规则",
    permissionsDescription:
      "只读展示项目 `.aicli/permissions.yaml`。修改规则请直接编辑磁盘文件。",
    permissionsFile: "权限文件",
    permissionsExists: "已加载 version {{version}}",
    permissionsMissing: "尚未找到项目权限文件。",
    denyTools: "拒绝工具",
    allowTools: "允许工具",
    noDenyTools: "未配置硬拒绝工具。",
    noAllowTools: "未配置硬允许列表。",
    rulesTitle: "静态规则",
    noRules: "permissions.yaml 中没有命名规则。",
    unnamedRule: "规则 {{index}}",
    grantsTitle: "记住的授权",
    grantsDescription:
      "持久化 always-allow 授权，保存在项目 `.aicli/grants.json`。危险工具不能被记住。",
    grantsStore: "授权存储",
    rememberGrant: "记住授权",
    grantToolPlaceholder: "工具名，例如 write",
    grantPatternPlaceholder: "可选路径/模式",
    grantHint: "模式可选。留空表示工具级授权，且仍会拒绝危险工具。",
    remember: "记住",
    revoke: "撤销",
    toolWideGrant: "（工具级）",
    noGrants: "还没有持久化授权。",
    grantRememberFailed: "记住授权失败",
    grantRevokeFailed: "撤销授权失败",
    memoryTitle: "记忆笔记",
    memoryDescription:
      "对 `.aicli/memory` 下的项目记忆做关键词列表/搜索与追加。",
    memorySearch: "搜索笔记",
    memorySearchPlaceholder: "关键词查询",
    search: "搜索",
    memoryAppend: "追加笔记",
    memoryTextPlaceholder: "写一条可复用的项目记忆",
    memoryTagsPlaceholder: "可选标签，逗号分隔",
    append: "追加",
    noMemoryNotes: "还没有记忆笔记。",
    noMemoryHits: "没有匹配该查询的笔记。",
    memoryAppendFailed: "追加记忆失败",
    pluginsTitle: "插件目录",
    pluginsDescription:
      "展示项目/用户插件根目录下发现的本地插件。市场安装不在本面板范围。",
    pluginNoDescription: "暂无描述。",
    pluginActive: "已激活",
    pluginInactive: "未激活",
    pluginVersion: "v{{version}}",
    capabilityBadge: "cap:{{capability}}",
    trust: "信任",
    untrust: "取消信任",
    enable: "启用",
    disable: "禁用",
    noPlugins: "未发现本地插件。",
    pluginUpdateFailed: "更新插件失败",
  },
  about: {
    currentWorkspace: "当前工作区",
    description:
      "这些信息用于快速确认当前前端会把请求发到哪里，以及本地设置保存在什么位置。",
    runtimeIdentityDescription:
      "前端会为当前浏览器生成一个持久化 runtime client id，并用它派生 userId。重置后会切换到新的会话命名空间。",
    apiBase: "API 基址",
    apiBaseFallback: "同源 /api 代理",
    currentRoute: "当前路由",
    runtimeIdentity: "运行时身份",
    runtimeUserId: "运行时用户 ID",
    workspacePath: "工作区路径",
    resetRuntimeClientId: "重置本地 runtime client id",
    runtimeOverview: "运行时概览",
    runtimeOverviewDescription:
      "这里读的是当前页面已经加载到的运行时摘要，不会额外发新请求。",
    localStorage: "本地存储",
    localStorageDescription:
      "设置数据存于浏览器 localStorage，不会写回仓库配置文件。",
    settingsKey: "settings localStorage 键",
    runtimeClientKey: "runtime client localStorage 键",
    selectedProvider: "可选 provider",
    selectedModel: "当前 model",
    sessionCount: "会话数",
    recoverableSessions: "可恢复会话",
    activeTeams: "运行中团队",
    activeTeamsSummary: "共加载 {{count}} 个团队摘要",
    sessionBreakdown: "{{active}} 活跃 / {{archived}} 已归档",
    latestUpdated: "最近更新 {{time}}",
    noSessions: "尚未发现会话",
    runtimeDefault: "运行时默认",
    scopeLabel: "范围",
    notSet: "未设置",
  },
} as const;
