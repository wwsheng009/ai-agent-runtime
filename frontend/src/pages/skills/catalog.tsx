// P2-1A：技能市场目录面板（关键词检索 + 目录列表 + 详情联动）。
//
// 呈现纪律：
//   * 检索返回 `resolved_mode` / `used_embedding` 原样展示——后端语义检索降级时
//     用户能看到真实模式，而不是被假装成语义命中；
//   * 目录 / 检索各自独立空态与失败态，检索失败不清空目录；
//   * 触顶提示用后端返回的 `limit` 与 `count` 计算，不猜。

import { useState } from "react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import type { RuntimeSkill, RuntimeSkillSearchResult } from "@/types/runtime";

import type { SkillsSearchStatus, SkillsStreamStatus } from "@/hooks/use-skills-market";
import type { RuntimeSkillSearchModeInput } from "@/api/runtime/skills";

import { SkillDetailPanel } from "./detail";
import { classifySkillsError, describeSkillSource, matchSummary, skillSearchModes, skillSubtitle } from "./shared";

type SkillCatalogPanelProps = {
  catalog: RuntimeSkill[];
  catalogCount: number;
  catalogStatus: SkillsStreamStatus;
  catalogError: unknown;
  searchQuery: string;
  searchCategory: string;
  searchMode: RuntimeSkillSearchModeInput;
  searchStatus: SkillsSearchStatus;
  searchResult: RuntimeSkillSearchResult | null;
  searchError: unknown;
  onSearch: (input: { query: string; category?: string; mode?: RuntimeSkillSearchModeInput }) => void;
  onClearSearch: () => void;
  onRefresh: () => void;
};

function SkillRow({
  skill,
  detail,
  active,
  onSelect,
}: {
  skill: RuntimeSkill;
  detail?: string;
  active: boolean;
  onSelect: () => void;
}) {
  const source = describeSkillSource(skill);
  const subtitle = skillSubtitle(skill);
  return (
    <li>
      <button
        type="button"
        onClick={onSelect}
        data-testid="skills-row"
        className={cn(
          "w-full rounded-panel border border-border bg-surface-softer px-3 py-2 text-left transition-colors hover:bg-surface-muted",
          active && "border-accent-primary-border bg-accent-primary-soft",
        )}
      >
        <div className="flex flex-wrap items-baseline justify-between gap-x-2 gap-y-0.5">
          <span className="truncate text-sm font-medium">{skill.name}</span>
          {subtitle ? <span className="text-xs text-muted-foreground">{subtitle}</span> : null}
        </div>
        {detail || skill.description ? (
          <p className="mt-0.5 line-clamp-2 text-xs leading-5 text-muted-foreground">
            {detail || skill.description}
          </p>
        ) : null}
        {source ? (
          <p className="mt-0.5 truncate text-xs text-muted-foreground" data-testid="skills-row-source">
            {source}
          </p>
        ) : null}
        {skill.tags.length > 0 ? (
          <div className="mt-1 flex flex-wrap gap-1">
            {skill.tags.map((tag) => (
              <span key={tag} className="rounded-full border border-border px-1.5 py-0.5 text-[0.65rem] text-muted-foreground">
                {tag}
              </span>
            ))}
          </div>
        ) : null}
      </button>
    </li>
  );
}

