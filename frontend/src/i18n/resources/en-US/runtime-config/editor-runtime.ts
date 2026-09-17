// 由 src/i18n/resources/en-US.ts 机械拆分而来（P0-6），仅搬迁不改语义。
import type { DeepStringShape } from "../../shape";
import type { zhRuntimeConfigEditorRuntime } from "../../zh-CN/runtime-config/editor-runtime";

export const enRuntimeConfigEditorRuntime = {
  auth: {
    title: "Auth config",
    description:
      "Maintain JWT, admin, and Access Key authentication fields with a dedicated form.",
    enable: "Enable",
    badges: {
      adminOn: "Admin auth on",
      adminOff: "Admin auth off",
      accessOn: "Access auth on",
      accessOff: "Access auth off",
      anonymousAllowed: "Anonymous allowed",
      anonymousDenied: "Anonymous denied",
    },
    jwtSession: {
      title: "JWT and session",
      description:
        "These fields are often overridden by startup environment variables; use them to inspect defaults and fallbacks.",
    },
    fields: {
      jwtSecretDescription: "Prefer injecting AUTH_JWT_SECRET in production.",
      accessKeySecretDescription:
        "Used for Access Key secret derivation and recovery.",
      jwtSecret: "JWT secret",
      accessKeySecret: "Access Key secret",
      jwtExpire: "JWT expiry",
      sessionTimeout: "Session timeout",
      maxApiCreateTimes: "Max API create times",
      adminToken: "Admin token",
      allowAnonymous: "Allow anonymous access",
    },
    placeholders: {
      jwtExpire: "24h",
      sessionTimeout: "30m",
    },
    admin: {
      title: "Admin authentication",
      description: "JWT and static token settings for `/admin/*`.",
      tokenDescription:
        "Static admin token, usually intended only for internal environments.",
    },
    access: {
      title: "Access authentication",
      description:
        "Access-key authentication switch and anonymous access policy for `/v1/*`.",
      allowAnonymousDescription:
        "When disabled, requests without an Access Key are rejected immediately.",
    },
  },
  circuitBreaker: {
    title: "Circuit breaker config",
    description:
      "Maintain failure thresholds, failure rate, time window, and half-open probe settings to avoid cascading failures.",
    fields: {
      failureThreshold: "Failure threshold",
      failureRate: "Failure rate",
      sampleThreshold: "Sample threshold",
      windowDuration: "Window duration",
      openTimeout: "Open timeout",
      halfOpenMaxCalls: "Half-open max calls",
    },
    failure: {
      title: "Failure criteria",
      description: "Decide when the circuit opens.",
    },
    recovery: {
      title: "Timing and recovery",
      description:
        "Control the sliding window, open duration, and half-open probe count.",
    },
  },
  websocket: {
    title: "WebSocket config",
    description:
      "Maintain core settings for Responses WS passthrough, HTTP bridge, and Realtime WS.",
    enable: "Enable",
    enableIngress: "Enable ingress",
    fields: {
      maxActiveConnections: "Max active connections",
      affinityTtl: "Affinity TTL",
      handshakeMaxRetries: "Handshake max retries",
      compatBridgeSourceProtocols: "Compat bridge source protocols",
      httpBridgeEnabled: "Enable HTTP bridge",
      compatBridgeEnabled: "Enable compat bridge",
      allowPassthroughOnly: "Allow passthrough only",
      metricsEnabled: "Enable metrics",
      closeCodeLabelsEnabled: "Enable close code labels",
      connectionPoolingEnabled: "Enable connection pooling",
      preFirstEventRetryOnce: "Retry once before first event",
      failoverOnHandshakeError: "Failover on handshake error",
    },
    badges: {
      enabledOn: "websocket on",
      enabledOff: "websocket off",
      responsesOn: "responses on",
      responsesOff: "responses off",
      realtimeOn: "realtime on",
      realtimeOff: "realtime off",
    },
    master: {
      title: "Master switch",
      description: "Controls whether the websocket module is enabled overall.",
    },
    responses: {
      title: "Responses WS",
      description:
        "Manage `/v1/responses` passthrough, HTTP bridge, and connection pool behavior.",
      compatProtocolsDescription:
        "Enter multiple source protocols separated by newlines or commas.",
    },
    realtime: {
      title: "Realtime WS",
      description:
        "Manage `/v1/realtime` ingress, capacity, and handshake failure handling.",
    },
  },
  concurrency: {
    title: "Concurrency config",
    description:
      "Maintain the global concurrency cap, queue settings, and provider-level concurrency limits.",
    enabled: "Enabled",
    disabled: "Disabled",
    enabledHint: "Current thresholds remain in the config even when disabled.",
    fields: {
      enabled: "Enable concurrency control",
      maxConcurrentRequests: "Max concurrent requests",
      queueSize: "Queue size",
      queueTimeout: "Queue timeout",
      provider: "Provider",
      limit: "Concurrency limit",
    },
    placeholders: {
      queueTimeout: "5s",
      provider: "nvidia",
    },
    badges: {
      enabledOn: "Concurrency on",
      enabledOff: "Concurrency off",
      providerLimitCount: "{{count}} provider limits",
      limitTotal: "Limit total {{total}}",
    },
    table: {
      title: "Per Provider Limits",
      description:
        "Maintain independent concurrency caps by provider name, commonly used to separate upstream capacity.",
      empty: "There are no provider-level concurrency limits yet.",
      limitCount: "{{count}} limits",
      globalLimit: "Global {{value}}",
      noGlobalLimit: "No global limit",
      columns: {
        provider: "Provider",
        limit: "Limit",
        actions: "Actions",
      },
    },
    actions: {
      create: "New provider limit",
      edit: "Edit",
      delete: "Delete",
    },
    dialog: {
      createTitle: "New provider concurrency limit",
      editTitle: "Edit {{name}}",
      description:
        "Maintain a single provider concurrency threshold in per_provider_limits.",
      save: "Save draft",
    },
  },
  monitor: {
    title: "Monitor config",
    description:
      "Maintain metrics, tracing, alert, pprof, and memory monitoring settings.",
    enable: "Enable",
    fields: {
      metricsPath: "Metrics path",
      metricsAggregation: "Enable metrics aggregation",
      tracingSampler: "Sampler",
      tracingExporter: "Exporter",
      tracingServerAddr: "OTLP server address",
      alertWebhookUrl: "Webhook URL",
      alertChannels: "Alert channels",
      alertMinThreshold: "Min alert threshold",
      alertSeverity: "Alert severity",
      pprofListenAddr: "Listen address",
      pprofGcInterval: "GC interval",
      memorySampleInterval: "Sample interval",
      memoryAlertThresholdMb: "Alert threshold (MB)",
      memoryLeakThresholdPercent: "Leak threshold (%)",
    },
    badges: {
      enabledOn: "monitor on",
      enabledOff: "monitor off",
      metricsOn: "metrics on",
      metricsOff: "metrics off",
      tracingOn: "tracing on",
      tracingOff: "tracing off",
      alertOn: "alert on",
      alertOff: "alert off",
    },
    master: {
      title: "Master switch",
      description: "Controls whether the monitoring module is enabled overall.",
    },
    metrics: {
      title: "Metrics",
      description: "Prometheus metric export and aggregation controls.",
      aggregationDescription: "Whether metric aggregation is enabled.",
    },
    tracing: {
      title: "Tracing",
      description: "Sampling, exporter, and OTLP server address.",
    },
    alert: {
      title: "Alert",
      description: "Alert webhook, channels, threshold, and severity.",
      channelsDescription:
        "Enter multiple channels separated by newlines or commas.",
    },
    pprof: {
      title: "PProf",
      description: "pprof profiling and GC interval controls.",
    },
    memory: {
      title: "Memory",
      description: "Memory sampling frequency, thresholds, and leak detection.",
    },
  },
  agentMaxSteps: {
    title: "Agent execution policy",
    description:
      "Maintains agent.maxSteps in the runtime config: the server default used when a request omits max_steps (0 = no limit). This card writes to disk directly and is not part of the draft flow below.",
    fieldLabel: "Server default max steps",
    currentDefault: "Server default: {{count}}.",
    loading: "Loading…",
    unknown: "Server default not loaded yet.",
    limitHint: "Range 0–{{limit}}; 0 means no limit.",
    save: "Save server default",
    saving: "Saving…",
    saved: "Saved: server default {{count}}, written to {{path}}.",
    saveFailed: "Save failed: {{message}}",
    loadFailed: "Failed to load the server default: {{message}}",
    configFile: "Config file: {{path}}",
    layerSummary: "Layers: {{layers}}",
    layerReadOnly: "read-only default (never written)",
    layerWritable: "write target",
    layerCandidate: "candidate (not created yet)",
    workspaceValue:
      "Workspace per-turn value: {{count}} (chat requests send it and override the server default; both share the same agent.maxSteps key).",
    scopeNote: "Source: agent.maxSteps in the runtime config file.",
  },
} satisfies DeepStringShape<typeof zhRuntimeConfigEditorRuntime>;
