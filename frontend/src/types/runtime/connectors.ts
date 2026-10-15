// IM 连接器（bots）对外契约类型。
//
// 唯一来源是 backend/internal/connectors/api 的 Schema JSON 与
// backend/internal/connectors/restapi 的视图（docs/bots/07-config-ux.md §7：
// 三端共用同一份 Schema，前端只做「字段 → 控件」的机械映射）。
// 密钥只回字段名（secret_keys），永不回明文。

/** 已知控件类型（未知值保留原样，渲染器降级为文本框）。 */
export type ConnectorFieldType =
  | "text"
  | "secret"
  | "url"
  | "bool"
  | "enum"
  | "number";

export interface ConnectorFieldOption {
  value: string;
  label: string;
}

export interface ConnectorFieldSchema {
  key: string;
  label: string;
  /** 后端约定的类型字符串；未知类型由渲染器降级处理。 */
  type: string;
  required?: boolean;
  placeholder?: string;
  help?: string;
  default?: string;
  options?: ConnectorFieldOption[];
}

/** 通道特有动作（扫码等）；P0 只透传展示，不触发。 */
export interface ConnectorActionSpec {
  key: string;
  label: string;
  kind: string;
}

export interface ConnectorProviderSchema {
  provider: string;
  label: string;
  modes?: string[];
  fields: ConnectorFieldSchema[];
  actions?: ConnectorActionSpec[];
}

/** GET /api/connectors/schema 的载荷（三端渲染器的唯一输入）。 */
export interface ConnectorSchema {
  version: string;
  providers: ConnectorProviderSchema[];
}

export interface ConnectorPolicyView {
  command_prefix?: string;
  allowed_users?: string[];
  session_mode?: string;
  reply_mode?: string;
  runtime_owner?: string;
  /** D6 连接器级模型路由；缺省（未配置）= 回落宿主全局默认（additive）。 */
  route?: ConnectorRoutePolicyView;
  /** D5 事件通知接收；缺省（未配置）= 不收跨会话事件（additive）。 */
  notify?: ConnectorNotifyPolicyView;
  /**
   * S1a 内容镜像（docs/bots/13 §8）：本地来源回合（TUI/Web/API/系统）的终态
   * 正文是否作为副本投递到该 bot 的绑定 chats；缺省关（additive）。
   */
  content_mirror?: boolean;
  /**
   * S2 声明式事件订阅（docs/bots/13 §6.1）：缺省（未配置）= 回退
   * notify/content_mirror 旧语义（双读单写，additive）。
   */
  events?: ConnectorEventsPolicyView;
}

/** S2 声明式事件订阅容器（enabled 且有订阅时生效）。 */
export interface ConnectorEventsPolicyView {
  enabled: boolean;
  subscriptions?: ConnectorEventSubscriptionView[];
}

/** 一条订阅：会话选择器 × 事件类型（支持 "turn.*" 族通配）× 投递形态。 */
export interface ConnectorEventSubscriptionView {
  id?: string;
  /** S4：单条订阅启停；缺省（省略）= 启用，false 显式停用。 */
  enabled?: boolean;
  /** 会话范围：bound（缺省）| ids | all。 */
  sessions?: { mode?: "bound" | "ids" | "all"; ids?: string[] };
  /** 事件类型：规范名/别名/族通配（如 assistant_message、approval、"turn.*"）。 */
  types?: string[];
  /** 投递形态：notice（缺省）| content | digest（completion 聚合）| stream（assistant_delta 流式编辑）。 */
  deliver?: "notice" | "content" | "digest" | "stream";
}

/** 连接器运行日志级别（后端 logs.go LogLevel*；未知值原样保留，渲染器降级）。 */
export type ConnectorLogLevel = "debug" | "info" | "warn" | "error";

/** 连接器运行日志方向：in=入站、out=出站、system=生命周期/归属。 */
export type ConnectorLogDirection = "in" | "out" | "system";

/**
 * 每连接器的累计运行计数与最近错误（进程内，重启清零；与
 * GET /api/connectors/{id}/logs 载荷中的 stats 同构）。
 */
export interface ConnectorStats {
  /** 入站消息成功入队数（白名单拒绝/队列满不计）。 */
  inbound: number;
  /** 入站消息处理完成后成功投递的回复数。 */
  outbound: number;
  /** error 级事件数（后端失败、投递失败、队列满等）。 */
  errors: number;
  /** 重复投递被幂等账本跳过的次数。 */
  dedup_skipped: number;
  /** 会话队列满被拒绝的次数。 */
  queue_full: number;
  /** 最近一次 error 级事件摘要；空串/缺省 = 尚无错误。 */
  last_error?: string;
  /** RFC3339；后端零值（0001-01-01T00:00:00Z）在前端归一为空串。 */
  last_error_at: string;
  /** 最近一次入/出站事件时间（RFC3339，同上归一）。 */
  last_activity_at: string;
}