export function SkillCatalogPanel(props: SkillCatalogPanelProps) {
  const {
    catalog,
    catalogCount,
    catalogStatus,
    catalogError,
    searchQuery,
    searchCategory,
    searchMode,
    searchStatus,
    searchResult,
    searchError,
    onSearch,
    onClearSearch,
    onRefresh,
  } = props;
  const { t } = useTranslation("skills");

  const [queryDraft, setQueryDraft] = useState(searchQuery);
  const [categoryDraft, setCategoryDraft] = useState(searchCategory);
  const [modeDraft, setModeDraft] = useState<RuntimeSkillSearchModeInput>(searchMode);
  const [selectedName, setSelectedName] = useState<string | null>(null);

  const searching = searchStatus !== "idle";
  const rows = searching && searchResult ? searchResult.results : catalog;
  const selectedSkill = rows.find((skill) => skill.name === selectedName) ?? null;

  const submit = () => {
    const query = queryDraft.trim();
    if (!query) {
      return;
    }
    onSearch({ query, category: categoryDraft.trim() || undefined, mode: modeDraft });
  };

  return (
    <section
      className="surface-panel min-w-0 rounded-panel border border-border px-3 py-3 sm:px-4"
      aria-label={t("catalog.title")}
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="text-sm font-semibold">{t("catalog.title")}</h2>
        <div className="flex items-center gap-2 text-xs text-muted-foreground">
          <span data-testid="skills-catalog-count">
            {t("catalog.count", { count: searching && searchResult ? searchResult.count : catalogCount })}
          </span>
          <Button variant="ghost" size="sm" onClick={onRefresh}>
            {t("actions.refresh")}
          </Button>
        </div>
      </div>

      <form
        className="mt-3 flex flex-col gap-2 sm:flex-row sm:items-center"
        onSubmit={(event) => {
          event.preventDefault();
          submit();
        }}
      >
        <input
          value={queryDraft}
          onChange={(event) => setQueryDraft(event.target.value)}
          placeholder={t("catalog.searchPlaceholder")}
          aria-label={t("catalog.searchPlaceholder")}
          data-testid="skills-search-input"
          className="min-w-0 flex-1 rounded-panel border border-border bg-surface-softer px-2.5 py-1.5 text-sm outline-none placeholder:text-muted-foreground focus:border-accent-primary-border"
        />
        <input
          value={categoryDraft}
          onChange={(event) => setCategoryDraft(event.target.value)}
          placeholder={t("catalog.categoryPlaceholder")}
          aria-label={t("catalog.categoryPlaceholder")}
          data-testid="skills-category-input"
          className="w-full rounded-panel border border-border bg-surface-softer px-2.5 py-1.5 text-sm outline-none placeholder:text-muted-foreground focus:border-accent-primary-border sm:w-36"
        />
        <div className="flex items-center gap-1" role="group" aria-label={t("catalog.modeLabel")}>
          {skillSearchModes.map((mode) => (
            <button
              key={mode}
              type="button"
              onClick={() => setModeDraft(mode)}
              aria-pressed={modeDraft === mode}
              data-testid={`skills-mode-${mode}`}
              className={cn(
                "rounded-panel border border-border px-2 py-1 text-xs text-muted-foreground",
                modeDraft === mode && "border-accent-primary-border bg-accent-primary-soft text-accent-primary",
              )}
            >
              {t(`catalog.mode.${mode}`)}
            </button>
          ))}
        </div>
        <div className="flex items-center gap-2">
          <Button type="submit" size="sm" disabled={!queryDraft.trim()}>
            {t("actions.search")}
          </Button>
          {searching ? (
            <Button type="button" variant="secondary" size="sm" onClick={onClearSearch}>
              {t("actions.clearSearch")}
            </Button>
          ) : null}
        </div>
      </form>

      {searching && searchStatus === "loading" ? (
        <p className="mt-3 text-xs text-muted-foreground" data-testid="skills-search-loading">
          {t("catalog.searching")}
        </p>
      ) : null}

      {searching && searchStatus === "error" ? (
        <div
          className="mt-3 rounded-panel border border-analytics-warning-border bg-analytics-warning-soft px-3 py-2 text-xs text-analytics-warning"
          data-testid="skills-search-error"
        >
          {t(`errors.${classifySkillsError(searchError)}`)}
        </div>
      ) : null}

      {searching && searchStatus === "ready" && searchResult ? (
        <div className="mt-3 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
          <span data-testid="skills-search-summary">
            {t("catalog.resultCount", { count: searchResult.count, query: searchResult.query })}
          </span>
          <span className="rounded-full border border-border px-1.5 py-0.5">
            {t("catalog.resolvedMode", {
              mode: searchResult.resolvedMode || searchResult.requestedMode || "unknown",
            })}
          </span>
          {searchResult.usedEmbedding ? (
            <span className="rounded-full border border-border px-1.5 py-0.5">
              {t("catalog.usedEmbedding")}
            </span>
          ) : (
            <span className="rounded-full border border-border px-1.5 py-0.5">
              {t("catalog.lexicalOnly")}
            </span>
          )}
          {searchResult.count >= searchResult.limit ? (
            <span className="text-analytics-warning" data-testid="skills-search-limit">
              {t("catalog.limitReached", { limit: String(searchResult.limit) })}
            </span>
          ) : null}
        </div>
      ) : null}

      {!searching && catalogStatus === "loading" ? (
        <p className="mt-3 text-xs text-muted-foreground" data-testid="skills-catalog-loading">
          {t("catalog.loading")}
        </p>
      ) : null}

      {!searching && catalogStatus === "error" ? (
        <div
          className="mt-3 rounded-panel border border-analytics-warning-border bg-analytics-warning-soft px-3 py-2 text-xs text-analytics-warning"
          data-testid="skills-catalog-error"
        >
          {t(`errors.${classifySkillsError(catalogError)}`)}
        </div>
      ) : null}

      {searching && searchStatus === "ready" && searchResult && searchResult.results.length === 0 ? (
        <p className="mt-3 text-xs text-muted-foreground" data-testid="skills-search-empty">
          {t("catalog.searchEmpty")}
        </p>
      ) : null}

      {!searching && catalogStatus === "ready" && catalog.length === 0 ? (
        <p className="mt-3 text-xs text-muted-foreground" data-testid="skills-catalog-empty">
          {t("catalog.empty")}
        </p>
      ) : null}

      {rows.length > 0 ? (
        <ul className="mt-3 max-h-[26rem] space-y-2 overflow-auto pr-1" data-testid="skills-list">
          {rows.map((skill) => (
            <SkillRow
              key={skill.name}
              skill={skill}
              detail={
                searching && searchResult
                  ? searchResult.matches.find((match) => match.skill.name === skill.name)?.details ||
                    matchSummary(skill)
                  : undefined
              }
              active={skill.name === selectedName}
              onSelect={() =>
                setSelectedName((current) => (current === skill.name ? null : skill.name))
              }
            />
          ))}
        </ul>
      ) : null}

      {selectedSkill ? (
        <div className="mt-3">
          <SkillDetailPanel
            key={selectedSkill.name}
            skill={selectedSkill}
            onClose={() => setSelectedName(null)}
          />
        </div>
      ) : null}
    </section>
  );
}
