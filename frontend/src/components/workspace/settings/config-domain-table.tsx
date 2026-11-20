import {
  ArrowDownIcon,
  ArrowUpDownIcon,
  ArrowUpIcon,
  ChevronLeftIcon,
  ChevronRightIcon,
  InfoIcon,
  SearchIcon,
  type LucideIcon,
} from "lucide-react";
import { useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";

import { SettingsEmptyState } from "./settings-empty-state";

type ConfigDomainTableColumn<T> = {
  align?: "left" | "right";
  cell: (item: T) => ReactNode;
  className?: string;
  header: string;
  sortKey?: string;
  sortValue?: (item: T) => string | number | boolean;
  sticky?: "right";
};

type ConfigDomainTableFilter<T> = {
  getValue: (item: T) => string;
  key: string;
  label: string;
  options: Array<{ label: string; value: string }>;
};

type ConfigDomainTableProps<T> = {
  actions?: ReactNode;
  columns: ConfigDomainTableColumn<T>[];
  description?: string;
  emptySearchState?: ReactNode;
  emptyState: ReactNode;
  getRowKey: (item: T) => string;
  getSearchText?: (item: T) => string;
  filters?: ConfigDomainTableFilter<T>[];
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
  filters = [],
  pageSize,
  searchable = false,
  summary,
  title,
  titleIcon: TitleIcon,
}: ConfigDomainTableProps<T>) {
  const { t } = useTranslation("runtimeConfig");
  const [searchText, setSearchText] = useState("");
  const [page, setPage] = useState(1);
  const [filterValues, setFilterValues] = useState<Record<string, string>>({});
  const [sort, setSort] = useState<{
    key: string;
    direction: "ascending" | "descending";
  } | null>(null);

  const searchImpl = getSearchText ?? defaultSearchText;
  const normalizedQuery = searchText.trim().toLowerCase();

  const filteredItems = items.filter((item) => {
    const matchesSearch =
      !searchable ||
      normalizedQuery.length === 0 ||
      searchImpl(item).toLowerCase().includes(normalizedQuery);
    const matchesFilters = filters.every((filter) => {
      const selected = filterValues[filter.key];
      return !selected || filter.getValue(item) === selected;
    });
    return matchesSearch && matchesFilters;
  });

  const sortColumn = columns.find(
    (column) => (column.sortKey ?? column.header) === sort?.key,
  );
  const sortedItems =
    sort && sortColumn?.sortValue
      ? [...filteredItems].sort((left, right) => {
          const leftValue = sortColumn.sortValue?.(left);
          const rightValue = sortColumn.sortValue?.(right);
          let comparison = 0;
          if (typeof leftValue === "number" && typeof rightValue === "number") {
            comparison = leftValue - rightValue;
          } else if (typeof leftValue === "boolean" && typeof rightValue === "boolean") {
            comparison = Number(leftValue) - Number(rightValue);
          } else {
            comparison = String(leftValue ?? "").localeCompare(
              String(rightValue ?? ""),
              undefined,
              { numeric: true, sensitivity: "base" },
            );
          }
          return sort.direction === "ascending" ? comparison : -comparison;
        })
      : filteredItems;

  const pageCount = pageSize
    ? Math.max(1, Math.ceil(sortedItems.length / pageSize))
    : 1;
  const currentPage = Math.min(page, pageCount);
  const startIndex = pageSize ? (currentPage - 1) * pageSize : 0;
  const visibleItems = pageSize
    ? sortedItems.slice(startIndex, startIndex + pageSize)
    : sortedItems;

  const hasPagination = Boolean(pageSize) && pageCount > 1;

  const pageButtonClass =
    "inline-flex size-7 items-center justify-center rounded-[0.6rem] border border-border bg-surface-solid text-muted-foreground transition hover:text-foreground disabled:cursor-not-allowed disabled:opacity-40";

  return (
    <div className="rounded-panel border border-border bg-surface-softer">
      <div className="flex flex-col gap-2.5 border-b border-border px-3 py-3">
        <div className="flex flex-wrap items-center gap-2.5">
          <div className="flex min-w-0 flex-[0_1_auto] items-center gap-3">
            {TitleIcon ? (
              <span className="inline-flex size-8 shrink-0 items-center justify-center rounded-field border border-border bg-surface-solid text-accent-primary">
                <TitleIcon size={15} />
              </span>
            ) : null}
            <div className="min-w-0">
              <div className="flex min-w-0 flex-wrap items-center gap-2">
                <div className="truncate text-base font-semibold text-foreground">
                  {title}
                </div>
                {description ? (
                  <span
                    title={description}
                    className="inline-flex size-5 items-center justify-center rounded-[0.6rem] border border-border bg-surface-solid text-muted-foreground"
                  >
                    <InfoIcon size={12} />
                  </span>
                ) : null}
              </div>
              {summary ? <div className="mt-1.5 flex flex-wrap gap-1.5">{summary}</div> : null}
            </div>
          </div>
          {searchable ? (
            <div className="relative min-w-[12rem] max-w-md flex-1">
            <SearchIcon
              size={14}
              className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-muted-foreground"
            />
            <Input
              type="search"
              size="md"
              value={searchText}
              onChange={(event) => {
                setSearchText(event.target.value);
                setPage(1);
              }}
              placeholder={t("editor.table.searchPlaceholder")}
              aria-label={t("editor.table.searchPlaceholder")}
              className="pl-8"
            />
          </div>
          ) : null}
          {filters.map((filter) => (
            <Select
              key={filter.key}
              ariaLabel={filter.label}
              className="w-auto min-w-[8rem] max-w-[11rem]"
              onChange={(value) => {
                setFilterValues((current) => ({
                  ...current,
                  [filter.key]: value,
                }));
                setPage(1);
              }}
              options={[
                { value: "", label: filter.label },
                ...filter.options.map((option) => ({
                  value: option.value,
                  label: option.label,
                })),
              ]}
              size="sm"
              surface="soft"
              value={filterValues[filter.key] ?? ""}
            />
          ))}
          {actions ? (
            <div className="ml-auto flex shrink-0 flex-wrap items-center gap-2">
              {actions}
            </div>
          ) : null}
        </div>
      </div>

      {items.length === 0 ? (
        <SettingsEmptyState>
          {emptyState}
        </SettingsEmptyState>
      ) : filteredItems.length === 0 ? (
        <SettingsEmptyState>{emptySearchState ?? t("editor.table.noSearchResults")}</SettingsEmptyState>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-max min-w-full border-separate border-spacing-0">
            <thead>
              <tr className="border-b border-border bg-surface-solid text-left">
                {columns.map((column) => (
                  <th
                    key={column.header}
                    className={`px-3 py-2.5 app-text-11 uppercase tracking-[0.12em] text-muted-foreground ${
                      column.align === "right" ? "text-right" : "text-left"
                    } ${column.sticky === "right" ? "sticky right-0 z-20 bg-surface-solid shadow-[-10px_0_12px_-12px_rgba(0,0,0,0.65)]" : ""} ${column.className ?? ""}`}
                    aria-sort={
                      column.sortValue
                        ? (sort?.key === (column.sortKey ?? column.header)
                            ? sort.direction
                            : "none")
                        : undefined
                    }
                  >
                    {column.sortValue ? (
                      <button
                        type="button"
                        className="inline-flex items-center gap-1.5 whitespace-nowrap text-left transition hover:text-foreground"
                        aria-label={t("editor.table.sortBy", {
                          column: column.header,
                        })}
                        onClick={() => {
                          const key = column.sortKey ?? column.header;
                          setSort((current) =>
                            current?.key === key
                              ? {
                                  key,
                                  direction:
                                    current.direction === "ascending"
                                      ? "descending"
                                      : "ascending",
                                }
                              : { key, direction: "ascending" },
                          );
                          setPage(1);
                        }}
                      >
                        {column.header}
                        {sort?.key === (column.sortKey ?? column.header) ? (
                          sort.direction === "ascending" ? (
                            <ArrowUpIcon size={12} aria-hidden="true" />
                          ) : (
                            <ArrowDownIcon size={12} aria-hidden="true" />
                          )
                        ) : (
                          <ArrowUpDownIcon size={12} aria-hidden="true" />
                        )}
                      </button>
                    ) : (
                      column.header
                    )}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {visibleItems.map((item) => (
                <tr
                  key={getRowKey(item)}
                  className="border-b border-border/70 align-top last:border-b-0"
                >
                  {columns.map((column) => (
                    <td
                      key={column.header}
                      className={`px-3 py-2.5 text-sm text-foreground ${
                        column.align === "right" ? "text-right" : "text-left"
                      } ${column.sticky === "right" ? "sticky right-0 z-10 bg-surface-softer shadow-[-10px_0_12px_-12px_rgba(0,0,0,0.65)]" : ""} ${column.className ?? ""}`}
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
        <div className="flex flex-wrap items-center justify-between gap-2 border-t border-border px-3 py-2.5">
          <span className="app-text-11 text-muted-foreground">
            {t("editor.table.pagination.showing", {
              start: String(startIndex + 1),
              end: String(
                Math.min(startIndex + (pageSize ?? 0), sortedItems.length),
              ),
              total: String(sortedItems.length),
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
            <span className="app-text-11 min-w-[4.5rem] text-center text-muted-foreground">
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