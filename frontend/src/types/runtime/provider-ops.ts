/**
 * Provider 模型发现（provider ops）契约类型。
 *
 * 与后端 `internal/api/skills/provider_ops_handlers.go` 的 JSON 载荷一一对应，
 * 端点位于 `/api/runtime/providers/{fetch-models,auto-import,probe-models}`，
 * 与 aicli micro web client 的 `/web/api/config/providers/*` 同源（都复用
 * `internal/providerops`），因此字段口径保持一致。
 */

import type { ProviderAccountCache } from "./siteaccount";

/** 三类请求的公共载荷：未保存的 provider 只传 name + base_url，已保存的可只传 name。 */
export type ProviderOpsRequest = {
  name?: string;
  base_url?: string;
  api_key?: string;
  api_key_ref?: string;
  protocol?: string;
  auth_mode?: string;
  models_path?: string;
  headers?: Record<string, string>;
  timeout_seconds?: number;
};

export type ProviderAutoImportRequest = ProviderOpsRequest & {
  default_model?: string;
};

export type ProviderProbeRequest = ProviderOpsRequest & {
  models?: string[];
  protocols?: string[];
};

/** 单个模型的元数据匹配结果（fetch-models / auto-import 响应）。 */
export type ProviderModelMetadata = {
  id: string;
  name?: string;
  reasoning_model?: boolean;
  reasoning_efforts?: string[];
  default_reasoning_effort?: string;
  compact_reasoning_effort?: string;
  max_context_tokens?: number;
  max_tokens?: number;
};

/**
 * `model_capabilities.<model>` 条目：字段与后端 `agentconfig.ModelCapabilitySpec`
 * 对齐。auto-import 返回的是全量结构（字段没有 omitempty），未匹配字段会以 Go
 * 零值出现（`input_modalities: null`、`max_tokens: 0`、`native_tools: {..false}`），
 * 前端合并进草稿时按「非空值才覆盖」处理，避免零值抹平已保存的能力声明。
 */
export type ProviderModelCapabilitySpec = {
  input_modalities?: string[] | null;
  native_tools?: {
    image_generation?: boolean;
    images_generations_api?: boolean;
  } | null;
  reasoning_model?: boolean;
  replay_reasoning_content?: boolean | null;
  reasoning_efforts?: string[] | null;
  reasoning_effort_budgets?: Record<string, number> | null;
  default_reasoning_effort?: string;
  max_context_tokens?: number;
  max_tokens?: number;
  auto_compact_ratio?: number;
  auto_compact_token_limit?: number;
  auto_compact_mode?: string;
  supports_remote_compact?: boolean;
  compact_reasoning_effort?: string;
  [key: string]: unknown;
};

/** /models 清单里的单个模型（raw 字段不上行，后端已 `json:"-"`）。 */
export type ProviderModelInfo = {
  id: string;
  display_name?: string;
  input_modalities?: string[];
  reasoning_efforts?: string[];
  max_context_tokens?: number;
  supports_remote_codex?: boolean;
};

/** 按协议 / provider template 分类后的分组视图。 */
export type ProviderModelGroup = {
  protocol: string;
  login_protocol?: string;
  provider_template?: string;
  has_template: boolean;
  primary: boolean;
  models: string[];
  other_models?: string[];
};

export type ProviderModelClassification = {
  login_protocol?: string;
  runtime_protocol?: string;
  total_models: number;
  primary_model_ids: string[];
  verified_model_ids?: string[];
  assumed_model_ids?: string[];
  other_models_count?: number;
  groups?: ProviderModelGroup[];
};

/** 「获取模型列表」响应。model_ids 可直接合并进 supported_models。 */
export type ProviderModelsResult = {
  endpoint: string;
  status_code: number;
  verified_at?: string;
  anonymous_allowed?: boolean;
  model_ids: string[];
  all_model_ids?: string[];
  assumed_model_ids?: string[];
  models?: ProviderModelInfo[];
  login_protocol?: string;
  runtime_protocol?: string;
  classification?: ProviderModelClassification | null;
  metadata?: Record<string, ProviderModelMetadata>;
  warnings?: string[];
};

/**
 * 「自动导入」响应：一份可直接合并进当前编辑草稿的补丁，
 * runtime server 不在该接口内写配置（落盘走既有保存链路）。
 *
 * `model_capabilities` / `max_tokens_limit` 是后端按站点类型与模型清单匹配出的
 * 能力声明与上限，编辑表单没有对应字段，因此经草稿 `extraJson` 透传保存——
 * 丢弃它们会让新建 provider 完全没有能力声明，直到用户手工补写。
 */
export type ProviderAutoImportResult = {
  name: string;
  protocol: string;
  login_protocol?: string;
  base_url: string;
  api_path?: string;
  forward_url?: string;
  default_model?: string;
  supported_models?: string[];
  assumed_model_ids?: string[];
  support_types?: string[];
  site_type?: string;
  site_type_confidence?: string;
  site_type_scores?: Record<string, number>;
  max_tokens_limit?: number;
  model_capabilities?: Record<string, ProviderModelCapabilitySpec>;
  account?: ProviderAccountCache | null;
  warnings?: string[];
};

/** 单个模型 × 协议探测结论：ok=实测支持；unsupported=明确拒绝；error=无法定性。 */
export type ProviderProbeResultItem = {
  model_id: string;
  protocol: string;
  verdict: "ok" | "unsupported" | "error" | "invalid" | string;
  message?: string;
  status_code?: number;
  duration_ms?: number;
};

export type ProviderProbeResult = {
  results: ProviderProbeResultItem[];
};
