import { useState } from "react";

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
        className="rounded-[0.9rem] border border-[var(--border)] bg-[var(--surface-softer)]"
        open={nextOpen}
      >
        <summary className="cursor-pointer list-none px-3 py-2.5">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="min-w-0">
              <div className="text-sm font-semibold text-[var(--foreground)]">
                {label}
              </div>
              <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                {valuePathLabel}
              </div>
            </div>
            <div className="flex items-center gap-2">
              <Badge>{entries.length} fields</Badge>
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
                  删除
                </Button>
              ) : null}
            </div>
          </div>
        </summary>

        <div className="space-y-2.5 border-t border-[var(--border)] px-3 py-3">
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
              当前对象为空，可以直接添加字段。
            </SettingsEmptyState>
          )}

          <div className="rounded-[0.75rem] border border-[var(--border)] bg-[var(--surface-solid)] px-3 py-2.5">
            <div className="app-text-11 uppercase tracking-[0.12em] text-[var(--muted-foreground)]">
              Add field
            </div>
            <div className="mt-2.5 grid gap-2.5 md:grid-cols-[minmax(0,1fr)_8rem_auto]">
              <input
                className={editorControlClassName}
                placeholder="new_key"
                value={newFieldKey}
                onChange={(event) => setNewFieldKey(event.target.value)}
              />
              <Select
                ariaLabel="新字段类型"
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
                添加字段
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
        className="rounded-[0.9rem] border border-[var(--border)] bg-[var(--surface-softer)]"
        open={nextOpen}
      >
        <summary className="cursor-pointer list-none px-3 py-2.5">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="min-w-0">
              <div className="text-sm font-semibold text-[var(--foreground)]">
                {label}
              </div>
              <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                {valuePathLabel}
              </div>
            </div>
            <div className="flex items-center gap-2">
              <Badge>{arrayValue.length} items</Badge>
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
                  删除
                </Button>
              ) : null}
            </div>
          </div>
        </summary>

        <div className="space-y-2.5 border-t border-[var(--border)] px-3 py-3">
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
              当前数组为空，可以先插入一个项目。
            </SettingsEmptyState>
          )}

          <div className="rounded-[0.75rem] border border-[var(--border)] bg-[var(--surface-solid)] px-3 py-2.5">
            <div className="app-text-11 uppercase tracking-[0.12em] text-[var(--muted-foreground)]">
              Add item
            </div>
            <div className="mt-2.5 flex flex-wrap items-center gap-2.5">
              <Select
                ariaLabel="新数组项类型"
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
                追加项目
              </Button>
            </div>
          </div>
        </div>
      </details>
    );
  }

  return (
    <div className="rounded-[0.9rem] border border-[var(--border)] bg-[var(--surface-softer)] px-3 py-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="min-w-0">
          <div className="text-sm font-semibold text-[var(--foreground)]">{label}</div>
          <div className="mt-1 text-xs text-[var(--muted-foreground)]">
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
              删除
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
            当前值: {String(value === true)}
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
