import { useState } from "react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/select";
import { cn } from "@/lib/utils";

import {
  type ConfigPathSegment,
  type ConfigValueKind,
  convertConfigValueKind,
  defaultConfigValueForKind,
  inferConfigValueKind,
} from "./runtime-config-editor-utils";
import { editorControlClassName } from "./editor-control-class";
import { isConfigRecord } from "./runtime-provider-config-utils";
import { SettingsEmptyState } from "./settings-empty-state";

const configValueKindOptions: Array<{
  value: ConfigValueKind;
  label: string;
}> = [
  { value: "object", label: "object" },
  { value: "array", label: "array" },
  { value: "string", label: "string" },
  { value: "number", label: "number" },
  { value: "boolean", label: "boolean" },
  { value: "null", label: "null" },
];

export type ConfigNodeEditorProps = {
  depth?: number;
  label: string;
  onChange: (path: ConfigPathSegment[], nextValue: unknown) => void;
  onDelete: (path: ConfigPathSegment[]) => void;
  path: ConfigPathSegment[];
  value: unknown;
};

export function BackendConfigNodeEditor({
  depth = 0,
  label,
  onChange,
  onDelete,
  path,
  value,
}: ConfigNodeEditorProps) {
  const { t } = useTranslation("runtimeConfig");
  const kind = inferConfigValueKind(value);
  const [newFieldKey, setNewFieldKey] = useState("");
  const [newFieldKind, setNewFieldKind] = useState<ConfigValueKind>("string");
  const [newArrayItemKind, setNewArrayItemKind] =
    useState<ConfigValueKind>("string");

  const canDelete = path.length > 0;
  const valuePathLabel =
    path.length > 0 ? path.map((segment) => String(segment)).join(".") : "root";
  const nextOpen = depth < 2;

  if (kind === "object") {
    const objectValue = isConfigRecord(value) ? value : {};
    const entries = Object.entries(objectValue);

    return (
      <details
        className="rounded-panel border border-border bg-surface-softer"
        open={nextOpen}
      >
        <summary className="cursor-pointer list-none px-3 py-2.5">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="min-w-0">
              <div className="text-sm font-semibold text-foreground">
                {label}
              </div>
              <div className="mt-1 text-xs text-muted-foreground">
                {valuePathLabel}
              </div>
            </div>
            <div className="flex items-center gap-2">
              <Badge>
                {t("editor.configNode.fieldsBadge", {
                  count: entries.length,
                })}
              </Badge>
              <Select
                ariaLabel={`${label} value kind`}
                value={kind}
                onChange={(nextKind) =>
                  onChange(
                    path,
                    convertConfigValueKind(
                      objectValue,
                      nextKind as ConfigValueKind,
                    ),
                  )
                }
                options={configValueKindOptions}
                className="w-auto"
                triggerClassName="w-auto min-w-24 px-3 py-1 text-xs"
                menuClassName="max-w-[10rem]"
                optionClassName="text-xs"
              />
              {canDelete ? (
                <Button variant="ghost" size="sm" onClick={() => onDelete(path)}>
                  {t("editor.configNode.delete")}
                </Button>
              ) : null}
            </div>
          </div>
        </summary>

        <div className="space-y-2.5 border-t border-border px-3 py-3">
          {entries.length > 0 ? (
            entries.map(([childKey, childValue]) => (
              <BackendConfigNodeEditor
                key={`${valuePathLabel}.${childKey}`}
                depth={depth + 1}
                label={childKey}
                onChange={onChange}
                onDelete={onDelete}
                path={[...path, childKey]}
                value={childValue}
              />
            ))
          ) : (
            <SettingsEmptyState variant="dashed">
              {t("editor.configNode.emptyObject")}
            </SettingsEmptyState>
          )}

          <div className="rounded-[0.75rem] border border-border bg-surface-solid px-3 py-2.5">
            <div className="app-text-11 uppercase tracking-[0.12em] text-muted-foreground">
              {t("editor.configNode.addField")}
            </div>
            <div className="mt-2.5 grid gap-2.5 md:grid-cols-[minmax(0,1fr)_8rem_auto]">
              <input
                className={editorControlClassName}
                placeholder={t("editor.configNode.newFieldPlaceholder")}
                value={newFieldKey}
                onChange={(event) => setNewFieldKey(event.target.value)}
              />
              <Select
                ariaLabel={t("editor.configNode.newFieldKindAriaLabel")}
                value={newFieldKind}
                onChange={(nextKind) => setNewFieldKind(nextKind as ConfigValueKind)}
                options={configValueKindOptions}
                className="w-full"
                triggerClassName="w-full text-sm"
                optionClassName="text-sm"
              />
              <Button
                variant="secondary"
                onClick={() => {
                  const nextKey = newFieldKey.trim();
                  if (!nextKey) {
                    return;
                  }
                  onChange(path, {
                    ...objectValue,
                    [nextKey]: defaultConfigValueForKind(newFieldKind),
                  });
                  setNewFieldKey("");
                }}
              >
                {t("editor.configNode.addField")}
              </Button>
            </div>
          </div>
        </div>
      </details>
    );
  }

  if (kind === "array") {
    const arrayValue = Array.isArray(value) ? value : [];

    return (
      <details
        className="rounded-panel border border-border bg-surface-softer"
        open={nextOpen}
      >
        <summary className="cursor-pointer list-none px-3 py-2.5">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="min-w-0">
              <div className="text-sm font-semibold text-foreground">
                {label}
              </div>
              <div className="mt-1 text-xs text-muted-foreground">
                {valuePathLabel}
              </div>
            </div>
            <div className="flex items-center gap-2">
              <Badge>
                {t("editor.configNode.itemsBadge", {
                  count: arrayValue.length,
                })}
              </Badge>
              <Select
                ariaLabel={`${label} value kind`}
                value={kind}
                onChange={(nextKind) =>
                  onChange(
                    path,
                    convertConfigValueKind(
                      arrayValue,
                      nextKind as ConfigValueKind,
                    ),
                  )
                }
                options={configValueKindOptions}
                className="w-auto"
                triggerClassName="w-auto min-w-24 px-3 py-1 text-xs"
                menuClassName="max-w-[10rem]"
                optionClassName="text-xs"
              />
              {canDelete ? (
                <Button variant="ghost" size="sm" onClick={() => onDelete(path)}>
                  {t("editor.configNode.delete")}
                </Button>
              ) : null}
            </div>
          </div>
        </summary>

        <div className="space-y-2.5 border-t border-border px-3 py-3">
          {arrayValue.length > 0 ? (
            arrayValue.map((item, index) => (
              <BackendConfigNodeEditor
                key={`${valuePathLabel}.${index}`}
                depth={depth + 1}
                label={`#${index + 1}`}
                onChange={onChange}
                onDelete={onDelete}
                path={[...path, index]}
                value={item}
              />
            ))
          ) : (
            <SettingsEmptyState variant="dashed">
              {t("editor.configNode.emptyArray")}
            </SettingsEmptyState>
          )}

          <div className="rounded-[0.75rem] border border-border bg-surface-solid px-3 py-2.5">
            <div className="app-text-11 uppercase tracking-[0.12em] text-muted-foreground">
              {t("editor.configNode.addItem")}
            </div>
            <div className="mt-2.5 flex flex-wrap items-center gap-2.5">
              <Select
                ariaLabel={t("editor.configNode.newArrayItemKindAriaLabel")}
                value={newArrayItemKind}
                onChange={(nextKind) =>
                  setNewArrayItemKind(nextKind as ConfigValueKind)
                }
                options={configValueKindOptions}
                className="w-auto"
                triggerClassName="w-auto min-w-24 text-sm"
                menuClassName="max-w-[10rem]"
                optionClassName="text-sm"
              />
              <Button
                variant="secondary"
                onClick={() =>
                  onChange(path, [
                    ...arrayValue,
                    defaultConfigValueForKind(newArrayItemKind),
                  ])
                }
              >
                {t("editor.configNode.addItem")}
              </Button>
            </div>
          </div>
        </div>
      </details>
    );
  }

  return (
    <div className="rounded-panel border border-border bg-surface-softer px-3 py-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="min-w-0">
          <div className="text-sm font-semibold text-foreground">{label}</div>
          <div className="mt-1 text-xs text-muted-foreground">
            {valuePathLabel}
          </div>
        </div>
        <div className="flex items-center gap-2">
          <Select
            ariaLabel={`${label} value kind`}
            value={kind}
            onChange={(nextKind) =>
              onChange(
                path,
                convertConfigValueKind(value, nextKind as ConfigValueKind),
              )
            }
            options={configValueKindOptions}
            className="w-auto"
            triggerClassName="w-auto min-w-24 px-3 py-1 text-xs"
            menuClassName="max-w-[10rem]"
            optionClassName="text-xs"
          />
          {canDelete ? (
            <Button variant="ghost" size="sm" onClick={() => onDelete(path)}>
              {t("editor.configNode.delete")}
            </Button>
          ) : null}
        </div>
      </div>

      <div className="mt-3">
        {kind === "boolean" ? (
          <Button
            variant="secondary"
            onClick={() => onChange(path, value !== true)}
          >
            {t("editor.configNode.currentValue")} {String(value === true)}
          </Button>
        ) : null}

        {kind === "number" ? (
          <input
            className={editorControlClassName}
            type="number"
            value={typeof value === "number" ? value : 0}
            onChange={(event) =>
              onChange(path, Number(event.target.value || "0"))
            }
          />
        ) : null}

        {kind === "string" ? (
          typeof value === "string" &&
          (value.includes("\n") || value.length > 80 ? (
            <textarea
              className={cn(editorControlClassName, "min-h-28 resize-y font-mono leading-6")}
              value={value}
              onChange={(event) => onChange(path, event.target.value)}
            />
          ) : (
            <input
              className={editorControlClassName}
              value={value}
              onChange={(event) => onChange(path, event.target.value)}
            />
          ))
        ) : null}
      </div>
    </div>
  );
}
