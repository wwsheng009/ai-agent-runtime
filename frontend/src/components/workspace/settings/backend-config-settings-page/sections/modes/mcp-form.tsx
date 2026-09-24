// MCP 新增/编辑表单：草稿类型、文本 ↔ 请求映射、表单组件。
//
// 独立于 config document 草稿：这里只在提交时调用 /api/runtime/mcps，
// 由父面板负责成功/失败反馈与列表刷新。

import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import type {
  RuntimeMcpConfig,
  RuntimeMcpTransportType,
  RuntimeMcpTrustLevel,
  RuntimeMcpUpsertRequest,
} from "@/types/runtime";

import { editorControlClassName, editorToggleRowClassName } from "../../../editor-control-class";
import { ConfigFormField } from "../../../config-form-field";
import { KeyValueEditor } from "./key-value-editor";
import { createKeyValueRow, type KeyValueRow } from "./key-value-rows";

/** env / headers 的结构化行；id 仅用于 React key，不进入请求体。 */
export type McpKeyValueRow = KeyValueRow;

export type McpDraft = {
  name: string;
  type: RuntimeMcpTransportType;
  description: string;
  enabled: boolean;
  /** "" 表示不传（由后端按类型推导默认值）。 */
  trustLevel: "" | RuntimeMcpTrustLevel;
  timeoutSeconds: string;
  maxParallelCalls: string;
  command: string;
  args: string;
  env: McpKeyValueRow[];
  url: string;
  headers: McpKeyValueRow[];
};

export type McpValidationError =
  | "name"
  | "command"
  | "url"
  | "timeoutSeconds"
  | "maxParallelCalls"
  | "duplicateKey";

// 草稿行工厂/常量/构建器供 mcp.tsx 与测试复用；react-refresh 禁止组件文件
// 导出非组件值，按 ui/button.tsx 的既有先例逐个做行级豁免（id 不进请求体）。
// eslint-disable-next-line react-refresh/only-export-components
export function createMcpKeyValueRow(key = "", value = ""): McpKeyValueRow {
  return createKeyValueRow(key, value);
}

// eslint-disable-next-line react-refresh/only-export-components
export const MCP_TRANSPORT_TYPES: RuntimeMcpTransportType[] = [
  "stdio",
  "sse",
  "websocket",
  "streamable",
];

const MCP_TRUST_LEVELS: RuntimeMcpTrustLevel[] = [
  "local",
  "trusted_remote",
  "untrusted_remote",
];

// 显式键映射：动态模板 key 无法通过 i18next 的类型收窄，这里保持 key 为字面量联合。
const MCP_TYPE_LABEL_KEYS = {
  stdio: "mcp.types.stdio",
  sse: "mcp.types.sse",
  websocket: "mcp.types.websocket",
  streamable: "mcp.types.streamable",
} as const;

const MCP_TRUST_LEVEL_LABEL_KEYS = {
  local: "mcp.trustLevels.local",
  trusted_remote: "mcp.trustLevels.trusted_remote",
  untrusted_remote: "mcp.trustLevels.untrusted_remote",
} as const;

// eslint-disable-next-line react-refresh/only-export-components
export function createMcpDraft(config?: RuntimeMcpConfig): McpDraft {
  const type = normalizeTransportType(config?.type);
  const { envRows, headerRows } = splitMcpEnvRows(config?.env, type);
  return {
    name: config?.name ?? "",
    type,
    description: config?.description ?? "",
    enabled: config?.disabled === true ? false : (config?.enabled ?? true),
    trustLevel: normalizeTrustLevel(config?.trustLevel),
    timeoutSeconds: parseTimeoutSeconds(config?.timeout),
    maxParallelCalls:
      config?.maxParallelCalls && config.maxParallelCalls > 0
        ? String(config.maxParallelCalls)
        : "",
    command: config?.command ?? "",
    args: (config?.args ?? []).join("\n"),
    env: envRows,
    url: config?.url ?? "",
    // headers 在后端以 HEADER_<Name> 存进 env，编辑时由 env 拆分回填（见下），
    // 提交时再合并回 env 发送，不再双写 headers 字段。
    headers: headerRows,
  };
}

const MCP_HEADER_ENV_PREFIX = "HEADER_";

/**
 * env → 结构化行：stdio 全部视为进程环境变量；URL 传输把 HEADER_<Name>
 * 拆为请求头行（去掉前缀），其余保留为环境变量行。
 */
