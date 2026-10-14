// Schema 字段 → 控件的机械映射（docs/bots/07-config-ux.md §7 控件映射表）：
// text→文本框、secret→密码框+留空语义+清除、url→URL 校验、bool→复选框、
// enum→下拉、number→数字框；未知类型降级为文本框并展示原始 key。
// 从 connector-dialog.tsx 拆出（P0-2 行数预算）；状态回调经 props 传入。

import { useTranslation } from "react-i18next";

import { CheckboxInput } from "@/components/ui/checkbox";
import type { ConnectorFieldSchema } from "@/types/runtime";

const inputClassName =
  "h-9 w-full rounded-control border border-border bg-surface-soft px-2.5 text-sm text-foreground outline-none placeholder:text-muted-foreground focus:border-accent-primary disabled:opacity-60";

interface FieldControlProps {
  field: ConnectorFieldSchema;
  /** 当前输入值（密钥字段为空串 = 保持原值语义由 cleared 承载）。 */
  value: string;
  /** 密钥字段：是否勾选「清除」（勾选后输入框禁用）。 */
  cleared: boolean;
  /** 密钥字段：是否已配置过（展示「已配置」提示）。 */
  configured: boolean;
  onValueChange: (key: string, value: string) => void;
  onClearToggle: (key: string, cleared: boolean) => void;
}

export function FieldControl({
  field,
  value,
  cleared,
  configured,
  onValueChange,
  onClearToggle,
}: FieldControlProps) {
  const { t } = useTranslation("connectors");
  if (field.type === "secret") {
    return (
      <div className="flex flex-col gap-1.5">
        <input
          type="password"
          autoComplete="new-password"
          aria-label={field.label}
          className={inputClassName}
          placeholder={
            field.placeholder ?? (configured ? t("dialog.secretConfigured") : undefined)
          }
          value={cleared ? "" : value}
          disabled={cleared}
          onChange={(event) => onValueChange(field.key, event.target.value)}
        />
        <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
          {configured && !cleared ? <span>{t("dialog.secretConfigured")}</span> : null}
          <label className="inline-flex items-center gap-1.5">
            <CheckboxInput
              checked={cleared}
              aria-label={t("dialog.secretClear")}
              onChange={(event) => onClearToggle(field.key, event.target.checked)}
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
            onValueChange(field.key, event.target.checked ? "true" : "false")
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
        onChange={(event) => onValueChange(field.key, event.target.value)}
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
        onChange={(event) => onValueChange(field.key, event.target.value)}
      />
      {field.type !== "text" && field.type !== "url" && field.type !== "number" ? (
        <span className="text-xs text-muted-foreground">{field.type}</span>
      ) : null}
    </div>
  );
}
