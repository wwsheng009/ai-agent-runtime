import {
  BotIcon,
  CheckIcon,
  CopyIcon,
  PencilIcon,
  StarIcon,
  Trash2Icon,
} from "lucide-react";
import { useMemo, useState } from "react";

import { Badge } from "@/components/ui/badge";
import { Select } from "@/components/ui/select";

import { ConfigDomainDialog } from "./config-domain-dialog";
import {
  ConfigDomainSummaryBadge,
  ConfigDomainTable,
} from "./config-domain-table";
import { ConfigFormField } from "./config-form-field";
import { editorControlClassName } from "./editor-control-class";
import {
  buildProviderCreateConfigSnippet,
  createDefaultProviderConfig,
  isConfigRecord,
  type RuntimeProviderSummary,
} from "./runtime-provider-config-utils";
import { type ProviderDraftInput } from "./runtime-provider-domain-form-utils";
import { readRuntimeProxyConfig } from "./runtime-proxy-domain-utils";
import {
  SettingsActionGroup,
  SettingsIconActionButton,
} from "./settings-action-group";
import { SettingsAddButton } from "./settings-add-button";
import { SettingsDialogFooter } from "./settings-dialog-footer";
import { SettingsNoticeCard } from "./settings-notice-card";

const KNOWN_PROVIDER_KEYS = new Set([
  "enabled",
  "protocol",
  "truncation_adapter",
  "base_url",
  "api_path",
  "forward_url",
  "api_key",
  "default_model",
  "supported_models",
  "support_types",
  "timeout",
  "headers",
  "model_mappings",
  "proxy",
]);

const providerProtocolOptions = [
  { value: "openai", label: "openai" },
  { value: "openai_image", label: "openai_image" },
  { value: "anthropic", label: "anthropic" },
  { value: "gemini", label: "gemini" },
  { value: "codex", label: "codex" },
] as const;

type RuntimeProviderDomainEditorProps = {
  defaultProvider: string;
  onDeleteProvider: (name: string) => void;
  onSaveProvider: (
    draft: ProviderDraftInput,
    previousName: string | null,
  ) => string | null;
  onSetDefaultProvider: (name: string) => void;
  providers: RuntimeProviderSummary[];
};

