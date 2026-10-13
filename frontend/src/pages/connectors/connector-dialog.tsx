// 连接器新建/编辑对话框：Schema 驱动渲染（docs/bots/07-config-ux.md §7 控件映射表）。
//
// 控件映射：text→文本框、secret→密码框+留空语义+清除、url→URL 校验、
// bool→复选框、enum→下拉、number→数字框；未知类型降级为文本框并展示原始 key。
// 表单状态与提交体构造在 ./shared.ts（含密钥「留空=不改 / __clear__=清除」单测）。

import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

import type {
  ConnectorFieldError,
  ConnectorFieldSchema,
  ConnectorSchema,
  ConnectorSubmitPayload,
  ConnectorView,
} from "@/types/runtime";

import { Button } from "@/components/ui/button";
import { CheckboxInput } from "@/components/ui/checkbox";
import { DialogOverlay, DialogPanel } from "@/components/ui/dialog-shell";
import { cn } from "@/lib/utils";

import {
  buildSubmitPayload,
  createEmptyForm,
  findProviderSchema,
  formFromConnector,
  switchProvider,
  validateConnectorForm,
  type ConnectorFormError,
  type ConnectorFormState,
} from "./shared";

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
  onClose,
}: ConnectorDialogProps) {
  const { t } = useTranslation("connectors");
  const isCreate = existing === null;
  const [form, setForm] = useState<ConnectorFormState>(() =>
    existing ? formFromConnector(existing) : createEmptyForm(schema),
  );
  const [errors, setErrors] = useState<ConnectorFormError[]>([]);

  useEffect(() => {
    if (!open) {
      return;
    }
    setForm(existing ? formFromConnector(existing) : createEmptyForm(schema));
    setErrors([]);
  }, [open, existing, schema]);

  useEffect(() => {
    if (!open) {
      return;
    }
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        onClose();
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [open, onClose]);

  if (!open) {
    return null;
  }

  const providerSchema = findProviderSchema(schema, form.provider);
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

  const updateValue = (key: string, value: string) => {
    setForm((prev) => ({ ...prev, values: { ...prev.values, [key]: value } }));
  };

  const toggleClearSecret = (key: string, cleared: boolean) => {
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
    onSubmit(buildSubmitPayload(form, providerSchema, { isCreate }));
  };

  const renderControl = (field: ConnectorFieldSchema) => {
    const value = form.values[field.key] ?? "";
    if (field.type === "secret") {
      const cleared = form.clearedSecrets[field.key] === true;
      const configured = (existing?.secret_keys ?? []).includes(field.key);
      return (
        <div className="flex flex-col gap-1.5">
          <input
            type="password"
            autoComplete="new-password"
            aria-label={field.label}
            className={inputClassName}
            placeholder={
              field.placeholder ??
              (configured ? t("dialog.secretConfigured") : undefined)
            }
            value={cleared ? "" : value}
            disabled={cleared}
            onChange={(event) => updateValue(field.key, event.target.value)}
          />
          <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
            {configured && !cleared ? (
              <span>{t("dialog.secretConfigured")}</span>
            ) : null}
            <label className="inline-flex items-center gap-1.5">
              <CheckboxInput
                checked={cleared}
                aria-label={t("dialog.secretClear")}
                onChange={(event) =>
                  toggleClearSecret(field.key, event.target.checked)
                }
              />
              {t("dialog.secretClear")}
            </label>
          </div>
        </div>
      );
    }
    if (field.type === "bool") {
      return (
        <label className="inline-flex items-center gap-2 text-sm text-foreground">
          <CheckboxInput
            checked={value === "true"}
            aria-label={field.label}
            onChange={(event) =>
              updateValue(field.key, event.target.checked ? "true" : "false")
            }
          />
          {field.help ?? field.label}
        </label>
      );
    }
    if (field.type === "enum") {
      return (
        <select
          aria-label={field.label}
          className={inputClassName}
          value={value}
          onChange={(event) => updateValue(field.key, event.target.value)}
        >
          <option value="">{t("dialog.optionUnset")}</option>
          {(field.options ?? []).map((option) => (
            <option key={option.value} value={option.value}>
              {option.label}
            </option>
          ))}
        </select>
      );
    }
    return (
      <div className="flex flex-col gap-1">
        <input
          type={field.type === "number" ? "number" : field.type === "url" ? "url" : "text"}
          aria-label={field.label}
          className={inputClassName}
          placeholder={field.placeholder}
          value={value}
          onChange={(event) => updateValue(field.key, event.target.value)}
        />
        {field.type !== "text" && field.type !== "url" && field.type !== "number" ? (
          <span className="text-xs text-muted-foreground">{field.type}</span>
        ) : null}
      </div>
    );
  };

  return (
    <DialogOverlay onDismiss={saving ? () => undefined : onClose}>
      <DialogPanel
        role="dialog"
        aria-modal="true"
        aria-label={isCreate ? t("dialog.createTitle") : t("dialog.editTitle", { name: existing.name })}
        className="max-w-xl"
      >
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
                    setForm((prev) => switchProvider(prev, schema, event.target.value))
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
                onChange={(event) =>
                  setForm((prev) => ({ ...prev, id: event.target.value }))
                }
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

            {(providerSchema?.modes?.length ?? 0) > 0 ? (
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

          <fieldset className="flex flex-col gap-3">
            <legend className="text-xs font-medium uppercase tracking-[0.14em] text-muted-foreground">
              {t("dialog.credentials")}
            </legend>
            {(providerSchema?.fields ?? []).map((field) => (
              <div key={field.key} className="flex flex-col gap-1">
                <label className="flex flex-col gap-1 text-xs text-muted-foreground">
                  <span>
                    {field.label}
                    {field.required ? <span className="text-accent-orange"> *</span> : null}
                  </span>
                  {renderControl(field)}
                </label>
                {field.help && field.type !== "bool" ? (
                  <span className="text-xs text-muted-foreground">{field.help}</span>
                ) : null}
                {fieldError(field.key) ? (
                  <span className="text-xs text-accent-orange">{fieldError(field.key)}</span>
                ) : null}
              </div>
            ))}
          </fieldset>
        </div>

        <footer className="flex items-center justify-end gap-2 border-t border-border px-4 py-3">
          <Button variant="ghost" size="sm" onClick={onClose} disabled={saving}>
            {t("dialog.cancel")}
          </Button>
          <Button variant="primary" size="sm" onClick={handleSubmit} disabled={saving}>
            {saving ? t("dialog.saving") : t("dialog.save")}
          </Button>
        </footer>
      </DialogPanel>
    </DialogOverlay>
  );
}
