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
}

export interface ConnectorFieldError {
  key: string;
  message: string;
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
