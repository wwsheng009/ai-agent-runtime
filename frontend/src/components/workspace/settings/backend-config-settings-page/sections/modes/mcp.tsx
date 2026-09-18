// MCP 管理模式面板：列表 + 新增/编辑表单 + 删除确认 + 启用/停用 + 热重载。
//
// 独立数据流：直接读写 /api/runtime/mcps，不进入 config document 草稿，
// 因此不会触发页面的未保存提示条（unsaved bar）。

import {
  PencilIcon,
  PowerIcon,
  RefreshCcwIcon,
  ServerIcon,
  Trash2Icon,
  WrenchIcon,
  ZapIcon,
} from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

import {
  createRuntimeMcp,
  deleteRuntimeMcp,
  listRuntimeMcps,
  reloadRuntimeMcps,
  setRuntimeMcpEnabled,
  updateRuntimeMcp,
} from "@/api/runtime/mcp";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import type {
  RuntimeMcpEntry,
  RuntimeMcpUpsertResponse,
} from "@/types/runtime";

import { SettingsAddButton } from "../../../settings-add-button";
import { SettingsEmptyState } from "../../../settings-empty-state";
import { SettingsIconActionButton } from "../../../settings-action-group";
import { SettingsNoticeCard } from "../../../settings-notice-card";
import { SummaryPill } from "../../primitives";
import {
  buildMcpUpsertRequest,
  createMcpDraft,
  RuntimeMcpForm,
  type McpDraft,
  type McpValidationError,
} from "./mcp-form";
import { McpToolsDialog } from "./mcp-tools-dialog";

const VALIDATION_MESSAGE_KEYS = {
  name: "mcp.messages.validationNameRequired",
  command: "mcp.messages.validationCommandRequired",
  url: "mcp.messages.validationUrlRequired",
  timeoutSeconds: "mcp.messages.validationTimeoutInvalid",
  maxParallelCalls: "mcp.messages.validationMaxParallelCallsInvalid",
  duplicateKey: "mcp.messages.validationDuplicateKey",
} as const satisfies Record<McpValidationError, string>;

const TYPE_LABEL_KEYS = {
  stdio: "mcp.types.stdio",
  sse: "mcp.types.sse",
  websocket: "mcp.types.websocket",
  streamable: "mcp.types.streamable",
} as const;

function formatError(error: unknown, fallback: string) {
  const message = error instanceof Error ? error.message.trim() : "";
  return message ? `${fallback}: ${message}` : fallback;
}

/** 用变更响应替换同条目；后端返回缺失时退回原名称。 */
function upsertEntry(
  entries: RuntimeMcpEntry[],
  next: RuntimeMcpUpsertResponse,
  fallbackName: string,
) {
  const name = next.config.name ?? fallbackName;
  const entry: RuntimeMcpEntry = {
    config: { ...next.config, name },
    status: next.status ?? {
      name,
      type: next.config.type,
      enabled: next.config.enabled ?? false,
      connected: false,
      toolCount: 0,
    },
  };
  const index = entries.findIndex((item) => item.config.name === name);
  if (index < 0) {
    return [...entries, entry];
  }
  return entries.map((item, itemIndex) => (itemIndex === index ? entry : item));
}

function mcpEndpoint(entry: RuntimeMcpEntry) {
  const { config } = entry;
  if (config.type === "stdio") {
    return [config.command, ...(config.args ?? [])].filter(Boolean).join(" ");
  }
  return config.url ?? "";
}

