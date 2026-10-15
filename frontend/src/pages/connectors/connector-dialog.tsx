// 连接器新建/编辑对话框：Schema 驱动渲染（docs/bots/07-config-ux.md §7 控件映射表）。
//
// 控件映射：text→文本框、secret→密码框+留空语义+清除、url→URL 校验、
// bool→复选框、enum→下拉、number→数字框；未知类型降级为文本框并展示原始 key。
// 表单状态与提交体构造在 ./shared.ts（含密钥「留空=不改 / __clear__=清除」单测）。

import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import {
  createConnector,
  deleteConnector,
} from "@/api/runtime/connectors";
import { listRuntimeModels } from "@/lib/runtime-api";
import type {
  ConnectorFieldError,
  ConnectorSchema,
  ConnectorSubmitPayload,
  ConnectorView,
} from "@/types/runtime";

import { Button } from "@/components/ui/button";
import { DialogOverlay, DialogPanel } from "@/components/ui/dialog-shell";
import { readAdminToken } from "@/lib/admin-token";
import { cn } from "@/lib/utils";

import { ConnectorFieldList } from "./connector-field-list";
import { ModelRoutePicker } from "./model-route-picker";
import { RescanSection } from "./rescan-section";
import { SubscriptionEditor, type SubscriptionEditorValue } from "./subscription-editor";
import {
  buildSubmitPayload,
  createEmptyForm,
  ensureCreateId,
  findProviderSchema,
  findQRAction,
  formFromConnector,
  generateConnectorId,
  hasModeField,
  mergeEventsIntoPayload,
  mergeRouteIntoPayload,
  switchProvider,
  validateConnectorForm,
  type ConnectorFormError,
  type ConnectorFormState,
} from "./shared";
import { ConnectorLoginPanel } from "./login-panel";

const inputClassName =
  "h-9 w-full rounded-control border border-border bg-surface-soft px-2.5 text-sm text-foreground outline-none placeholder:text-muted-foreground focus:border-accent-primary disabled:opacity-60";

interface ConnectorDialogProps {
  open: boolean;
  schema: ConnectorSchema | null;
  /** 为 null 时是新建。 */
  existing: ConnectorView | null;
  saving: boolean;
  serverError: string | null;
  serverFieldErrors: ConnectorFieldError[];
  onSubmit: (payload: ConnectorSubmitPayload) => void;
  /** 扫码新建/凭据回填后回调（父页刷新列表）。 */
  onChanged?: () => void;
  onClose: () => void;
}

