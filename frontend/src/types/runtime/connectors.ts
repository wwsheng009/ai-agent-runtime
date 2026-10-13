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
