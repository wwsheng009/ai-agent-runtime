// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。


// The page combines runtimeConfig and common namespace translators; keep this
// boundary intentionally loose while the individual editor components remain typed.
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export type Translator = any;
export type EditorMode =
  | "providers"
  | "providerGroups"
  | "networkProxy"
  | "routing"
  | "rateLimit"
  | "resourceManager"
  | "providerQueue"
  | "concurrency"
  | "retry"
  | "monitor"
  | "websocket"
  | "circuitBreaker"
  | "transformer"
  | "auth"
  | "agentRouting"
  | "mcp"
  | "profiles"
  | "source";
