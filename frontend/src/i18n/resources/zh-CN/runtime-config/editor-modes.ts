// 由 src/i18n/resources/zh-CN.ts 机械拆分而来（P0-6），仅搬迁不改语义。

export const zhRuntimeConfigEditorModes = {
  modes: {
    providers: {
      label: "Provider 配置",
      description: "用表格和弹出表单管理 provider 主配置。",
    },
    agentRouting: {
      label: "Agent 难度路由",
      description: "按 easy、normal、hard、expert 为子 Agent 和 Team 选择 provider 与模型。",
    },
    providerGroups: {
      label: "Provider Groups",
      description: "维护路由分组、故障切换、截断策略和成员列表。",
    },
    networkProxy: {
      label: "网络代理",
      description: "维护 runtime 上游 HTTP/HTTPS/SOCKS5 代理与 no_proxy。",
    },
    auth: {
      label: "Auth 配置",
      description: "维护 JWT、管理端和 Access Key 鉴权配置。",
    },
    routing: {
      label: "Routing 配置",
      description: "维护 routing 根配置和 routes 列表顺序。",
    },
    rateLimit: {
      label: "Rate Limit",
      description: "维护根限流、API Key 规则和路径级覆盖。",
    },
    resourceManager: {
      label: "Resource Manager",
      description: "维护资源管理开关、默认算法、健康检查和统计保留。",
    },
    providerQueue: {
      label: "Provider Queue",
      description: "维护 provider 级槽位、溢出策略、等待心跳和覆盖规则。",
    },
    concurrency: {
      label: "Concurrency",
      description: "维护全局并发上限、队列参数和 provider 级并发限制。",
    },
    retry: {
      label: "Retry",
      description: "维护全局重试默认值、增强策略和规则顺序。",
    },
    monitor: {
      label: "Monitor",
      description: "维护 metrics、tracing、alert、pprof 和 memory 监控配置。",
    },
    websocket: {
      label: "WebSocket",
      description: "维护 responses / realtime WebSocket 与 bridge 相关配置。",
    },
    circuitBreaker: {
      label: "Circuit Breaker",
      description: "维护熔断阈值、时间窗口和半开恢复参数。",
    },
    mcp: {
      label: "MCP 管理",
      description:
        "管理 MCP Server：查看连接状态与工具数，支持新增、编辑、删除、启用/停用与热重载。",
    },
    transformer: {
      label: "Transformer",
      description: "维护 HTTPTransformer 开关和 request/response body modifier。",
    },
    source: {
      label: "原始 YAML",
      description: "保留注释、空行和原始排版，作为兜底编辑模式。",
    },
  },
} as const;
