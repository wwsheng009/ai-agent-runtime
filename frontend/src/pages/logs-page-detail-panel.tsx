import { CheckIcon, CopyIcon } from "lucide-react";

import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import {
  isRuntimeLogIdentifierQueryActive,
} from "@/pages/logs-page-shared";
import type { RuntimeLogEntry } from "@/types/runtime";

type LogsPageDetailIdentifierRow = {
  key: string;
  label: string;
  value: string;
};

type LogsPageDetailMetadataRow = {
  label: string;
  value: string;
};

type LogsPageDetailPanelProps = {
  copiedSection: string | null;
  extraFieldsText: string;
  identifierRows: LogsPageDetailIdentifierRow[];
  metadataRows: LogsPageDetailMetadataRow[];
  metadataText: string;
  onClearQuery: () => void;
  onCopy: (sectionKey: string, value: string) => void | Promise<void>;
  onToggleIdentifierQuery: (value: string) => void;
  query: string;
  rawJsonText: string;
  responsePreviewText: string;
  selectedEntry: RuntimeLogEntry;
  selectedEntrySubtitle: string;
  selectedLevelTone: string;
};

export function LogsPageDetailPanel({
  copiedSection,
  extraFieldsText,
  identifierRows,
  metadataRows,
  metadataText,
  onClearQuery,
  onCopy,
  onToggleIdentifierQuery,
  query,
  rawJsonText,
  responsePreviewText,
  selectedEntry,
  selectedEntrySubtitle,
  selectedLevelTone,
}: LogsPageDetailPanelProps) {
  return (
    <div className="flex-1 space-y-2.5 overflow-y-auto px-3 py-3">
      <div className="rounded-[0.85rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
        <div className="flex flex-wrap items-start justify-between gap-2">
          <div className="flex flex-wrap items-center gap-2">
            <span
              className={cn(
                "inline-flex items-center rounded-[0.65rem] border px-2 py-1 app-text-10 font-semibold uppercase tracking-[0.14em]",
                selectedLevelTone,
              )}
            >
              {selectedEntry.level || "log"}
            </span>
            <span className="app-text-12 text-[var(--muted-foreground)]">
              cursor {selectedEntry.cursor}
            </span>
          </div>
          <CopyActionButton
            copied={copiedSection === "summary"}
            label="复制日志"
            onClick={() => onCopy("summary", rawJsonText)}
          />
        </div>
        <h2 className="mt-2.5 text-lg font-semibold tracking-[-0.02em]">
          {selectedEntry.message || selectedEntry.raw_text}
        </h2>
        <p className="mt-2 app-text-13 leading-5 text-[var(--muted-foreground)]">
          {selectedEntrySubtitle}
        </p>
      </div>

      {identifierRows.length > 0 ? (
        <div className="rounded-[0.85rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div>
              <div className="app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
                Identifiers
              </div>
              <div className="mt-1 app-text-11 text-[var(--muted-foreground)]">
                复制标识，或把当前值直接写回顶部搜索框继续追踪。
              </div>
            </div>
            {query.trim() ? (
              <Button
                variant="ghost"
                size="sm"
                className="h-7 rounded-[0.65rem] border border-[var(--border)] bg-black/10 px-2.5 text-[var(--muted-foreground)] hover:bg-black/20 hover:text-[var(--foreground)]"
                onClick={onClearQuery}
              >
                清除搜索
              </Button>
            ) : null}
          </div>

          <div className="mt-3 space-y-2">
            {identifierRows.map((row) => {
              const active = isRuntimeLogIdentifierQueryActive(query, row.value);
              return (
                <div
                  key={row.key}
                  className={cn(
                    "flex flex-col gap-3 rounded-[0.75rem] border px-3 py-2.5 sm:flex-row sm:items-start sm:justify-between",
                    active
                      ? "border-[var(--accent-primary-border)] bg-[var(--accent-primary-soft)]"
                      : "border-[var(--border)] bg-black/10",
                  )}
                >
                  <div className="min-w-0">
                    <div className="app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
                      {row.label}
                    </div>
                    <div className="mt-1.5 break-all font-mono app-text-12-5 leading-5 text-[var(--foreground)]">
                      {row.value}
                    </div>
                  </div>
                  <div className="flex flex-wrap items-center gap-2 sm:justify-end">
                    <CopyActionButton
                      copied={copiedSection === `identifier:${row.key}`}
                      label="复制值"
                      onClick={() => onCopy(`identifier:${row.key}`, row.value)}
                    />
                    <Button
                      variant={active ? "secondary" : "ghost"}
                      size="sm"
                      className={cn(
                        "h-7 rounded-[0.65rem] border px-2.5 app-text-11",
                        active
                          ? "border-[var(--accent-primary-border)] bg-[var(--accent-primary-soft)] text-[var(--accent-primary)]"
                          : "border-[var(--border)] bg-black/10 text-[var(--muted-foreground)] hover:bg-black/20 hover:text-[var(--foreground)]",
                      )}
                      onClick={() => onToggleIdentifierQuery(row.value)}
                    >
                      {active ? "取消过滤" : "过滤同值"}
                    </Button>
                  </div>
                </div>
              );
            })}
          </div>
        </div>
      ) : null}

      <div className="rounded-[0.85rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
        <div className="flex items-center justify-between gap-3">
          <div className="app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
            Metadata
          </div>
          <CopyActionButton
            copied={copiedSection === "metadata"}
            label="复制元数据"
            onClick={() => onCopy("metadata", metadataText)}
          />
        </div>
        <dl className="mt-3 grid gap-x-4 gap-y-2.5 sm:grid-cols-2 2xl:grid-cols-3">
          {metadataRows.map((row) => (
            <div
              key={row.label}
              className="min-w-0 border-t border-[var(--border)]/60 pt-2.5 first:border-t-0 first:pt-0"
            >
              <dt className="app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
                {row.label}
              </dt>
              <dd className="mt-1.5 break-all app-text-13 leading-5">
                {row.value}
              </dd>
            </div>
          ))}
        </dl>
      </div>

      {selectedEntry.response_body_preview ? (
        <div className="rounded-[0.85rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
          <div className="flex items-center justify-between gap-3">
            <div className="app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
              Response Preview
            </div>
            <CopyActionButton
              copied={copiedSection === "response_preview"}
              label="复制预览"
              onClick={() => onCopy("response_preview", responsePreviewText)}
            />
          </div>
          <pre className="app-code-surface mt-2 overflow-x-auto whitespace-pre-wrap break-words rounded-[0.75rem] border border-[var(--border)] bg-black/25 p-3 font-mono text-[var(--foreground)]">
            {selectedEntry.response_body_preview}
          </pre>
        </div>
      ) : null}

      {selectedEntry.fields && Object.keys(selectedEntry.fields).length > 0 ? (
        <div className="rounded-[0.85rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
          <div className="flex items-center justify-between gap-3">
            <div className="app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
              Extra Fields
            </div>
            <CopyActionButton
              copied={copiedSection === "extra_fields"}
              label="复制字段"
              onClick={() => onCopy("extra_fields", extraFieldsText)}
            />
          </div>
          <pre className="app-code-surface mt-2 overflow-x-auto whitespace-pre-wrap break-words rounded-[0.75rem] border border-[var(--border)] bg-black/25 p-3 font-mono text-[var(--foreground)]">
            {JSON.stringify(selectedEntry.fields, null, 2)}
          </pre>
        </div>
      ) : null}

      <div className="rounded-[0.85rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
        <div className="flex items-center justify-between gap-3">
          <div className="app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
            Raw JSON
          </div>
          <CopyActionButton
            copied={copiedSection === "raw_json"}
            label="复制 JSON"
            onClick={() => onCopy("raw_json", rawJsonText)}
          />
        </div>
        <pre className="app-code-surface mt-2 overflow-x-auto whitespace-pre-wrap break-words rounded-[0.75rem] border border-[var(--border)] bg-black/25 p-3 font-mono text-[var(--foreground)]">
          {selectedEntry.raw
            ? JSON.stringify(selectedEntry.raw, null, 2)
            : selectedEntry.raw_text}
        </pre>
      </div>
    </div>
  );
}

type CopyActionButtonProps = {
  copied: boolean;
  label: string;
  onClick: () => void;
};

function CopyActionButton({
  copied,
  label,
  onClick,
}: CopyActionButtonProps) {
  return (
    <Button
      variant="ghost"
      size="sm"
      className="h-7 rounded-[0.65rem] border border-[var(--border)] bg-black/10 px-2.5 text-[var(--muted-foreground)] hover:bg-black/20 hover:text-[var(--foreground)]"
      onClick={onClick}
    >
      {copied ? <CheckIcon size={13} /> : <CopyIcon size={13} />}
      {copied ? "已复制" : label}
    </Button>
  );
}