export function RuntimeProviderDomainEditor({
  defaultProvider,
  onDeleteProvider,
  onSaveProvider,
  onSetDefaultProvider,
  providers,
}: RuntimeProviderDomainEditorProps) {
  const [dialogOpen, setDialogOpen] = useState(false);
  const [dialogError, setDialogError] = useState<string | null>(null);
  const [editingProviderName, setEditingProviderName] = useState<string | null>(null);
  const [copiedProviderName, setCopiedProviderName] = useState<string | null>(null);
  const [draft, setDraft] = useState<ProviderDraftInput>(() =>
    createProviderDraftInput(null, ""),
  );

  const enabledCount = useMemo(
    () => providers.filter((provider) => provider.enabled).length,
    [providers],
  );

  function openCreateDialog() {
    setDialogError(null);
    setEditingProviderName(null);
    setDraft(createProviderDraftInput(null, defaultProvider));
    setDialogOpen(true);
  }

  function openEditDialog(provider: RuntimeProviderSummary) {
    setDialogError(null);
    setEditingProviderName(provider.name);
    setDraft(createProviderDraftInput(provider, defaultProvider));
    setDialogOpen(true);
  }

  function handleSave() {
    const error = onSaveProvider(draft, editingProviderName);
    if (error) {
      setDialogError(error);
      return;
    }
    setDialogOpen(false);
  }

  async function handleCopyProvider(provider: RuntimeProviderSummary) {
    try {
      await navigator.clipboard.writeText(buildProviderCreateConfigSnippet(provider));
      setCopiedProviderName(provider.name);
      window.setTimeout(() => {
        setCopiedProviderName((currentName) =>
          currentName === provider.name ? null : currentName,
        );
      }, 1500);
    } catch {
      setCopiedProviderName(null);
    }
  }

  return (
    <>
      <ConfigDomainTable
        title="Providers"
        titleIcon={BotIcon}
        description="用表格浏览 provider，用弹出表单编辑高频字段、JSON 附加字段和扩展配置。"
        items={providers}
        getRowKey={(provider) => provider.name}
        emptyState="当前还没有 provider，可直接创建一条新的 provider 草稿。"
        summary={
          <>
            <ConfigDomainSummaryBadge>{`${providers.length} 个 provider`}</ConfigDomainSummaryBadge>
            <ConfigDomainSummaryBadge>{`${enabledCount} 已启用`}</ConfigDomainSummaryBadge>
            <ConfigDomainSummaryBadge>
              {defaultProvider ? `默认 ${defaultProvider}` : "未设默认"}
            </ConfigDomainSummaryBadge>
          </>
        }
        actions={
          <SettingsAddButton size="sm" label="新建 provider" onClick={openCreateDialog} />
        }
        columns={[
          {
            header: "名称",
            cell: (provider) => (
              <div className="min-w-[11rem]">
                <div className="flex flex-wrap items-center gap-2">
                  <div className="font-semibold text-[var(--foreground)]">
                    {provider.name}
                  </div>
                  {provider.name === defaultProvider ? <Badge>default</Badge> : null}
                </div>
                <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                  {provider.baseUrl || "未设置 base_url"}
                </div>
              </div>
            ),
          },
          {
            header: "协议",
            cell: (provider) => (
              <div className="min-w-[7rem]">
                <div>{provider.protocol || "--"}</div>
                <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                  {provider.supportTypes.join(", ") || "未设类型"}
                </div>
              </div>
            ),
          },
          {
            header: "默认模型",
            cell: (provider) => (
              <div className="min-w-[10rem]">
                <div>{provider.defaultModel || "--"}</div>
                <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                  {provider.supportedModels.length} 个模型
                </div>
              </div>
            ),
          },
          {
            header: "状态",
            cell: (provider) => (
              <div className="flex flex-wrap gap-2">
                <Badge>{provider.enabled ? "已启用" : "已禁用"}</Badge>
                {provider.hasProxyOverride ? (
                  <Badge>{provider.proxyEnabled ? "代理覆盖" : "代理已配置"}</Badge>
                ) : null}
                {provider.extraFieldCount > 0 ? (
                  <Badge>{`${provider.extraFieldCount} 个扩展字段`}</Badge>
                ) : null}
              </div>
            ),
          },
          {
            header: "操作",
            cell: (provider) => (
              <SettingsActionGroup compact>
                {provider.name !== defaultProvider ? (
                  <SettingsIconActionButton
                    label={`设 ${provider.name} 为默认 provider`}
                    onClick={() => onSetDefaultProvider(provider.name)}
                  >
                    <StarIcon size={13} />
                  </SettingsIconActionButton>
                ) : null}
                <SettingsIconActionButton
                  label={`复制 ${provider.name} 的创建配置`}
                  onClick={() => void handleCopyProvider(provider)}
                >
                  {copiedProviderName === provider.name ? (
                    <CheckIcon size={13} />
                  ) : (
                    <CopyIcon size={13} />
                  )}
                </SettingsIconActionButton>
                <SettingsIconActionButton
                  label={`编辑 ${provider.name}`}
                  onClick={() => openEditDialog(provider)}
                >
                  <PencilIcon size={13} />
                </SettingsIconActionButton>
                <SettingsIconActionButton
                  label={`删除 ${provider.name}`}
                  onClick={() => onDeleteProvider(provider.name)}
                >
                  <Trash2Icon size={13} />
                </SettingsIconActionButton>
              </SettingsActionGroup>
            ),
            align: "right",
            className: "w-[9.5rem] min-w-[9.5rem]",
          },
        ]}
      />

      <ConfigDomainDialog
        open={dialogOpen}
        onClose={() => setDialogOpen(false)}
        title={editingProviderName ? `编辑 Provider: ${editingProviderName}` : "新建 Provider"}
        description="主字段使用表单编辑，`headers`、`model_mappings` 和其它扩展字段用 JSON 输入，避免丢失专用配置。"
        footer={
          <SettingsDialogFooter
            buttonSize="sm"
            note="保存后仍建议先看 diff，再写回 `config.yaml`。"
            confirmLabel="保存 provider"
            onCancel={() => setDialogOpen(false)}
            onConfirm={handleSave}
          />
        }
      >
        <div className="space-y-3">
          {dialogError ? (
            <SettingsNoticeCard tone="warning-soft">
              {dialogError}
            </SettingsNoticeCard>
          ) : null}

          <div className="grid gap-3 xl:grid-cols-2">
            <ConfigFormField label="名称" description="provider 唯一标识，用于路由和默认项配置。">
              <input
                className={editorControlClassName}
                value={draft.name}
                onChange={(event) => setDraft((current) => ({ ...current, name: event.target.value }))}
                placeholder="provider 名称"
              />
            </ConfigFormField>
            <ConfigFormField label="协议" description="常见值包括 openai、anthropic、gemini、codex。">
              <Select
                ariaLabel="Provider 协议"
                value={draft.protocol}
                onChange={(value) =>
                  setDraft((current) => ({ ...current, protocol: value }))
                }
                options={providerProtocolOptions}
                className="w-full"
                triggerClassName={editorControlClassName}
                optionClassName="text-sm"
              />
            </ConfigFormField>
            <ConfigFormField label="base_url">
              <input
                className={editorControlClassName}
                value={draft.baseUrl}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, baseUrl: event.target.value }))
                }
                placeholder="https://api.example.com"
              />
            </ConfigFormField>
            <ConfigFormField label="default_model">
              <input
                className={editorControlClassName}
                value={draft.defaultModel}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, defaultModel: event.target.value }))
                }
                placeholder="gpt-5.4"
              />
            </ConfigFormField>
            <ConfigFormField label="api_path">
              <input
                className={editorControlClassName}
                value={draft.apiPath}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, apiPath: event.target.value }))
                }
                placeholder="/v1/chat/completions"
              />
            </ConfigFormField>
            <ConfigFormField label="forward_url">
              <input
                className={editorControlClassName}
                value={draft.forwardUrl}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, forwardUrl: event.target.value }))
                }
                placeholder="/v1/chat/completions"
              />
            </ConfigFormField>
            <ConfigFormField label="timeout">
              <input
                className={editorControlClassName}
                value={draft.timeout}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, timeout: event.target.value }))
                }
                placeholder="300s"
              />
            </ConfigFormField>
            <ConfigFormField label="truncation_adapter">
              <input
                className={editorControlClassName}
                value={draft.truncationAdapter}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    truncationAdapter: event.target.value,
                  }))
                }
                placeholder="openai_local"
              />
            </ConfigFormField>
          </div>

          <div className="grid gap-3 xl:grid-cols-2">
            <ConfigFormField label="supported_models" description="支持用换行或逗号批量输入。">
              <textarea
                className={`${editorControlClassName} min-h-36 resize-y font-mono`}
                value={draft.supportedModelsText}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    supportedModelsText: event.target.value,
                  }))
                }
              />
            </ConfigFormField>
            <ConfigFormField label="support_types" description="协议类型列表，支持换行或逗号输入。">
              <textarea
                className={`${editorControlClassName} min-h-36 resize-y font-mono`}
                value={draft.supportTypesText}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    supportTypesText: event.target.value,
                  }))
                }
              />
            </ConfigFormField>
          </div>

          <ConfigFormField label="api_key" description="支持环境变量模板或代理注入占位符。">
            <textarea
              className={`${editorControlClassName} min-h-28 resize-y font-mono`}
              value={draft.apiKey}
              onChange={(event) =>
                setDraft((current) => ({ ...current, apiKey: event.target.value }))
              }
            />
          </ConfigFormField>

          <div className="grid gap-3 xl:grid-cols-2">
            <ConfigFormField label="headers JSON" description="固定请求头对象，例如组织标识、版本头。">
              <textarea
                className={`${editorControlClassName} min-h-40 resize-y font-mono`}
                value={draft.headersJson}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, headersJson: event.target.value }))
                }
              />
            </ConfigFormField>
            <ConfigFormField
              label="model_mappings JSON"
              description="模型映射对象，支持精确或通配符 key。"
            >
              <textarea
                className={`${editorControlClassName} min-h-40 resize-y font-mono`}
                value={draft.modelMappingsJson}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    modelMappingsJson: event.target.value,
                  }))
                }
              />
            </ConfigFormField>
          </div>

          <div className="rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
            <div className="mb-3 flex flex-wrap items-center justify-between gap-3">
              <div>
                <div className="text-[13px] font-semibold text-[var(--foreground)]">
                  Provider 级代理覆盖
                </div>
                <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                  可为当前 provider 单独指定 HTTP / HTTPS / SOCKS5 代理，不填则沿用全局代理或环境变量。
                </div>
              </div>
              <label className="flex items-center gap-2 text-sm text-[var(--foreground)]">
                <input
                  type="checkbox"
                  className="h-4 w-4 accent-[var(--accent-primary)]"
                  checked={draft.proxyEnabled}
                  onChange={(event) =>
                    setDraft((current) => ({
                      ...current,
                      proxyEnabled: event.target.checked,
                    }))
                  }
                />
                启用覆盖代理
              </label>
            </div>

            <div className="grid gap-3 xl:grid-cols-2">
              <ConfigFormField
                label="proxy.http"
                description="HTTP 请求代理，也支持 socks5://host:port。"
              >
                <input
                  className={editorControlClassName}
                  value={draft.proxyHttp}
                  onChange={(event) =>
                    setDraft((current) => ({
                      ...current,
                      proxyHttp: event.target.value,
                    }))
                  }
                  placeholder="http://127.0.0.1:10810 或 socks5://127.0.0.1:10810"
                />
              </ConfigFormField>
              <ConfigFormField
                label="proxy.https"
                description="HTTPS 请求代理，未填写时会回退到 HTTP 代理。"
              >
                <input
                  className={editorControlClassName}
                  value={draft.proxyHttps}
                  onChange={(event) =>
                    setDraft((current) => ({
                      ...current,
                      proxyHttps: event.target.value,
                    }))
                  }
                  placeholder="http://127.0.0.1:10810 或 socks5://127.0.0.1:10810"
                />
              </ConfigFormField>
            </div>

            <div className="mt-3">
              <ConfigFormField
                label="proxy.no_proxy"
                description="逗号分隔的绕过列表，例如 localhost,127.0.0.1,.internal.example.com。"
              >
                <textarea
                  className={`${editorControlClassName} min-h-24 resize-y font-mono`}
                  value={draft.proxyNoProxy}
                  onChange={(event) =>
                    setDraft((current) => ({
                      ...current,
                      proxyNoProxy: event.target.value,
                    }))
                  }
                  placeholder="localhost,127.0.0.1,.internal.example.com"
                />
              </ConfigFormField>
            </div>
          </div>

          <ConfigFormField
            label="扩展字段 JSON"
            description="保留未进入专用表单的其它字段，避免移除结构化树后无法编辑。"
          >
            <textarea
              className={`${editorControlClassName} min-h-44 resize-y font-mono`}
              value={draft.extraJson}
              onChange={(event) =>
                setDraft((current) => ({ ...current, extraJson: event.target.value }))
              }
            />
          </ConfigFormField>

          <div className="flex flex-wrap gap-3 rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-softer)] px-3 py-2.5">
            <label className="flex items-center gap-2 text-sm text-[var(--foreground)]">
              <input
                type="checkbox"
                className="h-4 w-4 accent-[var(--accent-primary)]"
                checked={draft.enabled}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, enabled: event.target.checked }))
                }
              />
              启用 provider
            </label>
            <label className="flex items-center gap-2 text-sm text-[var(--foreground)]">
              <input
                type="checkbox"
                className="h-4 w-4 accent-[var(--accent-primary)]"
                checked={draft.setAsDefault}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    setAsDefault: event.target.checked,
                  }))
                }
              />
              设为默认 provider
            </label>
          </div>
        </div>
      </ConfigDomainDialog>
    </>
  );
}

