// Schema 字段列表（凭据区 / 扫码高级设置两处共用）：label + 控件 + help + 错误行。
// 自 connector-dialog 抽出（S2b 触发的 500 行门禁拆分；行为不变）。

import type { ConnectorFieldSchema } from "@/types/runtime";

import { FieldControl } from "./field-control";

interface ConnectorFieldListProps {
  fields: readonly ConnectorFieldSchema[];
  values: Record<string, string>;
  clearedSecrets: Record<string, boolean>;
  configuredSecretKeys: readonly string[];
  fieldError: (key: string) => string | null;
  onValueChange: (key: string, value: string) => void;
  onClearToggle: (key: string, cleared: boolean) => void;
}

export function ConnectorFieldList({
  fields,
  values,
  clearedSecrets,
  configuredSecretKeys,
  fieldError,
  onValueChange,
  onClearToggle,
}: ConnectorFieldListProps) {
  return (
    <>
      {fields.map((field) => (
        <div key={field.key} className="flex flex-col gap-1">
          <label className="flex flex-col gap-1 text-xs text-muted-foreground">
            <span>
              {field.label}
              {field.required ? <span className="text-accent-orange"> *</span> : null}
            </span>
            <FieldControl
              field={field}
              value={values[field.key] ?? ""}
              cleared={clearedSecrets[field.key] === true}
              configured={configuredSecretKeys.includes(field.key)}
              onValueChange={onValueChange}
              onClearToggle={onClearToggle}
            />
          </label>
          {field.help && field.type !== "bool" ? (
            <span className="text-xs text-muted-foreground">{field.help}</span>
          ) : null}
          {fieldError(field.key) ? (
            <span className="text-xs text-accent-orange">{fieldError(field.key)}</span>
          ) : null}
        </div>
      ))}
    </>
  );
}
