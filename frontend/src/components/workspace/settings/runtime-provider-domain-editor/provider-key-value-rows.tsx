/**
 * Headers / 模型映射的结构化行编辑器：不暴露 JSON 文本域，按「键 / 值」行维护。
 *
 * 契约：
 *   * 受控输入是既有的 JSON 字符串草稿（headersJson / modelMappingsJson），保存链路不变；
 *   * 默认只渲染已设置的条目（空对象 = 空态提示），新行按需添加；
 *   * 键名去空白；空键行只是编辑态，不会写进 JSON / YAML；
 *   * 外部（打开其它 provider、自动导入等）改写 JSON 时，行编辑器跟随重同步。
 */

import { PlusIcon, XIcon } from "lucide-react";
import { useEffect, useRef, useState } from "react";

import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

import { editorControlClassName } from "../editor-control-class";
import {
  parseKeyValueEntries,
  serializeKeyValueEntries,
  type KeyValueEntry,
} from "./key-value-rows-model";

type ProviderKeyValueRowsProps = {
  addLabel: string;
  emptyHint: string;
  json: string;
  keyPlaceholder: string;
  onChangeJson: (json: string) => void;
  /** 行级删除按钮的无障碍标签（调用方按语言拼第 N 行）。 */
  removeLabel: (index: number) => string;
  valuePlaceholder: string;
};

export function ProviderKeyValueRows({
  addLabel,
  emptyHint,
  json,
  keyPlaceholder,
  onChangeJson,
  removeLabel,
  valuePlaceholder,
}: ProviderKeyValueRowsProps) {
  const [rows, setRows] = useState<KeyValueEntry[]>(() =>
    parseKeyValueEntries(json),
  );
  const lastJSON = useRef(json);

  useEffect(() => {
    if (json === lastJSON.current) {
      return;
    }
    // 外部改写草稿（打开其它 provider / 导入 / 合并）时重同步；迁移到微任务避免
    // effect 体内同步 setState（react-hooks/set-state-in-effect）。
    let cancelled = false;
    queueMicrotask(() => {
      if (cancelled) {
        return;
      }
      lastJSON.current = json;
      setRows(parseKeyValueEntries(json));
    });
    return () => {
      cancelled = true;
    };
  }, [json]);

  const commit = (nextRows: KeyValueEntry[]) => {
    setRows(nextRows);
    const serialized = serializeKeyValueEntries(nextRows);
    lastJSON.current = serialized;
    onChangeJson(serialized);
  };

  return (
    <div className="space-y-2">
      {rows.length === 0 ? (
        <p className="text-xs leading-5 text-muted-foreground">{emptyHint}</p>
      ) : null}

      {rows.map((row, index) => (
        <div className="flex items-center gap-2" key={index}>
          <input
            aria-label={`${keyPlaceholder} ${index + 1}`}
            className={cn(editorControlClassName, "min-w-0 flex-1")}
            onChange={(event) =>
              commit(
                rows.map((current, currentIndex) =>
                  currentIndex === index
                    ? { ...current, key: event.target.value }
                    : current,
                ),
              )
            }
            placeholder={keyPlaceholder}
            spellCheck={false}
            value={row.key}
          />
          <input
            aria-label={`${valuePlaceholder} ${index + 1}`}
            className={cn(editorControlClassName, "min-w-0 flex-[1.4]")}
            onChange={(event) =>
              commit(
                rows.map((current, currentIndex) =>
                  currentIndex === index
                    ? { ...current, value: event.target.value }
                    : current,
                ),
              )
            }
            placeholder={valuePlaceholder}
            spellCheck={false}
            value={row.value}
          />
          <Button
            aria-label={removeLabel(index + 1)}
            onClick={() => commit(rows.filter((_, currentIndex) => currentIndex !== index))}
            size="icon"
            variant="ghost"
          >
            <XIcon size={14} />
          </Button>
        </div>
      ))}

      <Button
        onClick={() => commit([...rows, { key: "", value: "" }])}
        size="sm"
        variant="secondary"
      >
        <PlusIcon size={14} />
        {addLabel}
      </Button>
    </div>
  );
}