function createProviderDraftInput(
  provider: RuntimeProviderSummary | null,
  defaultProvider: string,
): ProviderDraftInput {
  if (!provider) {
    const defaults = createDefaultProviderConfig("new_provider", "openai");
    return {
      name: "",
      enabled: Boolean(defaults.enabled),
      protocol: typeof defaults.protocol === "string" ? defaults.protocol : "openai",
      baseUrl: typeof defaults.base_url === "string" ? defaults.base_url : "",
      apiPath: typeof defaults.api_path === "string" ? defaults.api_path : "",
      forwardUrl: typeof defaults.forward_url === "string" ? defaults.forward_url : "",
      apiKey: typeof defaults.api_key === "string" ? defaults.api_key : "",
      defaultModel:
        typeof defaults.default_model === "string" ? defaults.default_model : "",
      supportedModelsText: Array.isArray(defaults.supported_models)
        ? defaults.supported_models.join("\n")
        : "",
      supportTypesText: Array.isArray(defaults.support_types)
        ? defaults.support_types.join("\n")
        : "",
      timeout: typeof defaults.timeout === "string" ? defaults.timeout : "",
      truncationAdapter:
        typeof defaults.truncation_adapter === "string"
          ? defaults.truncation_adapter
          : "",
      proxyEnabled: false,
      proxyHttp: "",
      proxyHttps: "",
      proxyNoProxy: "",
      headersJson: JSON.stringify(defaults.headers ?? {}, null, 2),
      modelMappingsJson: JSON.stringify(defaults.model_mappings ?? {}, null, 2),
      extraJson: "{}",
      setAsDefault: defaultProvider === "",
    };
  }

  const extraFields = Object.fromEntries(
    Object.entries(provider.raw).filter(([key]) => !KNOWN_PROVIDER_KEYS.has(key)),
  );
  const proxyConfig = readRuntimeProxyConfig(provider.raw.proxy);

  return {
    name: provider.name,
    enabled: provider.enabled,
    protocol: provider.protocol,
    baseUrl: provider.baseUrl,
    apiPath: provider.apiPath,
    forwardUrl: provider.forwardUrl,
    apiKey: provider.apiKey,
    defaultModel: provider.defaultModel,
    supportedModelsText: provider.supportedModels.join("\n"),
    supportTypesText: provider.supportTypes.join("\n"),
    timeout: provider.timeout,
    truncationAdapter: provider.truncationAdapter,
    proxyEnabled: proxyConfig.enabled,
    proxyHttp: proxyConfig.http,
    proxyHttps: proxyConfig.https,
    proxyNoProxy: proxyConfig.noProxy,
    headersJson: JSON.stringify(
      isConfigRecord(provider.raw.headers) ? provider.raw.headers : {},
      null,
      2,
    ),
    modelMappingsJson: JSON.stringify(
      isConfigRecord(provider.raw.model_mappings) ? provider.raw.model_mappings : {},
      null,
      2,
    ),
    extraJson: JSON.stringify(extraFields, null, 2),
    setAsDefault: provider.name === defaultProvider,
  };
}
