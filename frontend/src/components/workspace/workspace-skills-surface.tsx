// P2-1B：工作台「技能」页签内容——运行时技能目录列表 + 详情对话框。
//
// 与独立页面（/runtime/skills）同一呈现纪律：
//   * 目录加载失败 / 端点不可用按真实错误分类呈现，不伪装成「空目录」；
//   * 列表只渲染后端字段（分类 / 版本 / 来源缺失即不显示），不补默认值；
//   * 点击行打开详情对话框，详情由 `SkillDetailPanel` 现取，不用列表快照冒充最新定义。

import { useState } from "react";
import { RefreshCwIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { useSkillsCatalog } from "@/hooks/use-skills-catalog";
import { cn } from "@/lib/utils";
import { classifySkillsError, describeSkillSource, skillSubtitle } from "@/pages/skills/shared";
import type { RuntimeSkill } from "@/types/runtime";

import { WorkspaceSkillDetailDialog } from "./workspace-skill-detail-dialog";

type WorkspaceSkillsSurfaceProps = {
  className?: string;
};

function SkillRow({
  skill,
  onSelect,
}: {
  skill: RuntimeSkill;
  onSelect: () => void;
}) {
  const source = describeSkillSource(skill);
  const subtitle = skillSubtitle(skill);

  return (
    <li>
      <button
        className="w-full rounded-panel border border-border bg-surface-softer px-3 py-2 text-left transition-colors hover:bg-surface-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        data-testid="workspace-skills-row"
        type="button"
        onClick={onSelect}
      >
        <div className="flex flex-wrap items-baseline justify-between gap-x-2 gap-y-0.5">
          <span className="truncate text-sm font-medium">{skill.name}</span>
          {subtitle ? <span className="text-xs text-muted-foreground">{subtitle}</span> : null}
        </div>
        {skill.description ? (
          <p className="mt-0.5 line-clamp-2 text-xs leading-5 text-muted-foreground">
            {skill.description}
          </p>
        ) : null}
        {source ? (
          <p
            className="mt-0.5 truncate text-xs text-muted-foreground"
            data-testid="workspace-skills-row-source"
          >
            {source}
          </p>
        ) : null}
        {skill.tags.length > 0 ? (
          <div className="mt-1 flex flex-wrap gap-1">
            {skill.tags.map((tag) => (
              <span
                key={tag}
                className="rounded-full border border-border px-1.5 py-0.5 text-[0.65rem] text-muted-foreground"
              >
                {tag}
              </span>
            ))}
          </div>
        ) : null}
      </button>
    </li>
  );
}

export function WorkspaceSkillsSurface({ className }: WorkspaceSkillsSurfaceProps) {
  const { t } = useTranslation("skills");
  const { count, error, refresh, skills, status } = useSkillsCatalog();
  const [selectedSkill, setSelectedSkill] = useState<RuntimeSkill | null>(null);

  return (
    <section
      aria-label={t("catalog.title")}
      className={cn("flex min-h-0 flex-1 flex-col overflow-hidden", className)}
      data-testid="workspace-skills-surface"
    >
      <div className="flex flex-wrap items-center justify-between gap-2 border-b border-border px-3 py-2 sm:px-4">
        <div className="min-w-0">
          <h2 className="text-sm font-semibold">{t("catalog.title")}</h2>
          <p className="mt-0.5 text-xs text-muted-foreground">
            {t("workspaceTab.description")}
          </p>
        </div>
        <div className="flex items-center gap-2 text-xs text-muted-foreground">
          <span data-testid="workspace-skills-count">
            {t("catalog.count", { count })}
          </span>
          <Button
            data-testid="workspace-skills-refresh"
            size="sm"
            variant="ghost"
            onClick={refresh}
          >
            <RefreshCwIcon size={14} />
            {t("actions.refresh")}
          </Button>
        </div>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto px-3 py-3 sm:px-4">
        {status === "loading" ? (
          <p className="text-xs text-muted-foreground" data-testid="workspace-skills-loading">
            {t("catalog.loading")}
          </p>
        ) : null}

        {status === "error" ? (
          <div
            className="flex flex-col items-start gap-2 rounded-panel border border-analytics-warning-border bg-analytics-warning-soft px-3 py-2 text-xs text-analytics-warning"
            data-testid="workspace-skills-error"
          >
            <span>{t(`errors.${classifySkillsError(error)}`)}</span>
            <Button size="sm" variant="secondary" onClick={refresh}>
              {t("actions.retry")}
            </Button>
          </div>
        ) : null}

        {status === "ready" && skills.length === 0 ? (
          <p className="text-xs text-muted-foreground" data-testid="workspace-skills-empty">
            {t("catalog.empty")}
          </p>
        ) : null}

        {skills.length > 0 ? (
          <>
            <p className="mb-2 text-xs text-muted-foreground">{t("catalog.selectHint")}</p>
            <ul
              className="mx-auto max-w-[50rem] space-y-2"
              data-testid="workspace-skills-list"
            >
              {skills.map((skill) => (
                <SkillRow
                  key={skill.name}
                  skill={skill}
                  onSelect={() => setSelectedSkill(skill)}
                />
              ))}
            </ul>
          </>
        ) : null}
      </div>

      {selectedSkill ? (
        <WorkspaceSkillDetailDialog
          skill={selectedSkill}
          onClose={() => setSelectedSkill(null)}
        />
      ) : null}
    </section>
  );
}
