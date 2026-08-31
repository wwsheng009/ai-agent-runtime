import {
  ChevronLeftIcon,
  ChevronRightIcon,
  InfoIcon,
  SearchIcon,
  type LucideIcon,
} from "lucide-react";
import { useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";

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
  emptySearchState?: ReactNode;
  emptyState: ReactNode;
  getRowKey: (item: T) => string;
  getSearchText?: (item: T) => string;
  items: T[];
  pageSize?: number;
  searchable?: boolean;
  summary?: ReactNode;
  title: string;
  titleIcon?: LucideIcon;
};

function defaultSearchText<T>(item: T): string {
  try {
    return JSON.stringify(item);
  } catch {
    return String(item);
  }
}

export function ConfigDomainTable<T>({
  actions,
  columns,
  description,
  emptySearchState,
  emptyState,
  getRowKey,
  getSearchText,
  items,
  pageSize,
  searchable = false,
  summary,
  title,
  titleIcon: TitleIcon,
}: ConfigDomainTableProps<T>) {
  const { t } = useTranslation("runtimeConfig");
  const [searchText, setSearchText] = useState("");
  const [page, setPage] = useState(1);

  const searchImpl = getSearchText ?? defaultSearchText;
  const normalizedQuery = searchText.trim().toLowerCase();

  const filteredItems =
    !searchable || normalizedQuery.length === 0
      ? items
      : items.filter((item) =>
          searchImpl(item).toLowerCase().includes(normalizedQuery),
        );

  const pageCount = pageSize
    ? Math.max(1, Math.ceil(filteredItems.length / pageSize))
    : 1;
  const currentPage = Math.min(page, pageCount);
  const startIndex = pageSize ? (currentPage - 1) * pageSize : 0;
  const visibleItems = pageSize
    ? filteredItems.slice(startIndex, startIndex + pageSize)
    : filteredItems;

  const hasPagination = Boolean(pageSize) && pageCount > 1;

  const pageButtonClass =
    "inline-flex size-7 items-center justify-center rounded-[0.6rem] border border-[var(--border)] bg-[var(--surface-solid)] text-[var(--muted-foreground)] transition hover:text-[var(--foreground)] disabled:cursor-not-allowed disabled:opacity-40";

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
        {searchable ? (
          <div className="relative w-full max-w-[19rem]">
            <SearchIcon
              size={14}
              className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-[var(--muted-foreground)]"
            />
            <input
              type="search"
              value={searchText}
              onChange={(event) => {
                setSearchText(event.target.value);
                setPage(1);
              }}
              placeholder={t("editor.table.searchPlaceholder")}
              aria-label={t("editor.table.searchPlaceholder")}
              className="h-9 w-full rounded-[0.7rem] border border-[var(--border)] bg-[var(--surface-solid)] pl-8 pr-3 text-sm text-[var(--foreground)] outline-none transition placeholder:text-[var(--muted-foreground)] focus:border-[var(--accent-primary-border)] focus:ring-2 focus:ring-[var(--ring)]"
            />
          </div>
        ) : null}
      </div>

      {items.length === 0 ? (
        <SettingsEmptyState>
          {emptyState}
        </SettingsEmptyState>
      ) : filteredItems.length === 0 ? (
        <SettingsEmptyState>{emptySearchState ?? t("editor.table.noSearchResults")}</SettingsEmptyState>
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
              {visibleItems.map((item) => (
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

      {hasPagination ? (
        <div className="flex flex-wrap items-center justify-between gap-2 border-t border-[var(--border)] px-3 py-2.5">
          <span className="app-text-11 text-[var(--muted-foreground)]">
            {t("editor.table.pagination.showing", {
              start: String(startIndex + 1),
              end: String(
                Math.min(startIndex + (pageSize ?? 0), filteredItems.length),
              ),
              total: String(filteredItems.length),
            })}
          </span>
          <div className="flex items-center gap-1.5">
            <button
              type="button"
              disabled={currentPage <= 1}
              onClick={() => setPage(currentPage - 1)}
              aria-label={t("editor.table.pagination.prev")}
              title={t("editor.table.pagination.prev")}
              className={pageButtonClass}
            >
              <ChevronLeftIcon size={14} />
            </button>
            <span className="app-text-11 min-w-[4.5rem] text-center text-[var(--muted-foreground)]">
              {currentPage} / {pageCount}
            </span>
            <button
              type="button"
              disabled={currentPage >= pageCount}
              onClick={() => setPage(currentPage + 1)}
              aria-label={t("editor.table.pagination.next")}
              title={t("editor.table.pagination.next")}
              className={pageButtonClass}
            >
              <ChevronRightIcon size={14} />
            </button>
          </div>
        </div>
      ) : null}
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