export function McpModeSection() {
  const { t } = useTranslation("runtimeConfig");
  const [entries, setEntries] = useState<RuntimeMcpEntry[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [statusMessage, setStatusMessage] = useState<string | null>(null);
  const [pending, setPending] = useState<string | null>(null);
  const [form, setForm] = useState<{
    mode: "create" | "edit";
    draft: McpDraft;
  } | null>(null);
  /** 非空时打开对应 MCP 的工具清单对话框。 */
  const [toolsFor, setToolsFor] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    setIsLoading(true);
    setError(null);
    try {
      const result = await listRuntimeMcps();
      setEntries(result.mcps);
    } catch (loadError) {
      setError(formatError(loadError, t("mcp.messages.loadFailed")));
    } finally {
      setIsLoading(false);
    }
  }, [t]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  function openCreate() {
    setError(null);
    setStatusMessage(null);
    setForm({ mode: "create", draft: createMcpDraft() });
  }

  function openEdit(entry: RuntimeMcpEntry) {
    setError(null);
    setStatusMessage(null);
    setForm({ mode: "edit", draft: createMcpDraft(entry.config) });
  }

  async function submitForm() {
    if (!form || pending) {
      return;
    }
    const built = buildMcpUpsertRequest(form.draft);
    if ("validationError" in built) {
      setError(t(VALIDATION_MESSAGE_KEYS[built.validationError]));
      return;
    }

    const name = built.request.name;
    setPending("save");
    setError(null);
    setStatusMessage(null);
    try {
      const result =
        form.mode === "create"
          ? await createRuntimeMcp(built.request)
          : await updateRuntimeMcp(name, built.request);
      setEntries((current) => upsertEntry(current, result, name));
      setStatusMessage(
        t(
          form.mode === "create"
            ? "mcp.messages.createSuccess"
            : "mcp.messages.updateSuccess",
          { name },
        ),
      );
      setForm(null);
    } catch (submitError) {
      setError(formatError(submitError, t("mcp.messages.operationFailed")));
    } finally {
      setPending(null);
    }
  }

  async function toggleEnabled(entry: RuntimeMcpEntry, enabled: boolean) {
    const name = entry.config.name;
    if (pending) {
      return;
    }
    setPending(`toggle:${name}`);
    setError(null);
    setStatusMessage(null);
    try {
      const result = await setRuntimeMcpEnabled(name, enabled);
      setEntries((current) => upsertEntry(current, result, name));
      setStatusMessage(
        t(
          enabled
            ? "mcp.messages.enableSuccess"
            : "mcp.messages.disableSuccess",
          { name },
        ),
      );
    } catch (toggleError) {
      setError(formatError(toggleError, t("mcp.messages.operationFailed")));
    } finally {
      setPending(null);
    }
  }

  async function remove(entry: RuntimeMcpEntry) {
    const name = entry.config.name;
    if (pending || !window.confirm(t("mcp.messages.confirmDelete", { name }))) {
      return;
    }
    setPending(`delete:${name}`);
    setError(null);
    setStatusMessage(null);
    try {
      await deleteRuntimeMcp(name);
      setEntries((current) =>
        current.filter((item) => item.config.name !== name),
      );
      if (form?.mode === "edit" && form.draft.name === name) {
        setForm(null);
      }
      setStatusMessage(t("mcp.messages.deleteSuccess", { name }));
    } catch (deleteError) {
      setError(formatError(deleteError, t("mcp.messages.operationFailed")));
    } finally {
      setPending(null);
    }
  }

  async function reload() {
    if (pending) {
      return;
    }
    setPending("reload");
    setError(null);
    setStatusMessage(null);
    try {
      await reloadRuntimeMcps();
      const result = await listRuntimeMcps();
      setEntries(result.mcps);
      setStatusMessage(t("mcp.messages.reloadSuccess"));
    } catch (reloadError) {
      setError(formatError(reloadError, t("mcp.messages.operationFailed")));
    } finally {
      setPending(null);
    }
  }

  const connectedCount = entries.filter((entry) => entry.status.connected).length;

  return (
    <>
      <div className="rounded-panel border border-border bg-surface-softer p-3">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2">
              <span className="inline-flex size-6 items-center justify-center rounded-field border border-border bg-surface-solid text-accent-primary">
                <ServerIcon size={13} />
              </span>
              <div className="text-base font-semibold text-foreground">
                {t("mcp.title")}
              </div>
              <SummaryPill
                label={t("mcp.title")}
                value={t("mcp.counts.total", { count: entries.length })}
              />
              <SummaryPill
                label={t("mcp.status.connected")}
                value={t("mcp.counts.connected", { count: connectedCount })}
              />
            </div>
            <p className="mt-1.5 max-w-[46rem] text-xs leading-5 text-muted-foreground">
              {t("mcp.description")}
            </p>
          </div>

          <div className="flex flex-wrap items-center gap-2">
            <Button
              disabled={isLoading || pending !== null}
              size="sm"
              type="button"
              variant="secondary"
              onClick={() => {
                void refresh();
              }}
            >
              <RefreshCcwIcon size={14} />
              {t("mcp.actions.refresh")}
            </Button>
            <Button
              disabled={pending !== null}
              size="sm"
              type="button"
              variant="secondary"
              onClick={() => {
                void reload();
              }}
            >
              <ZapIcon size={14} />
              {t("mcp.actions.reload")}
            </Button>
            <SettingsAddButton
              disabled={pending !== null}
              label={t("mcp.actions.add")}
              size="sm"
              type="button"
              onClick={openCreate}
            />
          </div>
        </div>
      </div>

      {statusMessage ? (
        <SettingsNoticeCard className="mt-0" tone="neutral">
          <span role="status">{statusMessage}</span>
        </SettingsNoticeCard>
      ) : null}

      {error ? (
        <SettingsNoticeCard className="mt-0" tone="warning">
          <span role="alert">{error}</span>
        </SettingsNoticeCard>
      ) : null}

      {form ? (
        <RuntimeMcpForm
          draft={form.draft}
          isSaving={pending !== null}
          mode={form.mode}
          onCancel={() => setForm(null)}
          onChange={(draft) => setForm({ ...form, draft })}
          onSubmit={() => {
            void submitForm();
          }}
        />
      ) : null}

      {isLoading ? (
        <SettingsEmptyState variant="dashed">
          {t("mcp.messages.loading")}
        </SettingsEmptyState>
      ) : entries.length === 0 && !error ? (
        <SettingsEmptyState variant="dashed">
          {t("mcp.messages.empty")}
        </SettingsEmptyState>
      ) : (
        <div className="grid gap-2">
          {entries.map((entry) => {
            const { config, status } = entry;
            const name = config.name;
            const isToggling = pending === `toggle:${name}`;
            const statusLabel = !status.enabled
              ? t("mcp.status.disabled")
              : status.connected
                ? t("mcp.status.connected")
                : t("mcp.status.disconnected");
            const endpoint = mcpEndpoint(entry);
            const trustLabel = status.trustLevel || config.trustLevel;
            const typeKey =
              TYPE_LABEL_KEYS[config.type as keyof typeof TYPE_LABEL_KEYS];
            return (
              <div
                key={name}
                className="rounded-panel border border-border bg-surface-softer p-3"
                data-mcp-name={name}
              >
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <div className="flex min-w-0 flex-wrap items-center gap-2">
                    <span className="text-sm font-semibold text-foreground">
                      {name}
                    </span>
                    <Badge className="normal-case">
                      {typeKey ? t(typeKey) : config.type}
                    </Badge>
                    <Badge
                      className={cn(
                        !status.enabled
                          ? "text-accent-orange"
                          : status.connected
                            ? "text-accent-primary"
                            : undefined,
                      )}
                    >
                      {statusLabel}
                    </Badge>
                    <Badge>
                      {trustLabel
                        ? trustLabel
                        : t("mcp.trustLevels.defaultShort")}
                    </Badge>
                    <SummaryPill
                      label={t("mcp.fields.tools")}
                      value={t("mcp.counts.tools", { count: status.toolCount })}
                    />
                  </div>

                  <div className="flex flex-nowrap items-center gap-1">
                    <SettingsIconActionButton
                      disabled={pending !== null}
                      label={t("mcp.actions.tools")}
                      onClick={() => {
                        setToolsFor(entry.config.name);
                      }}
                    >
                      <WrenchIcon size={14} />
                    </SettingsIconActionButton>
                    <SettingsIconActionButton
                      disabled={pending !== null}
                      label={t("mcp.actions.edit")}
                      onClick={() => openEdit(entry)}
                    >
                      <PencilIcon size={14} />
                    </SettingsIconActionButton>
                    <SettingsIconActionButton
                      disabled={pending !== null}
                      label={
                        status.enabled
                          ? t("mcp.actions.disable")
                          : t("mcp.actions.enable")
                      }
                      onClick={() => {
                        void toggleEnabled(entry, !status.enabled);
                      }}
                    >
                      {isToggling ? (
                        <RefreshCcwIcon className="animate-spin" size={14} />
                      ) : (
                        <PowerIcon size={14} />
                      )}
                    </SettingsIconActionButton>
                    <SettingsIconActionButton
                      className="text-accent-orange"
                      disabled={pending !== null}
                      label={t("mcp.actions.delete")}
                      onClick={() => {
                        void remove(entry);
                      }}
                    >
                      <Trash2Icon size={14} />
                    </SettingsIconActionButton>
                  </div>
                </div>

                <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
                  <span className="truncate font-mono">
                    {endpoint || "—"}
                  </span>
                  {config.description ? (
                    <span className="truncate">{config.description}</span>
                  ) : null}
                  {status.lastError ? (
                    <span className="text-accent-orange">
                      {t("mcp.fields.lastError")}: {status.lastError}
                    </span>
                  ) : null}
                </div>
              </div>
            );
          })}
        </div>
      )}

      {toolsFor ? (
        <McpToolsDialog
          name={toolsFor}
          onClose={() => {
            setToolsFor(null);
          }}
        />
      ) : null}
    </>
  );
}
