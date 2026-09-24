// 由 src/i18n/resources/en-US.ts 机械拆分而来（P0-6），仅搬迁不改语义。
import type { DeepStringShape } from "../../shape";
import type { zhRuntimeConfigEditorModes } from "../../zh-CN/runtime-config/editor-modes";

export const enRuntimeConfigEditorModes = {
  modes: {
    providers: {
      label: "Provider config",
      description: "Manage the main provider config with tables and popup forms.",
    },
    agentRouting: {
      label: "Agent difficulty routing",
      description:
        "Choose providers and models for subagents and teams at easy, normal, hard, and expert difficulty.",
    },
    providerGroups: {
      label: "Provider Groups",
      description: "Maintain routing groups, failover, truncation strategy, and member lists.",
    },
    networkProxy: {
      label: "Network proxy",
      description: "Maintain runtime upstream HTTP/HTTPS/SOCKS5 proxies and no_proxy.",
    },
    auth: {
      label: "Auth config",
      description: "Maintain JWT, admin, and Access Key authentication settings.",
    },
    routing: {
      label: "Routing config",
      description: "Maintain the routing root config and route ordering.",
    },
    rateLimit: {
      label: "Rate Limit",
      description: "Maintain root rate limiting, API key rules, and path overrides.",
    },
    resourceManager: {
      label: "Resource Manager",
      description:
        "Maintain the resource manager switch, default algorithms, health checks, and stats retention.",
    },
    providerQueue: {
      label: "Provider Queue",
      description:
        "Maintain provider-level slots, overflow strategy, heartbeat waits, and override rules.",
    },
    concurrency: {
      label: "Concurrency",
      description:
        "Maintain the global concurrency cap, queue parameters, and provider-level limits.",
    },
    retry: {
      label: "Retry",
      description: "Maintain the global retry default, enhancement strategy, and rule order.",
    },
    monitor: {
      label: "Monitor",
      description:
        "Maintain metrics, tracing, alert, pprof, and memory monitoring config.",
    },
    websocket: {
      label: "WebSocket",
      description:
        "Maintain responses / realtime WebSocket and bridge-related config.",
    },
    circuitBreaker: {
      label: "Circuit Breaker",
      description:
        "Maintain failure thresholds, time windows, and half-open recovery parameters.",
    },
    mcp: {
      label: "MCP management",
      description:
        "Manage MCP servers: inspect connection state and tool counts, add, edit, delete, enable/disable, and hot reload.",
    },
    transformer: {
      label: "Transformer",
      description:
        "Maintain HTTPTransformer switches and request/response body modifiers.",
    },
    profiles: {
      label: "Profiles",
      description:
        "Maintain profile.yaml per scenario: tools, skills, MCP, prompts and agents, with validation, preview and lifecycle actions.",
    },
    source: {
      label: "Raw YAML",
      description:
        "Keep comments, blank lines, and the original layout as the fallback editor mode.",
    },
  },
} satisfies DeepStringShape<typeof zhRuntimeConfigEditorModes>;
