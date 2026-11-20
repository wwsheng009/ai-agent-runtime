import {
  AlertTriangleIcon,
  ArrowUpRightIcon,
  LoaderCircleIcon,
  PencilIcon,
  PlusIcon,
  RefreshCwIcon,
  SparklesIcon,
  Trash2Icon,
} from "lucide-react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";

import type { AutomationTemplate } from "./templates";

type AutomationTemplatesPanelProps = {
  templates: AutomationTemplate[];
  loading: boolean;
  deletingId: string;
  error: string | null;
  onCreate: () => void;
  onDelete: (template: AutomationTemplate) => void;
  onEdit: (template: AutomationTemplate) => void;
  onRetry: () => void;
  onUse: (template: AutomationTemplate) => void;
};

export function AutomationTemplatesPanel({
  templates,
  loading,
  deletingId,
  error,
  onCreate,
  onDelete,
  onEdit,
  onRetry,
  onUse,
}: AutomationTemplatesPanelProps) {
  const { t } = useTranslation("workspace");

  return (
    <section
      aria-labelledby="automation-templates-title"
      className="surface-panel rounded-panel-lg p-3 sm:p-4"
    >
      <div className="flex flex-col gap-2 border-b border-border pb-3 sm:flex-row sm:items-end sm:justify-between">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <SparklesIcon
              aria-hidden="true"
              className="size-4 text-accent-primary"
            />
            <h2
              id="automation-templates-title"
              className="text-sm font-semibold"
            >
              {t("panels.schedule.templates.title")}
            </h2>
            <Badge>{templates.length}</Badge>
          </div>
          <p className="mt-1 max-w-2xl text-xs leading-5 text-muted-foreground">
            {t("panels.schedule.templates.description")}
          </p>
        </div>
        <div className="flex shrink-0 flex-wrap items-center gap-2">
          <p className="hidden max-w-xl text-xs leading-5 text-muted-foreground xl:block">
            {t("panels.schedule.templates.note")}
          </p>
          <Button onClick={onCreate} size="sm">
            <PlusIcon aria-hidden="true" className="size-3.5" />
            {t("panels.schedule.templates.create")}
          </Button>
        </div>
      </div>

      {error ? (
        <div
          className="mt-3 flex flex-wrap items-center justify-between gap-3 rounded-field border border-destructive/30 bg-destructive/5 px-3 py-2.5 text-sm text-destructive"
          role="alert"
        >
          <span className="flex items-center gap-2">
            <AlertTriangleIcon aria-hidden="true" className="size-4 shrink-0" />
            {error}
          </span>
          <Button onClick={onRetry} size="sm" variant="secondary">
            <RefreshCwIcon aria-hidden="true" className="size-3.5" />
            {t("panels.schedule.templates.retry")}
          </Button>
        </div>
      ) : null}

      {loading && templates.length === 0 ? (
        <div
          className="mt-4 flex items-center justify-center gap-2 rounded-panel border border-border/70 bg-surface/30 px-4 py-10 text-sm text-muted-foreground"
          role="status"
        >
          <LoaderCircleIcon
            aria-hidden="true"
            className="size-4 animate-spin"
          />
          {t("panels.schedule.templates.loading")}
        </div>
      ) : templates.length === 0 ? (
        <div className="mt-4 rounded-panel border border-dashed border-border px-4 py-10 text-center">
          <SparklesIcon
            aria-hidden="true"
            className="mx-auto size-6 text-muted-foreground"
          />
          <p className="mt-2 text-sm font-medium">
            {t("panels.schedule.templates.empty")}
          </p>
          <p className="mt-1 text-xs text-muted-foreground">
            {t("panels.schedule.templates.emptyHint")}
          </p>
          <Button className="mt-4" onClick={onCreate} size="sm">
            <PlusIcon aria-hidden="true" className="size-3.5" />
            {t("panels.schedule.templates.create")}
          </Button>
        </div>
      ) : (
        <div className="mt-3 grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
          {templates.map((template) => {
            const Icon = template.icon;
            const deleting = deletingId === template.id;
            return (
              <article
                className="group flex min-w-0 flex-col rounded-panel border border-border bg-surface-softer p-3 transition-colors hover:border-accent-primary-border hover:bg-surface-solid focus-within:ring-2 focus-within:ring-ring"
                key={template.id}
              >
                <div className="flex items-start justify-between gap-3">
                  <span className="grid size-10 shrink-0 place-items-center rounded-panel border border-accent-primary-border bg-accent-primary-soft text-accent-primary">
                    <Icon aria-hidden="true" className="size-5" />
                  </span>
                  <Badge>{template.category}</Badge>
                </div>
                <h3 className="mt-3 text-sm font-semibold leading-5 text-foreground">
                  {template.title}
                </h3>
                <p className="mt-1 min-h-10 text-xs leading-5 text-muted-foreground">
                  {template.description}
                </p>
                <div className="mt-3 flex items-center gap-2 border-t border-border/70 pt-2.5 text-xs text-muted-foreground">
                  <span
                    className="size-1.5 rounded-full bg-accent-secondary"
                    aria-hidden="true"
                  />
                  <span>{template.frequency}</span>
                </div>
                <Button
                  className="mt-3 w-full justify-between"
                  onClick={() => onUse(template)}
                  size="sm"
                  variant="secondary"
                >
                  {t("panels.schedule.templates.use")}
                  <ArrowUpRightIcon aria-hidden="true" className="size-3.5" />
                </Button>
                <div className="mt-2 grid grid-cols-2 gap-2">
                  <Button
                    aria-label={`${t("panels.schedule.templates.edit")}: ${template.title}`}
                    disabled={deleting}
                    onClick={() => onEdit(template)}
                    size="sm"
                    variant="ghost"
                  >
                    <PencilIcon aria-hidden="true" className="size-3.5" />
                    {t("panels.schedule.templates.edit")}
                  </Button>
                  <Button
                    aria-label={`${t("panels.schedule.templates.delete")}: ${template.title}`}
                    disabled={deleting}
                    onClick={() => onDelete(template)}
                    size="sm"
                    variant="ghost"
                  >
                    {deleting ? (
                      <LoaderCircleIcon
                        aria-hidden="true"
                        className="size-3.5 animate-spin"
                      />
                    ) : (
                      <Trash2Icon aria-hidden="true" className="size-3.5" />
                    )}
                    {deleting
                      ? t("panels.schedule.templates.deleting")
                      : t("panels.schedule.templates.delete")}
                  </Button>
                </div>
              </article>
            );
          })}
        </div>
      )}
    </section>
  );
}
