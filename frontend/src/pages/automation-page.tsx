import {
  ArrowLeftIcon,
  CalendarClockIcon,
  HistoryIcon,
  ListChecksIcon,
  PlusIcon,
  RefreshCwIcon,
  SettingsIcon,
  SparklesIcon,
} from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link, useNavigate } from "react-router-dom";

import { Badge } from "@/components/ui/badge";
import { Button, buttonVariants } from "@/components/ui/button";
import { useScheduleTasks } from "@/hooks/workspace/use-schedule-tasks";
import { cn } from "@/lib/utils";
import {
  createScheduleTemplate,
  deleteScheduleTemplate,
  fetchScheduleTemplates,
  updateScheduleTemplate,
} from "@/api/runtime/schedule";
import type {
  RuntimeScheduleTask,
  RuntimeScheduleTemplate,
} from "@/types/runtime";

import { AutomationListPanel } from "./automation/list-panel";
import {
  AUTOMATION_RUNS_ALL_TASKS,
  AutomationRunsPanel,
} from "./automation/runs-panel";
import { AutomationSummary } from "./automation/summary";
import {
  buildAutomationTemplates,
  emptyAutomationTemplateForm,
  templateFormFrom,
  templateRequestFromForm,
  validateAutomationTemplateForm,
  type AutomationTemplateForm,
  type AutomationTemplateFormErrors,
} from "./automation/templates";
import { TemplateEditorDialog } from "./automation/template-editor-dialog";
import { AutomationTemplatesPanel } from "./automation/templates-panel";
import type {
  AutomationCreateRequest,
  AutomationView,
  AutomationStatusFilter,
  AutomationTargetFilter,
} from "./automation/types";

const views: Array<{
  id: AutomationView;
  icon: typeof ListChecksIcon;
  labelKey: "list" | "runs" | "templates";
}> = [
  { id: "list", icon: ListChecksIcon, labelKey: "list" },
  { id: "runs", icon: HistoryIcon, labelKey: "runs" },
  { id: "templates", icon: SparklesIcon, labelKey: "templates" },
];