function splitMcpEnvRows(
  env: Record<string, string> | undefined,
  type: RuntimeMcpTransportType,
): { envRows: McpKeyValueRow[]; headerRows: McpKeyValueRow[] } {
  const envRows: McpKeyValueRow[] = [];
  const headerRows: McpKeyValueRow[] = [];
  for (const [key, value] of Object.entries(env ?? {})) {
    const isHeader =
      type !== "stdio" &&
      key.startsWith(MCP_HEADER_ENV_PREFIX) &&
      key.length > MCP_HEADER_ENV_PREFIX.length;
    if (isHeader) {
      headerRows.push(
        createMcpKeyValueRow(key.slice(MCP_HEADER_ENV_PREFIX.length), value),
      );
      continue;
    }
    envRows.push(createMcpKeyValueRow(key, value));
  }
  return { envRows, headerRows };
}

/** 结构化行 → 请求体 Record：忽略空键行，键/值都 trim。 */
function mcpRowsToEntries(rows: McpKeyValueRow[]): Array<[string, string]> {
  const entries: Array<[string, string]> = [];
  for (const row of rows) {
    const key = row.key.trim();
    if (!key) {
      continue;
    }
    entries.push([key, row.value.trim()]);
  }
  return entries;
}

/** "30s" / "1m30s" 等 Go duration 文本只回填纯秒数，其余形式留空由用户重填。 */
function parseTimeoutSeconds(timeout: string | undefined): string {
  if (!timeout) {
    return "";
  }
  const match = /^(\d+)s$/.exec(timeout.trim());
  return match ? match[1] : "";
}

function normalizeTransportType(
  type: RuntimeMcpConfig["type"] | undefined,
): RuntimeMcpTransportType {
  switch ((type ?? "").trim().toLowerCase()) {
    case "stdio":
      return "stdio";
    case "sse":
      return "sse";
    case "websocket":
    case "ws":
      return "websocket";
    default:
      return "streamable";
  }
}

function normalizeTrustLevel(
  trustLevel: RuntimeMcpConfig["trustLevel"] | undefined,
): "" | RuntimeMcpTrustLevel {
  return MCP_TRUST_LEVELS.includes(trustLevel as RuntimeMcpTrustLevel)
    ? (trustLevel as RuntimeMcpTrustLevel)
    : "";
}

// eslint-disable-next-line react-refresh/only-export-components
export function parseLineList(value: string): string[] {
  return value
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter(Boolean);
}

function parsePositiveInteger(
  value: string,
): { ok: true; value: number | null } | { ok: false } {
  const trimmed = value.trim();
  if (!trimmed) {
    return { ok: true, value: null };
  }
  const parsed = Number(trimmed);
  if (!Number.isInteger(parsed) || parsed <= 0) {
    return { ok: false };
  }
  return { ok: true, value: parsed };
}

export type McpUpsertBuildResult =
  | { request: RuntimeMcpUpsertRequest }
  | { validationError: McpValidationError };

/** 草稿 → UpsertRequest；先做本地校验，避免把明显无效的请求发给后端。 */
// eslint-disable-next-line react-refresh/only-export-components
export function buildMcpUpsertRequest(draft: McpDraft): McpUpsertBuildResult {
  const name = draft.name.trim();
  if (!name) {
    return { validationError: "name" };
  }

  const request: RuntimeMcpUpsertRequest = {
    name,
    type: draft.type,
    description: draft.description.trim(),
    enabled: draft.enabled,
  };
  if (draft.trustLevel) {
    request.trustLevel = draft.trustLevel;
  }

  const timeout = parsePositiveInteger(draft.timeoutSeconds);
  if (!timeout.ok) {
    return { validationError: "timeoutSeconds" };
  }
  if (timeout.value !== null) {
    request.timeoutSeconds = timeout.value;
  }

  const maxParallelCalls = parsePositiveInteger(draft.maxParallelCalls);
  if (!maxParallelCalls.ok) {
    return { validationError: "maxParallelCalls" };
  }
  if (maxParallelCalls.value !== null) {
    request.maxParallelCalls = maxParallelCalls.value;
  }

  if (draft.type === "stdio") {
    const command = draft.command.trim();
    if (!command) {
      return { validationError: "command" };
    }
    request.command = command;
    request.args = parseLineList(draft.args);
  } else {
    const url = draft.url.trim();
    if (!url) {
      return { validationError: "url" };
    }
    request.url = url;
  }

  // stdio 的 env 行就是进程环境变量；URL 传输把请求头行合并回
  // HEADER_<Name> 写进 env（后端 MCPConfig 只有 env，没有 headers 字段），
  // 不发送 headers，避免双写与「HEADER_ 前缀清空」语义分叉。
  const envEntries = mcpRowsToEntries(draft.env);
  if (draft.type !== "stdio") {
    for (const [key, value] of mcpRowsToEntries(draft.headers)) {
      envEntries.push([`${MCP_HEADER_ENV_PREFIX}${key}`, value]);
    }
  }
  const seenKeys = new Set<string>();
  for (const [key] of envEntries) {
    if (seenKeys.has(key)) {
      return { validationError: "duplicateKey" };
    }
    seenKeys.add(key);
  }
  // 显式下发（允许空对象）：结构化编辑器删光所有行时必须真正清空，
  // 省略 env 会被后端当成「保持原值」，用户将无法删除已有配置。
  request.env = Object.fromEntries(envEntries);

  return { request };
}

