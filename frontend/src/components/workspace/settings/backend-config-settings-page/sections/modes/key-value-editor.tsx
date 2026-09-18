// 结构化键值行编辑器：每行「键 / 值」输入 + 删除按钮，底部可追加新行。
//
// 边界：
// - 纯展示组件，不持有状态；行增删改都通过 onChange 交回调用方；
// - 空键与重复键在行内以 aria-invalid + 提示文案暴露（文案由调用方经
//   keyPlaceholder/valuePlaceholder 与 i18n 提供）；
// - separator 用于展示行内分隔符，并透传给多行粘贴解析（见 key-value-rows.ts）；
// - 行模型与粘贴纯逻辑在 key-value-rows.ts（组件文件只导出组件）。

import { PlusIcon, Trash2Icon } from "lucide-react";
import { type ClipboardEvent } from "react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

import { editorControlClassName } from "../../../editor-control-class";
import { SettingsIconActionButton } from "../../../settings-action-group";
import {
  applyKeyValuePaste,
  createKeyValueRow,
  type KeyValueRow,
} from "./key-value-rows";

export type KeyValueEditorProps = {
  rows: KeyValueRow[];
  onChange: (rows: KeyValueRow[]) => void;
  addLabel: string;
  keyPlaceholder: string;
  valuePlaceholder: string;
  separator: "=" | ":";
  disabled?: boolean;
};

/** 重复键（trim 后精确匹配、忽略空键）涉及的所有行下标。 */
function duplicateKeyIndexes(rows: KeyValueRow[]): Set<number> {
  const firstIndexByKey = new Map<string, number>();
  const duplicates = new Set<number>();
  rows.forEach((row, index) => {
    const key = row.key.trim();
    if (!key) {
      return;
    }
    const firstIndex = firstIndexByKey.get(key);
    if (firstIndex === undefined) {
      firstIndexByKey.set(key, index);
      return;
    }
    duplicates.add(firstIndex);
    duplicates.add(index);
  });
  return duplicates;
}

export function KeyValueEditor({
  rows,
  onChange,
  addLabel,
  keyPlaceholder,
  valuePlaceholder,
  separator,
  disabled = false,
}: KeyValueEditorProps) {
  const { t } = useTranslation("runtimeConfig");
  const duplicateIndexes = duplicateKeyIndexes(rows);

  function replaceRow(index: number, patch: Partial<KeyValueRow>) {
    onChange(
      rows.map((row, rowIndex) =>
        rowIndex === index ? { ...row, ...patch } : row,
      ),
    );
  }

  function handleValuePaste(
    index: number,
    event: ClipboardEvent<HTMLInputElement>,
  ) {
    const target = rows[index];
    if (!target) {
      return;
    }
    const pasted = applyKeyValuePaste(
      rows,
      target.id,
      event.clipboardData.getData("text/plain"),
      separator,
    );
    if (!pasted) {
      return;
    }
    event.preventDefault();
    onChange(pasted);
  }

  return (
    <div className="grid gap-2">
      {rows.map((row, index) => {
        const isKeyMissing = row.key.trim() === "";
        const isDuplicate = duplicateIndexes.has(index);
        const invalid = isKeyMissing || isDuplicate;
        const errorText = isDuplicate
          ? t("mcp.kv.duplicateKey")
          : isKeyMissing
            ? t("mcp.kv.keyRequired")
            : null;
        const errorId = `kv-error-${row.id}`;
        return (
          <div className="grid gap-1" data-kv-row={index} key={row.id}>
            <div className="flex items-center gap-1.5">
              <input
                aria-describedby={errorText ? errorId : undefined}
                aria-invalid={invalid || undefined}
                aria-label={`${keyPlaceholder} ${index + 1}`}
                className={cn(
                  editorControlClassName,
                  "disabled:opacity-60",
                  invalid && "border-accent-orange",
                )}
                disabled={disabled}
                placeholder={keyPlaceholder}
                value={row.key}
                onChange={(event) => replaceRow(index, { key: event.target.value })}
              />
              <span aria-hidden="true" className="text-sm text-muted-foreground">
                {separator}
              </span>
              <input
                aria-label={`${valuePlaceholder} ${index + 1}`}
                className={cn(editorControlClassName, "disabled:opacity-60")}
                disabled={disabled}
                placeholder={valuePlaceholder}
                value={row.value}
                onChange={(event) =>
                  replaceRow(index, { value: event.target.value })
                }
                onPaste={(event) => handleValuePaste(index, event)}
              />
              <SettingsIconActionButton
                disabled={disabled}
                label={`${t("mcp.kv.removeRow")} ${index + 1}`}
                onClick={() =>
                  onChange(rows.filter((_, rowIndex) => rowIndex !== index))
                }
              >
                <Trash2Icon size={14} />
              </SettingsIconActionButton>
            </div>
            {errorText ? (
              <p className="text-xs text-accent-orange" id={errorId}>
                {errorText}
              </p>
            ) : null}
          </div>
        );
      })}

      <div>
        <Button
          disabled={disabled}
          size="sm"
          type="button"
          variant="secondary"
          onClick={() => onChange([...rows, createKeyValueRow()])}
        >
          <PlusIcon size={14} />
          {addLabel}
        </Button>
      </div>

      <p className="text-xs text-muted-foreground">{t("mcp.kv.pasteHint")}</p>
    </div>
  );
}