/** GET /api/connectors/{id}/logs 的一条运行日志（summary 由后端拼装并脱敏）。 */
export interface ConnectorLogEntry {
  /** RFC3339；无有效时间的条目由归一化丢弃。 */
  time: string;
  /** 已知值见 ConnectorLogLevel；未知值原样保留。 */
  level: string;
  /** 已知值见 ConnectorLogDirection；未知值原样保留。 */
  direction: string;
  connector_id: string;
  /** 平台会话标识（连接器级系统事件可为空）。 */
  chat?: string;
  summary: string;
}

/** GET /api/connectors/{id}/logs 的载荷（entries 倒序，最新在前）。 */
export interface ConnectorLogsPayload {
  entries: ConnectorLogEntry[];
  stats: ConnectorStats;
}

/** 连接器运行可观测快照（ConnectorView.runtime，additive）。 */
export interface ConnectorRuntimeView {
  stats: ConnectorStats;
}

export interface ConnectorView {
  id: string;
  provider: string;
  name: string;
  enabled: boolean;
  mode: string;
  settings?: Record<string, string>;
  /** 已配置的密钥字段名（无明文）。 */
  secret_keys?: string[];
  policy: ConnectorPolicyView;
  /**
   * 当前运行归属的宿主（空 = 未认领）。additive 字段：旧后端可缺省，
   * 「抢占」（POST /api/connectors/{id}/takeover）成功后就地刷新。
   */
  active_owner?: string;
  /** 归属是否属于本宿主（后端按 HostID 算好，前端只负责展示/高亮）。 */
  owned_by_current_host?: boolean;
  /**
   * 运行可观测快照（累计计数 + 最近错误）。additive 字段：旧后端可缺省，
   * 缺失时页面不展示计数（日志明细走 GET /{id}/logs）。
   */
  runtime?: ConnectorRuntimeView;
}

/**
 * 连接器 ↔ 会话绑定（docs/bots/09-multi-instance-binding.md §5.2）。
 * connector 摘要供 UI 直接渲染；additive：旧后端可缺省。
 */
export interface ConnectorBindingView {
  connector_id: string;
  chat: string;
  session_id: string;
  node_id?: string;
  updated_at?: string;
  connector?: {
    id: string;
    name: string;
    provider: string;
    enabled: boolean;
    active_owner?: string;
  };
}

/** 连接器策略里的事件通知接收配置（docs/bots/09 §7.4.1 D5）。 */
export interface ConnectorNotifyPolicyView {
  enabled: boolean;
  kinds?: string[];
  scope?: string;
}

/** 连接器策略里的模型路由配置（docs/bots/09 §7.4.2 D6）。 */
export interface ConnectorRoutePolicyView {
  provider?: string;
  model?: string;
  reasoning_effort?: string;
}

export interface ConnectorFieldError {
  key: string;
  message: string;
}

/**
 * 通道动作（POST /api/connectors/{id}/actions/{action}）返回的载荷。
 *
 * 微信扫码登录的两个动作共用一份形状：
 *   start_login   → login_id/base_url/qr_code/qr_code_content/expires_at；
 *   login_status  → 同上 + status(wait|scanned|confirmed|expired)；confirmed
 *                   且 credential_ready=true 时附带 settings/secrets 回填补丁。
 * 未知通道动作的字段透传在 `raw`（通用展示/排障）。
 */
export interface ConnectorActionPayload {
  login_id?: string;
  /** 归一后的登录状态：wait | scanned | confirmed | expired。 */
  status?: string;
  base_url?: string;
  qr_code?: string;
  qr_code_content?: string;
  expires_at?: string;
  /** 二维码过期后 Provider 自动换新的次数。 */
  refresh_count?: number;
  account_id?: string;
  user_id?: string;
  /** confirmed 且凭据齐备时为 true（此时才有 settings/secrets）。 */
  credential_ready?: boolean;
  /**
   * 人读摘要（通用字段：kind=button 的动作结果优先按它展示，
   * 如 QQ「授权校验」返回「校验通过：Bot 名（AppID …）」）。
   */
  message?: string;
  /** 回填补丁：合并进连接器 settings（PATCH）。 */
  settings?: Record<string, string>;
  /** 回填补丁：合并进连接器 secrets（留空=保持、__clear__=清除）。 */
  secrets?: Record<string, string>;
  /** 原始载荷（未识别字段原样保留）。 */
  raw: Record<string, unknown>;
}

/** 创建/更新提交体（restapi.submitRequest 的镜像）。 */
export interface ConnectorSubmitPayload {
  id?: string;
  provider?: string;
  name?: string;
  enabled?: boolean;
  mode?: string;
  settings?: Record<string, string>;
  secrets?: Record<string, string>;
  policy?: ConnectorPolicyView;
}