export function AutomationPage() {
  const { t } = useTranslation("workspace");
  const navigate = useNavigate();
  const controller = useScheduleTasks({ enabled: true, sessionId: "" });
  const [view, setView] = useState<AutomationView>("list");
  const [runsTaskId, setRunsTaskId] = useState(AUTOMATION_RUNS_ALL_TASKS);
  const [query, setQuery] = useState("");
  const [statusFilter, setStatusFilter] =
    useState<AutomationStatusFilter>("all");
  const [targetFilter, setTargetFilter] =
    useState<AutomationTargetFilter>("all");
  const [createRequest, setCreateRequest] =
    useState<AutomationCreateRequest | null>(null);
  const [templateRecords, setTemplateRecords] = useState<
    RuntimeScheduleTemplate[]
  >([]);
  const [templatesLoading, setTemplatesLoading] = useState(false);
  const [templatesError, setTemplatesError] = useState<string | null>(null);
  const [templateEditor, setTemplateEditor] = useState<{
    mode: "create" | "edit";
    form: AutomationTemplateForm;
  } | null>(null);
  const [templateSaving, setTemplateSaving] = useState(false);
  const [templateDeletingId, setTemplateDeletingId] = useState("");
  const [templateEditorError, setTemplateEditorError] = useState<string | null>(
    null,
  );
  const [templateFieldErrors, setTemplateFieldErrors] =
    useState<AutomationTemplateFormErrors>({});
  const requestSequence = useRef(0);
  const templateLoadSequence = useRef(0);
  const tabRefs = useRef<Array<HTMLButtonElement | null>>([]);
  const pendingHistoryFocus = useRef(false);
  const templates = useMemo(
    () => buildAutomationTemplates(templateRecords, t),
    [templateRecords, t],
  );

  const loadTemplates = useCallback(async () => {
    const sequence = ++templateLoadSequence.current;
    setTemplatesLoading(true);
    setTemplatesError(null);
    try {
      const response = await fetchScheduleTemplates();
      if (sequence === templateLoadSequence.current) {
        setTemplateRecords(response.templates);
      }
    } catch {
      if (sequence === templateLoadSequence.current) {
        setTemplatesError(t("panels.schedule.templates.loadError"));
      }
    } finally {
      if (sequence === templateLoadSequence.current) {
        setTemplatesLoading(false);
      }
    }
  }, [t]);

  useEffect(() => {
    void loadTemplates();
    return () => {
      templateLoadSequence.current += 1;
    };
  }, [loadTemplates]);

  const startCreateTemplate = useCallback(() => {
    setTemplateEditor({ mode: "create", form: emptyAutomationTemplateForm() });
    setTemplateFieldErrors({});
    setTemplateEditorError(null);
  }, []);

  const startEditTemplate = useCallback(
    (template: (typeof templates)[number]) => {
      setTemplateEditor({
        mode: "edit",
        form: templateFormFrom(template.source),
      });
      setTemplateFieldErrors({});
      setTemplateEditorError(null);
    },
    [],
  );

  const closeTemplateEditor = useCallback(() => {
    if (templateSaving) return;
    setTemplateEditor(null);
    setTemplateFieldErrors({});
    setTemplateEditorError(null);
  }, [templateSaving]);

  const changeTemplateForm = useCallback(
    (patch: Partial<AutomationTemplateForm>) => {
      setTemplateEditor((current) =>
        current ? { ...current, form: { ...current.form, ...patch } } : current,
      );
      setTemplateFieldErrors({});
    },
    [],
  );

  const submitTemplate = useCallback(async () => {
    if (!templateEditor || templateSaving) return;
    const errors = validateAutomationTemplateForm(templateEditor.form);
    setTemplateFieldErrors(errors);
    setTemplateEditorError(null);
    if (Object.keys(errors).length > 0) return;

    setTemplateSaving(true);
    try {
      const request = templateRequestFromForm(templateEditor.form);
      const response =
        templateEditor.mode === "create"
          ? await createScheduleTemplate(request)
          : await updateScheduleTemplate(templateEditor.form.id, request);
      setTemplateRecords((current) => [
        response.template,
        ...current.filter((template) => template.id !== response.template.id),
      ]);
      setTemplateEditor(null);
      setTemplateFieldErrors({});
    } catch (caught) {
      const message = caught instanceof Error ? caught.message : String(caught);
      setTemplateEditorError(
        t("panels.schedule.templates.saveFailed", { message }),
      );
    } finally {
      setTemplateSaving(false);
    }
  }, [templateEditor, templateSaving, t]);

  const removeTemplate = useCallback(
    async (template: (typeof templates)[number]) => {
      if (
        templateDeletingId ||
        !window.confirm(
          t("panels.schedule.templates.confirmDelete", {
            title: template.title,
          }),
        )
      ) {
        return;
      }
      setTemplateDeletingId(template.id);
      setTemplatesError(null);
      try {
        const response = await deleteScheduleTemplate(template.id);
        if (!response.deleted) {
          throw new Error("Template was not deleted");
        }
        setTemplateRecords((current) =>
          current.filter((item) => item.id !== template.id),
        );
      } catch (caught) {
        const message =
          caught instanceof Error ? caught.message : String(caught);
        setTemplatesError(
          t("panels.schedule.templates.deleteFailed", { message }),
        );
      } finally {
        setTemplateDeletingId("");
      }
    },
    [templateDeletingId, t],
  );

  const taskFilter = useCallback(
    (task: RuntimeScheduleTask) => {
      const targetKind = task.targetKind ?? "session";
      if (statusFilter !== "all" && task.status !== statusFilter) {
        return false;
      }
      if (targetFilter !== "all" && targetKind !== targetFilter) {
        return false;
      }
      const normalizedQuery = query.trim().toLocaleLowerCase();
      if (!normalizedQuery) {
        return true;
      }
      const searchable = [
        task.id,
        task.title,
        task.prompt,
        task.goal,
        task.targetWorkspace,
        task.targetProfile,
        ...(task.plan ?? []),
      ]
        .filter(Boolean)
        .join(" ")
        .toLocaleLowerCase();
      return searchable.includes(normalizedQuery);
    },
    [query, statusFilter, targetFilter],
  );

  const filteredCount = useMemo(
    () => controller.tasks.filter(taskFilter).length,
    [controller.tasks, taskFilter],
  );

  const startCreate = useCallback(
    (preset: AutomationCreateRequest["preset"] = {}) => {
      requestSequence.current += 1;
      setView("list");
      setCreateRequest({
        key: `automation-create-${requestSequence.current}`,
        preset,
      });
    },
    [],
  );

  const openView = useCallback((nextView: AutomationView) => {
    if (nextView === "runs") {
      setRunsTaskId(AUTOMATION_RUNS_ALL_TASKS);
    }
    setView(nextView);
  }, []);

  const handleViewHistory = useCallback((task: RuntimeScheduleTask) => {
    pendingHistoryFocus.current = true;
    setRunsTaskId(task.id);
    setView("runs");
  }, []);

  const handleOpenSession = useCallback(
    (sessionId: string) => {
      const normalized = sessionId.trim();
      if (!normalized) {
        return;
      }
      navigate(`/workspace/sessions/${encodeURIComponent(normalized)}`);
    },
    [navigate],
  );

  useEffect(() => {
    if (view !== "runs" || !pendingHistoryFocus.current) {
      return;
    }
    pendingHistoryFocus.current = false;
    const runsTabIndex = views.findIndex((item) => item.id === "runs");
    tabRefs.current[runsTabIndex]?.focus();
  }, [view]);

  const handleCreateRequestHandled = useCallback((key: string) => {
    setCreateRequest((current) => (current?.key === key ? null : current));
  }, []);

  return (
    <div className="min-h-screen [background:var(--workspace-shell-bg)] text-foreground lg:h-dvh lg:overflow-hidden">
      <div className="mx-auto flex min-h-screen w-full max-w-[1760px] flex-col gap-2 px-2.5 py-2.5 sm:px-3 lg:h-full lg:min-h-0">
        <header className="surface-panel relative overflow-hidden rounded-panel-lg px-3.5 py-3">
          <div className="pointer-events-none absolute inset-0 bg-[radial-gradient(circle_at_top_left,rgba(240,199,123,0.18),transparent_28%),radial-gradient(circle_at_right,rgba(103,215,230,0.12),transparent_22%)]" />
          <div className="relative flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
            <div className="min-w-0 space-y-1.5">
              <div className="flex flex-wrap items-center gap-2">
                <Badge className="border-accent-primary-border bg-accent-primary-soft text-accent-primary">
                  <CalendarClockIcon aria-hidden="true" className="size-3.5" />
                  {t("panels.schedule.title")}
                </Badge>
                <Badge>{t("panels.schedule.independentPage")}</Badge>
                {controller.loading ? (
                  <span className="text-xs text-muted-foreground" role="status">
                    {t("panels.schedule.syncing")}
                  </span>
                ) : null}
              </div>
              <div>
                <h1 className="text-base font-semibold tracking-[-0.03em] sm:text-[1.1rem]">
                  {t("panels.schedule.title")}
                </h1>
                <p className="mt-1 max-w-3xl text-sm leading-6 text-muted-foreground">
                  {t("panels.schedule.description")}
                </p>
              </div>
            </div>

            <nav
              aria-label={t("panels.schedule.title")}
              className="flex flex-wrap items-center gap-2"
            >
              <Button onClick={() => startCreate()} size="sm">
                <PlusIcon aria-hidden="true" className="size-4" />
                {t("panels.schedule.create")}
              </Button>
              <Button
                aria-label={t("panels.schedule.refresh")}
                disabled={controller.loading}
                onClick={controller.refresh}
                size="icon"
                variant="secondary"
              >
                <RefreshCwIcon
                  aria-hidden="true"
                  className={cn("size-4", controller.loading && "animate-spin")}
                />
              </Button>
              <Link
                to="/runtime/config"
                className={cn(
                  buttonVariants({ variant: "secondary", size: "sm" }),
                )}
              >
                <SettingsIcon aria-hidden="true" className="size-3.5" />
                {t("panels.schedule.backToRuntimeConfig")}
              </Link>
              <Link
                to="/workspace/chats/new"
                className={cn(buttonVariants({ variant: "ghost", size: "sm" }))}
              >
                <ArrowLeftIcon aria-hidden="true" className="size-3.5" />
                {t("panels.schedule.backToWorkspace")}
              </Link>
            </nav>
          </div>
        </header>

        <main className="min-h-0 flex-1 space-y-2 overflow-y-auto">
          <AutomationSummary tasks={controller.tasks} />

          <div className="surface-panel rounded-panel-lg p-2 sm:p-2.5">
            <div
              aria-label={t("panels.schedule.tabListLabel")}
              className="flex flex-wrap gap-1"
              role="tablist"
            >
              {views.map(({ id, icon: Icon, labelKey }, index) => {
                const selected = view === id;
                return (
                  <button
                    aria-controls={`automation-tabpanel-${id}`}
                    aria-selected={selected}
                    className={cn(
                      buttonVariants({
                        size: "sm",
                        variant: selected ? "secondary" : "ghost",
                      }),
                      selected &&
                        "border-accent-primary-border bg-accent-primary-soft",
                    )}
                    id={`automation-tab-${id}`}
                    key={id}
                    onClick={() => openView(id)}
                    onKeyDown={(event) => {
                      let nextIndex: number | null = null;
                      if (event.key === "ArrowRight") {
                        nextIndex = (index + 1) % views.length;
                      } else if (event.key === "ArrowLeft") {
                        nextIndex = (index - 1 + views.length) % views.length;
                      } else if (event.key === "Home") {
                        nextIndex = 0;
                      } else if (event.key === "End") {
                        nextIndex = views.length - 1;
                      }
                      if (nextIndex === null) return;
                      event.preventDefault();
                      const nextView = views[nextIndex];
                      if (!nextView) return;
                      openView(nextView.id);
                      tabRefs.current[nextIndex]?.focus();
                    }}
                    role="tab"
                    ref={(element) => {
                      tabRefs.current[index] = element;
                    }}
                    tabIndex={selected ? 0 : -1}
                    type="button"
                  >
                    <Icon aria-hidden="true" className="size-3.5" />
                    {t(`panels.schedule.tabs.${labelKey}`)}
                  </button>
                );
              })}
            </div>
          </div>

          <div
            aria-labelledby={`automation-tab-${view}`}
            id={`automation-tabpanel-${view}`}
            role="tabpanel"
            tabIndex={0}
          >
            {view === "list" ? (
              <AutomationListPanel
                controller={controller}
                createRequest={createRequest}
                emptyMessage={
                  controller.tasks.length > 0 && filteredCount === 0
                    ? t("panels.schedule.filters.noMatches")
                    : undefined
                }
                filteredCount={filteredCount}
                onCreateRequestHandled={handleCreateRequestHandled}
                onOpenSession={handleOpenSession}
                onViewHistory={handleViewHistory}
                onQueryChange={setQuery}
                onStatusFilterChange={setStatusFilter}
                onTargetFilterChange={setTargetFilter}
                query={query}
                statusFilter={statusFilter}
                targetFilter={targetFilter}
                taskFilter={taskFilter}
              />
            ) : null}
            {view === "runs" ? (
              <AutomationRunsPanel
                onSelectedTaskIdChange={setRunsTaskId}
                selectedTaskId={runsTaskId}
                tasks={controller.tasks}
              />
            ) : null}
            {view === "templates" ? (
              <AutomationTemplatesPanel
                deletingId={templateDeletingId}
                error={templatesError}
                loading={templatesLoading}
                onCreate={startCreateTemplate}
                onDelete={removeTemplate}
                onEdit={startEditTemplate}
                onRetry={() => void loadTemplates()}
                onUse={(template) => startCreate(template.form)}
                templates={templates}
              />
            ) : null}
          </div>
        </main>
      </div>
      {templateEditor ? (
        <TemplateEditorDialog
          busy={templateSaving}
          error={templateEditorError}
          fieldErrors={templateFieldErrors}
          form={templateEditor.form}
          mode={templateEditor.mode}
          onChange={changeTemplateForm}
          onClearError={() => setTemplateEditorError(null)}
          onClose={closeTemplateEditor}
          onSubmit={() => void submitTemplate()}
        />
      ) : null}
    </div>
  );
}