export function ConnectorDialog({
  open,
  schema,
  existing,
  saving,
  serverError,
  serverFieldErrors,
  onSubmit,
  onChanged,
  onClose,
}: ConnectorDialogProps) {
  const { t } = useTranslation("connectors");
  const adminToken = readAdminToken();
  const isCreate = existing === null;
  const [form, setForm] = useState<ConnectorFormState>(() =>
    existing
      ? formFromConnector(existing, findProviderSchema(schema, existing.provider))
      : ensureCreateId(createEmptyForm(schema), schema),
  );
  const [errors, setErrors] = useState<ConnectorFormError[]>([]);
  /** 扫码新建：连接器已就绪后内嵌登录面板；null=尚未开始扫码。 */
  const [qrConnector, setQrConnector] = useState<ConnectorView | null>(null);
  const [qrCreating, setQrCreating] = useState(false);
  const [qrError, setQrError] = useState<string | null>(null);
  const [qrBusy, setQrBusy] = useState(false);
  /** 扫码是否已确认并回填（未确认时关闭对话框要清理草稿连接器）。 */
  const [qrConfirmed, setQrConfirmed] = useState(false);
  /**
   * D4 两层模型（docs/bots/09 §6）：编辑态「重新扫码=替换凭据」的面板开关；
   * 实体 id/name/policy 与绑定不变，只回填 secrets/settings。
   */
  const [rescanOpen, setRescanOpen] = useState(false);
  /** D6 路由候选（runtime 模型目录：provider + 模型清单）；打开时拉一次。 */
  const [catalog, setCatalog] = useState<{
    providers: readonly { name: string; models: readonly string[] }[];
    defaultReasoningEffort?: string;
  }>({ providers: [] });
  /** D6 路由编辑值（编辑态从 existing.policy.route 回填）。 */
  const [routeValue, setRouteValue] = useState({ provider: "", model: "", reasoningEffort: "" });
  /** S2 事件订阅编辑值（编辑态从 existing.policy.events 回填）。 */
  const [eventsValue, setEventsValue] = useState<SubscriptionEditorValue>({
    enabled: false,
    subscriptions: [],
  });

  /**
   * 已初始化的 (open, existing) 组合。列表刷新重建 schema 对象不得重置
   * 表单/扫码态（否则扫码过程中会被打断）。
   */
  const resetKeyRef = useRef<string | null>(null);
  useEffect(() => {
    if (!open) {
      resetKeyRef.current = null;
      return;
    }
    const key = existing ? `edit:${existing.id}` : "create";
    if (resetKeyRef.current === key) {
      return;
    }
    resetKeyRef.current = key;
    setForm(
      existing
        ? formFromConnector(existing, findProviderSchema(schema, existing.provider))
        : ensureCreateId(createEmptyForm(schema), schema),
    );
    setErrors([]);
    setQrConnector(null);
    setQrCreating(false);
    setQrError(null);
    setQrBusy(false);
    setQrConfirmed(false);
    setRescanOpen(false);
    const route = existing?.policy.route;
    setRouteValue({
      provider: route?.provider ?? "",
      model: route?.model ?? "",
      reasoningEffort: route?.reasoning_effort ?? "",
    });
    const events = existing?.policy.events;
    setEventsValue({
      enabled: events?.enabled ?? false,
      subscriptions: (events?.subscriptions ?? []).map((item) => ({
        ...item,
        sessions: item.sessions
          ? { ...item.sessions, ids: [...(item.sessions.ids ?? [])] }
          : undefined,
        types: [...(item.types ?? [])],
      })),
    });
  }, [open, existing, schema]);

  // D6 路由候选：打开对话框时拉 runtime 模型目录（失败静默——字段仍可手输）。
  useEffect(() => {
    if (!open) { return; }
    let cancelled = false;
    void listRuntimeModels()
      .then((models) => {
        if (cancelled) { return; }
        setCatalog({
          providers: (models.providers ?? []).map((p) => ({
            name: p.name,
            models: p.models ?? [],
          })),
          defaultReasoningEffort: models.default_reasoning_effort || undefined,
        });
      })
      .catch(() => undefined);
    return () => { cancelled = true; };
  }, [open]);

  useEffect(() => {
    if (!open) {
      return;
    }
    if (qrConnector || rescanOpen) {
      return; // 内嵌登录面板自行处理 Escape（回填中拦截）
    }
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== "Escape") {
        return;
      }
      if (saving || qrBusy) {
        return; // 保存中与遮罩/取消按钮一致：拦截关闭
      }
      onClose();
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [open, saving, qrBusy, qrConnector, rescanOpen, onClose]);

  if (!open) {
    return null;
  }

  const providerSchema = findProviderSchema(schema, form.provider);
  const modeFieldPresent = hasModeField(providerSchema);
  const qrAction = findQRAction(providerSchema);
  /** 新建 + 通道支持扫码：走「扫码即得凭据」的简单路径。 */
  const scanMode = isCreate && qrAction !== null;
  /** 编辑 + 通道支持扫码：提供「重新扫码=替换凭据」（D4 两层模型）。 */
  const rescanAvailable = !isCreate && qrAction !== null && existing !== null;
  const fieldError = (key: string) => {
    const local = errors.find((item) => item.key === key)?.code;
    if (local === "required") {
      return key === "id" ? t("dialog.idRequired") : t("dialog.fieldRequired");
    }
    if (local === "invalid_id") {
      return t("dialog.idInvalid");
    }
    return serverFieldErrors.find((item) => item.key === key)?.message ?? null;
  };

  /** 字段变更时清除该 key 的本地校验错误（服务端错误由下次提交/父级重置覆盖）。 */
  const clearFieldError = (key: string) => {
    setErrors((prev) =>
      prev.some((item) => item.key === key)
        ? prev.filter((item) => item.key !== key)
        : prev,
    );
  };

  const updateValue = (key: string, value: string) => {
    clearFieldError(key);
    setForm((prev) => ({ ...prev, values: { ...prev.values, [key]: value } }));
  };

  const toggleClearSecret = (key: string, cleared: boolean) => {
    clearFieldError(key);
    setForm((prev) => ({
      ...prev,
      values: cleared ? { ...prev.values, [key]: "" } : prev.values,
      clearedSecrets: { ...prev.clearedSecrets, [key]: cleared },
    }));
  };

  const handleSubmit = () => {
    if (!providerSchema) {
      return;
    }
    const next = validateConnectorForm(form, providerSchema, {
      requireID: isCreate,
      existing,
    });
    setErrors(next);
    if (next.length > 0) {
      return;
    }
    const payload = buildSubmitPayload(form, providerSchema, { isCreate });
    // D6 路由 + S2 事件订阅：编辑态随本 payload 一起 PATCH（restapi policyFrom 合并进 policy）。
    const merged = isCreate ? payload : mergeRouteIntoPayload(payload, routeValue, existing);
    onSubmit(isCreate ? payload : mergeEventsIntoPayload(merged, eventsValue, existing));
  };

  /**
   * 扫码登录：先按当前表单创建连接器（ID 缺失时自动生成、基址用 Schema 默认值），
   * 随后在对话框内嵌 ConnectorLoginPanel 展示二维码并轮询，扫码确认后凭据自动回填。
   */
  const handleStartScan = async () => {
    if (!providerSchema || !qrAction) {
      return;
    }
    const id = form.id.trim() || generateConnectorId(providerSchema.provider);
    const payload = buildSubmitPayload(
      { ...form, id },
      providerSchema,
      { isCreate: true },
    );
    setQrCreating(true);
    setQrError(null);
    try {
      const view = await createConnector(payload, { adminToken });
      setQrConnector(view);
    } catch (caught) {
      setQrError(caught instanceof Error ? caught.message : String(caught));
      setQrCreating(false);
    }
  };

  /** 扫码确认并回填：标记已确认（关闭时不再清理）并刷新列表。 */
  const handleQrRefilled = () => {
    setQrConfirmed(true);
    onChanged?.();
  };

  /** 关闭对话框：未确认的扫码草稿连接器先清理，避免留下不可用的空连接器。 */
  const handleClose = () => {
    if (qrConnector && !qrConfirmed) {
      void deleteConnector(qrConnector.id, { adminToken })
        .then(() => onChanged?.())
        .catch(() => undefined);
    }
    onClose();
  };

  /** D4：重新扫码回填凭据（实体不变）→ 回编辑表单；关闭同理，无草稿清理。 */
  const handleRescanRefilled = () => {
    setQrConfirmed(true);
    setRescanOpen(false);
    onChanged?.();
  };
  const handleRescanClose = () => {
    setRescanOpen(false);
    setQrBusy(false);
    onChanged?.();
  };

  /** 字段列表共用 props（两处复用；常量取值避免 JSX 分支收窄误伤 secret_keys）。 */
  const fieldListProps = { fields: providerSchema?.fields ?? [], values: form.values, clearedSecrets: form.clearedSecrets, configuredSecretKeys: existing?.secret_keys ?? [], fieldError, onValueChange: updateValue, onClearToggle: toggleClearSecret };

  const activeQRAction = scanMode ? qrAction : null;
  const qrActiveConnector = activeQRAction ? qrConnector : null;

  return (
    <DialogOverlay onDismiss={saving || qrBusy ? () => undefined : handleClose}>
      <DialogPanel
        role="dialog"
        aria-modal="true"
        aria-label={isCreate ? t("dialog.createTitle") : t("dialog.editTitle", { name: existing.name })}
        className={qrActiveConnector || rescanOpen ? "max-w-md" : "max-w-xl"}
      >
        {activeQRAction && qrActiveConnector ? (
          <ConnectorLoginPanel
            connectorId={qrActiveConnector.id}
            connectorName={qrActiveConnector.name}
            action={activeQRAction}
            onRefilled={handleQrRefilled}
            onClose={handleClose}
            onBusyChange={setQrBusy}
            showHeader
          />
        ) : rescanOpen && rescanAvailable && existing ? (
          // D4：重新扫码=替换凭据。实体 id/name/policy 与绑定不变；
          // 扫码成功回填 secrets/settings 后由 handleRescanRefilled 收尾。
          <ConnectorLoginPanel
            connectorId={existing.id}
            connectorName={existing.name}
            action={qrAction}
            onRefilled={handleRescanRefilled}
            onClose={handleRescanClose}
            onBusyChange={setQrBusy}
            showHeader
          />
        ) : (
          <>
        <header className="border-b border-border px-4 py-3">
          <h2 className="text-sm font-semibold text-foreground">
            {isCreate ? t("dialog.createTitle") : t("dialog.editTitle", { name: existing.name })}
          </h2>
        </header>

        <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto px-4 py-3">
          {serverError ? (
            <div className="rounded-card border border-accent-orange/25 bg-accent-orange/8 px-3 py-2 text-sm text-muted-foreground">
              {t("dialog.saveFailed", { message: serverError })}
            </div>
          ) : null}

          <div className="grid gap-3 sm:grid-cols-2">
            <label className="flex flex-col gap-1 text-xs text-muted-foreground">
              {t("dialog.provider")}
              {isCreate ? (
                <select
                  aria-label={t("dialog.provider")}
                  className={inputClassName}
                  value={form.provider}
                  onChange={(event) =>
                    setForm((prev) =>
                      ensureCreateId(switchProvider(prev, schema, event.target.value), schema),
                    )
                  }
                >
                  {(schema?.providers ?? []).map((provider) => (
                    <option key={provider.provider} value={provider.provider}>
                      {provider.label}
                    </option>
                  ))}
                </select>
              ) : (
                <span className="inline-flex h-9 items-center rounded-control border border-border bg-surface-softer px-2.5 text-sm text-foreground">
                  {providerSchema?.label ?? form.provider}
                </span>
              )}
            </label>

            <label className="flex flex-col gap-1 text-xs text-muted-foreground">
              {t("dialog.id")}
              <input
                type="text"
                aria-label={t("dialog.id")}
                className={cn(inputClassName, isCreate ? null : "opacity-60")}
                placeholder={t("dialog.idPlaceholder")}
                value={form.id}
                readOnly={!isCreate}
                onChange={(event) => {
                  clearFieldError("id");
                  setForm((prev) => ({ ...prev, id: event.target.value }));
                }}
              />
              {fieldError("id") ? (
                <span className="text-xs text-accent-orange">{fieldError("id")}</span>
              ) : null}
            </label>

            <label className="flex flex-col gap-1 text-xs text-muted-foreground">
              {t("dialog.name")}
              <input
                type="text"
                aria-label={t("dialog.name")}
                className={inputClassName}
                placeholder={t("dialog.namePlaceholder")}
                value={form.name}
                onChange={(event) =>
                  setForm((prev) => ({ ...prev, name: event.target.value }))
                }
              />
            </label>

            {!modeFieldPresent && (providerSchema?.modes?.length ?? 0) > 0 ? (
              <label className="flex flex-col gap-1 text-xs text-muted-foreground">
                {t("dialog.mode")}
                <select
                  aria-label={t("dialog.mode")}
                  className={inputClassName}
                  value={form.mode}
                  onChange={(event) =>
                    setForm((prev) => ({ ...prev, mode: event.target.value }))
                  }
                >
                  {(providerSchema?.modes ?? []).map((mode) => (
                    <option key={mode} value={mode}>
                      {mode}
                    </option>
                  ))}
                </select>
              </label>
            ) : null}
          </div>

          {!isCreate ? (
            <p className="text-xs text-muted-foreground">{t("dialog.providerLocked")}</p>
          ) : null}

          {!isCreate ? (
            <ModelRoutePicker
              value={{
                provider: routeValue.provider,
                model: routeValue.model,
                reasoning_effort: routeValue.reasoningEffort,
              }}
              catalog={catalog}
              onChange={(next) =>
                setRouteValue({
                  provider: next.provider ?? "",
                  model: next.model ?? "",
                  reasoningEffort: next.reasoning_effort ?? "",
                })
              }
            />
          ) : null}

          {!isCreate ? (
            <SubscriptionEditor value={eventsValue} onChange={setEventsValue} />
          ) : null}

          <RescanSection
            available={rescanAvailable}
            done={qrConfirmed}
            onStart={() => setRescanOpen(true)}
          />

          {scanMode ? (
            <section className="flex flex-col gap-2.5 rounded-card border border-accent-primary-border bg-accent-primary-soft/40 px-3 py-3">
              <p className="text-xs text-muted-foreground">{t("dialog.scanHint")}</p>
              <Button
                variant="primary"
                size="sm"
                className="self-start"
                onClick={() => void handleStartScan()}
                disabled={qrCreating}
              >
                {qrCreating ? t("dialog.scanStarting") : t("dialog.scanLogin")}
              </Button>
              {qrError ? (
                <p className="text-xs text-accent-orange">
                  {t("dialog.scanFailed", { message: qrError })}
                </p>
              ) : null}
              <details className="text-xs text-muted-foreground">
                <summary className="cursor-pointer select-none">
                  {t("dialog.advanced")}
                </summary>
                <div className="mt-2.5 flex flex-col gap-3">
                  <ConnectorFieldList {...fieldListProps} />
                </div>
              </details>
            </section>
          ) : (
            <fieldset className="flex flex-col gap-3">
              <legend className="text-xs font-medium uppercase tracking-[0.14em] text-muted-foreground">
                {t("dialog.credentials")}
              </legend>
              <ConnectorFieldList {...fieldListProps} />
            </fieldset>
          )}
        </div>

        <footer className="flex items-center justify-end gap-2 border-t border-border px-4 py-3">
          <Button variant="ghost" size="sm" onClick={onClose} disabled={saving}>
            {t("dialog.cancel")}
          </Button>
          <Button variant="primary" size="sm" onClick={handleSubmit} disabled={saving}>
            {saving ? t("dialog.saving") : t("dialog.save")}
          </Button>
        </footer>
          </>
        )}
      </DialogPanel>
    </DialogOverlay>
  );
}
