export const zhCN = {
  common: {
    actions: {
      close: "关闭",
      cancel: "取消",
      confirm: "确认",
      reset: "恢复默认",
      refresh: "刷新",
      copied: "已复制",
      copyLink: "复制链接",
      clearAll: "清除全部",
      clearSearch: "清除搜索",
      copyValue: "复制值",
      copyMetadata: "复制元数据",
      copyPreview: "复制预览",
      copyFields: "复制字段",
      copyJson: "复制 JSON",
    },
    loading: {
      page: "正在加载页面...",
      details: "正在加载详情...",
      logs: "正在加载日志...",
    },
    states: {
      justNow: "刚刚",
      none: "无",
      live: "在线",
      connecting: "连接中",
      reconnecting: "重新连接中",
      streamError: "流错误",
      idle: "空闲",
      fileDetected: "已检测到文件",
      waitingForLogFile: "等待日志文件",
      enabled: "已启用",
      disabled: "已关闭",
    },
  },
  landing: {
    header: {
      productLabel: "Agent 工作区平台",
      productName: "AI Agent Runtime",
      productTour: "产品导览",
      openWorkspace: "打开工作台",
    },
    hero: {
      eyebrow: "用于研究、执行和审阅的浏览器原生 AI 工作区",
      rotatingWord1: "深度研究",
      rotatingWord2: "Agent 工作区",
      rotatingWord3: "工件审阅",
      rotatingWord4: "运行时团队",
      titlePrefix: "研究、编排并交付",
      titleSuffix: "都在一个浏览器工作区里完成。",
      body:
        "AI Agent Runtime 将实时线程、运行时事件、工件证据和团队协作放在同一个产品界面中。",
      primaryCta: "进入工作台",
      secondaryCta: "查看产品亮点",
      unifiedFlowTitle: "统一流程",
      unifiedFlowBody:
        "从提示到动作再到审阅，始终在同一条路径里完成，不必在多个工具和隐藏执行层之间来回切换。",
      teamReadyTitle: "面向团队的工作区",
      teamReadyBody:
        "把线程上下文、运行时团队和操作细节保留在同一个壳层里，让交接保持清晰可读。",
      verifiableOutputTitle: "可验证输出",
      verifiableOutputBody:
        "检查工件、跟踪运行时事件，并把证据和产生它的工作放在一起。",
      snapshotEyebrow: "产品快照",
      snapshotTitle: "一个外壳，三个可见层",
      snapshotBody:
        "让发现、活跃工作和运行时证据同时保持清晰：前面是产品叙事，中间是当前线程，周围是操作细节。",
      productSiteTitle: "产品站点",
      productSiteBody:
        "清晰的产品叙事会说明工作区做什么、适合什么场景，以及团队为什么可以信任输出。",
      workspaceEntryTitle: "工作区入口",
      workspaceEntryBody:
        "从 /workspace 直接进入活跃线程，让应用显得即时，而不是靠路由跳转拼起来的。",
      runtimeEvidenceTitle: "运行时证据",
      runtimeEvidenceBody1: "聊天轮次和回复会始终附着在线程上。",
      runtimeEvidenceBody2: "历史同步让工作区和会话保持一致。",
      runtimeEvidenceBody3: "运行时流会暴露工具调用、路由和工件。",
    },
    deferred: {
      productHighlights: "产品亮点",
    },
    community: {
      eyebrow: "社区",
      title: "把团队、证据和运行时控制放进同一条流程",
      subtitle:
        "AI Agent Runtime 面向需要产品级工作区、同时又不能丢失操作细节的团队。首页负责讲清流程，工作区负责把它带到执行现场。",
      pillarsBadge: "产品支柱",
      layersBadge: "三个可见层",
      pillars: {
        focusedWork: {
          title: "聚焦工作",
          summary:
            "先拿到清晰线程，再只收集真正相关的上下文，让任务持续推进而不丢失原始请求。",
        },
        sharedVisibility: {
          title: "共享可见性",
          summary:
            "把队友、运行时事件和任务调度放在同一个操作界面里，而不是散落在多个标签页中。",
        },
        inspectableResults: {
          title: "结果可审阅",
          summary:
            "审阅工件、回执和流式执行细节时，始终保留解释这些结果为何产生的线程上下文。",
        },
      },
      readyBadge: "准备开始",
      readyTitle: "打开工作区，把从请求到审阅的完整线程一路带过去。",
      readyPoint1:
        "在同一个操作界面里完成提示、运行时上下文、团队协作和工件审阅。",
      readyPoint2:
        "先看产品导览，再直接进入活跃工作区开始动作。",
      launchWorkspace: "启动工作区",
      browseHighlights: "浏览产品亮点",
    },
    caseStudy: {
      eyebrow: "产品亮点",
      title: "看工作区如何把 Agent 工作变成可审阅的内容",
      subtitle:
        "每张卡片都对应 AI Agent Runtime 的真实表面：活跃线程、运行时团队、流式执行，以及始终附着在工作上的工件细节。",
      exploreInWorkspace: "在工作区中查看",
      cards: {
        liveExecution: {
          label: "实时执行",
          title: "在一个工作区里跟完整轮次生命周期",
          description:
            "从提交到完成跟踪一次实时 Agent 回合，同时会话历史和运行时事件持续回灌到同一条线程。",
        },
        teamCoordination: {
          label: "团队协同",
          title: "从同一条控制轨协调多个运行时队友",
          description:
            "多团队摘要、队友准备状态和调度细节都保留在工作线程里，不需要切走页面。",
        },
        artifactDetail: {
          label: "工件细节",
          title: "像产品界面那样审阅输出，而不是看 JSON 泉涌",
          description:
            "在源码和预览之间切换时不丢线程，让证据和输出始终贴近它们产生时的消息。",
        },
        agentReasoning: {
          label: "Agent 推理",
          title: "看助手如何从规划走到执行",
          description:
            "把规划、路由、编排和工具事件映射为可读的消息流，并附上回执。",
        },
        operationalClarity: {
          label: "操作清晰度",
          title: "保持运行时可读，而不是把底层系统藏起来",
          description:
            "聊天、会话和运行时能力都通过明确的操作控制暴露出来，前端表面保持清爽。",
        },
        productExperience: {
          label: "产品体验",
          title: "让网站和工作区保持视觉上的连续",
          description:
            "把首页和工作区当作相连的两个表面：前者解释价值，后者让团队立即行动。",
        },
      },
    },
    sandbox: {
      eyebrow: "运行时环境",
      title: "每次 Agent 回合背后都有真实执行环境",
      subtitle:
        "Agent 需要文件、命令、会话状态和运行时反馈才能真正做事。AI Agent Runtime 把这些表面都显式暴露出来，让团队可以直接检查发生了什么，而不是事后猜测。",
      terminalLabel: "运行时终端",
      featuresLabel: "运行时能力",
      featuresTitle: "线程、工具、工件和团队都有明确的控制面。",
      featuresBody1:
        "这个工作区的目标是操作感，而不是装饰感。你可以在同一个地方发送回合、查看历史、跟踪运行时事件和审阅输出。",
      featuresBody2:
        "需要曝光的 runtime API 仍然可见，但产品表面足够克制，适合工程师、操作者和审阅者日常使用。",
      surfacesLabel: "可用表面",
      tags: {
        shellAccess: "Shell 访问",
        workspaceFiles: "工作区文件",
        sessionHistory: "会话历史",
        runtimeSse: "Runtime SSE",
        artifactPreview: "工件预览",
      },
    },
    skills: {
      eyebrow: "核心能力",
      title: "在 Agent 工作时仍然保持可见的能力",
      subtitle:
        "AI Agent Runtime 让工作流从理解、协同到交付的每一步都保持清晰。线程、辅助上下文和运行时证据都放在同一个工作区里。",
      ladderLabel: "能力阶梯",
      stageLabel: "阶段 {{index}}",
      columns: {
        understand: {
          title: "理解",
          description:
            "先把线程、仓库和相关证据找出来，让工作区聚焦在任务本身，而不是把原始上下文随处堆满。",
          items: ["仓库扫描", "定向读取", "证据优先上下文"],
        },
        coordinate: {
          title: "协同",
          description:
            "让正在执行的任务、队友状态和运行时信号保持一致，这样整个团队都能看到阻塞、运行中和待关注的部分。",
          items: ["线程规划", "团队交接", "运行时检查点"],
        },
        deliver: {
          title: "交付",
          description:
            "在同一个工作区里编辑、审阅和验证，让工件和执行细节始终贴着产生它们的消息。",
          items: ["代码修改", "工件预览", "验证循环"],
        },
      },
    },
    whatsNew: {
      eyebrow: "为什么它能工作",
      title: "从提示词到可验证输出，需要的东西都在这里",
      subtitle:
        "首页现在用用户语言解释产品：如何进入工作区、执行如何保持可见，以及工件如何始终和产生它的线程绑定。",
      cards: {
        productShell: {
          label: "产品外壳",
          title: "为第一次理解而组织",
          description:
            "Hero、亮点、能力、运行时细节和 CTA 的顺序，能让用户在进入工作区之前先理解产品。",
        },
        workspaceEntry: {
          label: "工作区入口",
          title: "更直接地进入活跃工作",
          description:
            "主 CTA 现在直接进入 /workspace，减少路由噪音，让用户更快进入实际工作界面。",
        },
        runtimeSignal: {
          label: "运行时",
          title: "实时信号始终附着在线程上",
          description:
            "提交消息、同步会话历史和运行时流式传输，会让线程在工作进行时一直保持最新。",
        },
        artifactEvidence: {
          label: "工件",
          title: "证据始终可审阅",
          description:
            "规划、编排、路由、子 agent 和工具 payload 都会回写到可审阅的工件和回执中。",
        },
        conversation: {
          label: "对话",
          title: "以线程为中心的工作流",
          description:
            "消息表面会持续聚焦当前线程，而不是把执行状态散落到不同屏幕上。",
        },
        extension: {
          label: "扩展",
          title: "可以随运行时继续成长",
          description:
            "这套结构为更丰富的 markdown、更深的工件审阅和更复杂的团队控制预留了空间，但不会改变主流程。",
        },
      },
    },
    footer: {
      quote: "“保持线程清晰、运行时可见、输出可审阅。”",
      body:
        "AI Agent Runtime 为团队提供进入活跃工作的产品级入口，把线程、运行时事件和工件放在足够近的位置，支持真实审阅和交接。",
      openWorkspace: "打开工作区",
      viewProductHighlights: "查看产品亮点",
    },
  },
  workspace: {
    shell: {
      newChatEyebrow: "新工作区聊天",
      newChatTitle: "从一个空白线程开始，让运行时状态在工作推进时附着上来。",
      newChatBody: "这个路由现在是真实的新聊天入口，不再依赖预置的 mock 线程。",
      loadingSettingsPanel: "正在加载设置面板…",
      loadingArtifactPanel: "正在加载 artifact 面板…",
      loadingArtifactDetails: "正在加载 artifact 详情…",
    },
    topbar: {
      home: "首页",
      newChat: "新建聊天",
      logs: "日志",
      runtime: "Runtime",
      settings: "设置",
      showFiles: "显示文件",
      hideFiles: "隐藏文件",
      newThreadTitle: "新建聊天",
      newThreadSubtitle: "先开始一个线程，再让运行时状态在工作推进时附着上来。",
      threadTransport: {
        live: "在线运行时",
        error: "运行时降级",
        seeded: "预置预览",
      },
      threadStatus: {
        sessionAttached: "已附着会话",
        previewThread: "预览线程",
        newThread: "新线程",
      },
      subtitle: {
        needsRestoreWithSession: "会话 {{sessionId}} 需要恢复关注",
        needsRestore: "运行时恢复需要关注",
        viaSource: "{{transportLabel}} via {{source}}",
        session: "会话 {{sessionId}}",
      },
    },
    sidebar: {
      workspaceLabel: "Workspace",
      appName: "AI Agent Runtime",
      refreshRuntimeTeams: "刷新运行时团队",
      openSettings: "打开设置",
      startNewChat: "新建聊天",
      searchPlaceholder: "搜索线程",
      sections: {
        chats: "本地聊天",
        sessions: "会话",
        runtime: "运行时概览",
      },
      threadStatuses: {
        review: "等待复核",
        draft: "草稿线程",
        active: "活跃线程",
      },
      sessionStatuses: {
        error: "会话同步异常",
        restored: "已恢复会话",
        attached: "已附着运行时会话",
        pending: "尚未附着运行时会话",
      },
      sessionDetails: {
        pending: "尚未附着运行时会话。",
        error: "会话已存在，但最新同步失败，需要再次尝试恢复。",
        restored: "已从运行时会话历史中恢复，可继续推进。",
        attached: "已附着到当前工作区流程中的运行时会话。",
      },
      emptyChats: {
        search: "当前搜索条件下没有匹配的本地聊天。",
        default: "在运行时会话附着之前，本地聊天会显示在这里。",
      },
      emptySessions: {
        search: "当前搜索条件下没有匹配的会话。",
        default: "可恢复的运行时会话会在加载后显示在这里。",
      },
      runtimeStats: {
        sessions: "{{count}} 个会话",
        recoverable: "{{count}} 个可恢复",
        pending: "{{count}} 个待附着",
        syncing: "同步中",
      },
      runtimeTeamsUnavailable: "暂无可用 runtime 团队。",
      openRuntimeTeamDetails: "打开运行时团队详情",
      backendConfigPage: "后端配置页",
      active: "{{count}} 个活跃",
      unknown: "未知",
    },
    composer: {
      transport: {
        live: "在线运行时",
        error: "运行时错误",
        seeded: "预置",
      },
      sessionState: {
        attached: "已附着会话",
        new: "新会话",
      },
      placeholder: {
        newThread: "让工作区去检查、构建、审阅或协调下一步……",
        thread: "让工作区去研究、修改、验证或协调下一步……",
      },
      submit: {
        stopResponse: "停止响应",
        startNewThread: "开始新线程",
        sendTurn: "发送回合",
        startThread: "开始线程",
      },
      promptTips: "提示建议",
      promptTipsMenuTitle: "提示建议",
      responseActive: "响应中",
      provider: "Provider",
      model: "Model",
      loadingModels: "正在加载模型",
      modelCatalogUnavailable: "模型目录不可用",
      runtimeDefaultModel: "运行时默认模型",
      modelWithName: "模型 {{model}}",
      shortcuts: "Ctrl/Cmd + Enter",
      stop: "停止",
      submitShort: "提交",
      filesCount: "{{count}} 个文件",
    },
  },
  runtimeConfig: {
    page: {
      badge: "Runtime config",
      independentPage: "独立页面",
      title: "后端配置工作台",
      description: "独立处理 runtime 后端配置，并为 provider 提供专门入口。",
      backToWorkspace: "返回工作台",
      logs: "日志",
    },
  },
  settings: {
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
        "用于限制单轮里最多允许的规划 / 路由 / 工具执行步数。",
      currentMaxSteps: "当前值为 {{count}}。",
      maxStepsAdvice:
        "通常 8 到 12 足够覆盖常见工作区任务，复杂编排可以提高到 15 到 20。",
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
      selectedProvider: "当前 provider",
      selectedModel: "当前 model",
      sessionCount: "会话数",
      recoverableSessions: "可恢复会话",
      activeTeams: "运行中团队",
      activeTeamsSummary: "共加载 {{count}} 个团队摘要",
      sessionBreakdown: "{{active}} 在线 / {{archived}} 已归档",
      latestUpdated: "最近更新 {{time}}",
      noSessions: "尚未发现会话",
      runtimeDefault: "运行时默认",
      scopeLabel: "范围",
      notSet: "未设置",
    },
  },
  logs: {
    title: "实时日志",
    subtitle: "Tail + Stream",
    backToWorkspace: "返回工作台",
    home: "首页",
    searchPlaceholder:
      "搜索 request_id、trace_id、session_id、message、provider、raw JSON",
    levelLabel: "级别",
    allLevels: "全部",
    tokenLabel: "Token",
    tokenPlaceholder: "远程部署时可填写",
    refresh: "刷新",
    copyLink: "复制链接",
    followLatest: "Follow 最新",
    fileLabel: "日志文件:",
    fileFallback: "尚未配置 runtime log file_path",
    streamHint: "流连接提示:",
    loadError: "加载失败:",
    currentView: "当前视图",
    clearAll: "清除全部",
    showOnly: "只看",
    listTitle: "日志列表",
    listSubtitle: "以最小宽度完成扫描和定位。",
    loading: "加载中",
    entries: "条",
    time: "时间",
    levelShort: "Lv",
    event: "事件",
    readingLogs: "正在读取日志...",
    logLoadFailed: "日志加载失败",
    noLogs: "当前筛选条件下没有可展示的日志。",
    detailsTitle: "日志详情",
    detailsDescription: "这里承担长文本、JSON 和错误体阅读。",
    detailsLoading: "正在加载日志详情...",
    selectPrompt: "从左侧选择一条日志查看详细字段。",
    copiedLog: "已复制日志",
    identifiers: "标识",
    identifiersHelp: "复制标识，或把当前值直接写回顶部搜索框继续追踪。",
    clearSearch: "清除搜索",
    filterSameValue: "过滤同值",
    cancelFilter: "取消过滤",
    metadata: "元数据",
    responsePreview: "响应预览",
    extraFields: "额外字段",
    rawJson: "原始 JSON",
    connectionLive: "在线",
    connectionConnecting: "连接中",
    connectionReconnecting: "重新连接中",
    connectionError: "流错误",
    connectionIdle: "空闲",
    identifierRequest: "请求 ID",
    identifierTrace: "Trace ID",
    identifierSession: "Session ID",
    levelError: "错误",
    levelWarn: "警告",
    levelInfo: "信息",
    levelDebug: "调试",
    levelOther: "其他",
    levelShortError: "ERR",
    levelShortWarn: "WRN",
    levelShortInfo: "INF",
    levelShortDebug: "DBG",
    levelShortOther: "LOG",
    chipQuery: "搜索",
    chipLevel: "级别",
    chipFollow: "跟随",
    chipCursor: "Cursor",
    activeChipOff: "关闭",
    copied: "已复制",
    cursorLabel: "游标",
    levelFallback: "日志",
    requestPrefix: "请求",
    runtimeLogFallback: "运行时日志",
    statusPrefix: "状态",
    timestamp: "时间戳",
    level: "级别",
    module: "模块",
    caller: "调用方",
    requestId: "Request ID",
    traceId: "Trace ID",
    sessionId: "Session ID",
    provider: "Provider",
    model: "Model",
    method: "方法",
    url: "URL",
    responseStatus: "响应状态",
    upstreamError: "上游错误",
  },
} as const;
