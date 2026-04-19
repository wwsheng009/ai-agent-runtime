import { InfoIcon, type LucideIcon } from "lucide-react";
import { type ReactNode } from "react";

import { Badge } from "@/components/ui/badge";

import { SettingsEmptyState } from "./settings-empty-state";

type ConfigDomainTableColumn<T> = {
  align?: "left" | "right";
  cell: (item: T) => ReactNode;
  className?: string;
  header: string;
};

type ConfigDomainTableProps<T> = {
  actions?: ReactNode;
  columns: ConfigDomainTableColumn<T>[];
  description?: string;
  emptyState: ReactNode;
  getRowKey: (item: T) => string;
  items: T[];
  summary?: ReactNode;
  title: string;
  titleIcon?: LucideIcon;
};

export function ConfigDomainTable<T>({
  actions,
  columns,
  description,
  emptyState,
  getRowKey,
  items,
  summary,
  title,
  titleIcon: TitleIcon,
}: ConfigDomainTableProps<T>) {
  return (
    <div className="rounded-[0.9rem] border border-[var(--border)] bg-[var(--surface-softer)]">
      <div className="flex flex-col gap-2.5 border-b border-[var(--border)] px-3 py-3">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex min-w-0 items-center gap-3">
            {TitleIcon ? (
              <span className="inline-flex size-8 shrink-0 items-center justify-center rounded-[0.7rem] border border-[var(--border)] bg-[var(--surface-solid)] text-[var(--accent-primary)]">
                <TitleIcon size={15} />
              </span>
            ) : null}
            <div className="min-w-0">
              <div className="flex flex-wrap items-center gap-2">
                <div className="truncate text-base font-semibold text-[var(--foreground)]">
                  {title}
                </div>
                {description ? (
                  <span
                    title={description}
                    className="inline-flex size-5 items-center justify-center rounded-[0.6rem] border border-[var(--border)] bg-[var(--surface-solid)] text-[var(--muted-foreground)]"
                  >
                    <InfoIcon size={12} />
                  </span>
                ) : null}
              </div>
              {summary ? <div className="mt-1.5 flex flex-wrap gap-1.5">{summary}</div> : null}
            </div>
          </div>
          {actions ? <div className="flex flex-wrap items-center gap-2">{actions}</div> : null}
        </div>
      </div>

      {items.length === 0 ? (
        <SettingsEmptyState>
          {emptyState}
        </SettingsEmptyState>
      ) : (
        <div className="overflow-auto">
          <table className="min-w-full border-collapse">
            <thead>
              <tr className="border-b border-[var(--border)] bg-[var(--surface-solid)] text-left">
                {columns.map((column) => (
                  <th
                    key={column.header}
                    className={`px-3 py-2.5 app-text-11 uppercase tracking-[0.12em] text-[var(--muted-foreground)] ${
                      column.align === "right" ? "text-right" : "text-left"
                    } ${column.className ?? ""}`}
                  >
                    {column.header}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {items.map((item) => (
                <tr
                  key={getRowKey(item)}
                  className="border-b border-[var(--border)]/70 align-top last:border-b-0"
                >
                  {columns.map((column) => (
                    <td
                      key={column.header}
                      className={`px-3 py-2.5 text-sm text-[var(--foreground)] ${
                        column.align === "right" ? "text-right" : "text-left"
                      } ${column.className ?? ""}`}
                    >
                      {column.cell(item)}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

export function ConfigDomainSummaryBadge({
  children,
}: {
  children: ReactNode;
}) {
  return <Badge>{children}</Badge>;
}