type RuntimeMcpFormProps = {
  draft: McpDraft;
  isSaving: boolean;
  mode: "create" | "edit";
  onChange: (draft: McpDraft) => void;
  onCancel: () => void;
  onSubmit: () => void;
};

export function RuntimeMcpForm({
  draft,
  isSaving,
  mode,
  onChange,
  onCancel,
  onSubmit,
}: RuntimeMcpFormProps) {
  const { t } = useTranslation("runtimeConfig");

  function patch(next: Partial<McpDraft>) {
    onChange({ ...draft, ...next });
  }

  const isStdio = draft.type === "stdio";

  return (
    <form
      className="rounded-panel border border-border bg-surface-softer p-3"
      aria-label={
        mode === "create"
          ? t("mcp.form.createTitle")
          : t("mcp.form.editTitle", { name: draft.name })
      }
      onSubmit={(event) => {
        event.preventDefault();
        onSubmit();
      }}
    >
      <div className="text-base font-semibold text-foreground">
        {mode === "create"
          ? t("mcp.form.createTitle")
          : t("mcp.form.editTitle", { name: draft.name })}
      </div>

      <div className="mt-3 grid gap-3 lg:grid-cols-2">
        <ConfigFormField label={t("mcp.fields.name")}>
          <input
            aria-label={t("mcp.fields.name")}
            className={cn(editorControlClassName, "disabled:opacity-60")}
            disabled={mode === "edit"}
            placeholder={t("mcp.placeholders.name")}
            value={draft.name}
            onChange={(event) => patch({ name: event.target.value })}
          />
        </ConfigFormField>

        <ConfigFormField label={t("mcp.fields.type")}>
          <select
            aria-label={t("mcp.fields.type")}
            className={editorControlClassName}
            value={draft.type}
            onChange={(event) =>
              patch({ type: event.target.value as RuntimeMcpTransportType })
            }
          >
            {MCP_TRANSPORT_TYPES.map((type) => (
              <option key={type} value={type}>
                {t(MCP_TYPE_LABEL_KEYS[type])}
              </option>
            ))}
          </select>
        </ConfigFormField>

        <ConfigFormField label={t("mcp.fields.description")}>
          <input
            aria-label={t("mcp.fields.description")}
            className={editorControlClassName}
            placeholder={t("mcp.placeholders.description")}
            value={draft.description}
            onChange={(event) => patch({ description: event.target.value })}
          />
        </ConfigFormField>

        <ConfigFormField
          description={t("mcp.trustLevels.inherit")}
          label={t("mcp.fields.trustLevel")}
        >
          <select
            aria-label={t("mcp.fields.trustLevel")}
            className={editorControlClassName}
            value={draft.trustLevel}
            onChange={(event) =>
              patch({
                trustLevel: event.target.value as "" | RuntimeMcpTrustLevel,
              })
            }
          >
            <option value="">{t("mcp.trustLevels.inherit")}</option>
            {MCP_TRUST_LEVELS.map((level) => (
              <option key={level} value={level}>
                {t(MCP_TRUST_LEVEL_LABEL_KEYS[level])}
              </option>
            ))}
          </select>
        </ConfigFormField>

        {isStdio ? (
          <>
            <ConfigFormField label={t("mcp.fields.command")}>
              <input
                aria-label={t("mcp.fields.command")}
                className={editorControlClassName}
                placeholder={t("mcp.placeholders.command")}
                value={draft.command}
                onChange={(event) => patch({ command: event.target.value })}
              />
            </ConfigFormField>

            <ConfigFormField
              description={t("mcp.hints.args")}
              label={t("mcp.fields.args")}
            >
              <textarea
                aria-label={t("mcp.fields.args")}
                className={cn(editorControlClassName, "min-h-[4.5rem] resize-y font-mono")}
                placeholder={t("mcp.placeholders.args")}
                value={draft.args}
                onChange={(event) => patch({ args: event.target.value })}
              />
            </ConfigFormField>

            <ConfigFormField
              description={t("mcp.hints.env")}
              label={t("mcp.fields.env")}
            >
              <KeyValueEditor
                addLabel={t("mcp.kv.addRow")}
                disabled={isSaving}
                keyPlaceholder={t("mcp.kv.keyPlaceholder")}
                rows={draft.env}
                separator="="
                valuePlaceholder={t("mcp.kv.valuePlaceholder")}
                onChange={(env) => patch({ env })}
              />
            </ConfigFormField>
          </>
        ) : (
          <>
            <ConfigFormField label={t("mcp.fields.url")}>
              <input
                aria-label={t("mcp.fields.url")}
                className={editorControlClassName}
                placeholder={t("mcp.placeholders.url")}
                value={draft.url}
                onChange={(event) => patch({ url: event.target.value })}
              />
            </ConfigFormField>

            <ConfigFormField
              description={t("mcp.hints.urlEnv")}
              label={t("mcp.fields.env")}
            >
              <KeyValueEditor
                addLabel={t("mcp.kv.addRow")}
                disabled={isSaving}
                keyPlaceholder={t("mcp.kv.keyPlaceholder")}
                rows={draft.env}
                separator="="
                valuePlaceholder={t("mcp.kv.valuePlaceholder")}
                onChange={(env) => patch({ env })}
              />
            </ConfigFormField>

            <ConfigFormField
              description={t("mcp.hints.headers")}
              label={t("mcp.fields.headers")}
            >
              <KeyValueEditor
                addLabel={t("mcp.kv.addRow")}
                disabled={isSaving}
                keyPlaceholder={t("mcp.kv.keyPlaceholder")}
                rows={draft.headers}
                separator=":"
                valuePlaceholder={t("mcp.kv.valuePlaceholder")}
                onChange={(headers) => patch({ headers })}
              />
            </ConfigFormField>
          </>
        )}

        <ConfigFormField
          description={t("mcp.hints.timeoutSeconds")}
          label={t("mcp.fields.timeoutSeconds")}
        >
          <input
            aria-label={t("mcp.fields.timeoutSeconds")}
            className={editorControlClassName}
            inputMode="numeric"
            placeholder={t("mcp.placeholders.timeoutSeconds")}
            value={draft.timeoutSeconds}
            onChange={(event) => patch({ timeoutSeconds: event.target.value })}
          />
        </ConfigFormField>

        <ConfigFormField
          description={t("mcp.hints.maxParallelCalls")}
          label={t("mcp.fields.maxParallelCalls")}
        >
          <input
            aria-label={t("mcp.fields.maxParallelCalls")}
            className={editorControlClassName}
            inputMode="numeric"
            placeholder={t("mcp.placeholders.maxParallelCalls")}
            value={draft.maxParallelCalls}
            onChange={(event) => patch({ maxParallelCalls: event.target.value })}
          />
        </ConfigFormField>

        <label className={cn(editorToggleRowClassName, "lg:col-span-2")}>
          <span className="text-sm text-foreground">{t("mcp.fields.enabled")}</span>
          <input
            aria-label={t("mcp.fields.enabled")}
            checked={draft.enabled}
            type="checkbox"
            onChange={(event) => patch({ enabled: event.target.checked })}
          />
        </label>
      </div>

      <div className="mt-3 flex flex-wrap justify-end gap-2">
        <Button type="button" variant="ghost" onClick={onCancel}>
          {t("mcp.actions.cancel")}
        </Button>
        <Button disabled={isSaving} type="submit">
          {t("mcp.actions.save")}
        </Button>
      </div>
    </form>
  );
}
