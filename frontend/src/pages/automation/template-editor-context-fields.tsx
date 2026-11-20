import { InfoIcon, TargetIcon } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

import { listRuntimeModels } from "@/api/runtime/models";
import { listRuntimeProfiles } from "@/api/runtime/profiles";
import { listWorkspaceDirectories } from "@/api/runtime/workspace-directories";
import { Select } from "@/components/ui/select";
import type {
  RuntimeProfileListEntry,
  RuntimeScheduleTargetKind,
  RuntimeWorkspaceDirectory,
} from "@/types/runtime";
import { cn } from "@/lib/utils";

import {
  templateCompactTextareaClassName,
  templateErrorText,
  templateFieldA11y,
  templateFieldClassName,
  templateFieldErrorId,
  withCurrentSelectOption,
} from "./template-editor-controls";
import { TemplateField, TemplateSection } from "./template-editor-fields";
import { TemplateModelFields } from "./template-editor-model-fields";
import type {
  AutomationTemplateForm,
  AutomationTemplateFormErrors,
} from "./templates";

const TARGET_OPTIONS = [
  {
    kind: "session",
    labelKey: "panels.schedule.target.session",
    descriptionKey: "panels.schedule.targetDescription.session",
  },
  {
    kind: "headless",
    labelKey: "panels.schedule.target.headless",
    descriptionKey: "panels.schedule.targetDescription.headless",
  },
  {
    kind: "workspace",
    labelKey: "panels.schedule.target.workspace",
    descriptionKey: "panels.schedule.targetDescription.workspace",
  },
] as const satisfies ReadonlyArray<{
  descriptionKey: string;
  kind: RuntimeScheduleTargetKind;
  labelKey: string;
}>;

type AsyncCatalog<T> = {
  data: T | null;
  failed: boolean;
  loading: boolean;
};

function useAsyncCatalog<T>(load: () => Promise<T>): AsyncCatalog<T> {
  const [state, setState] = useState<AsyncCatalog<T>>({
    data: null,
    failed: false,
    loading: true,
  });

  useEffect(() => {
    let cancelled = false;
    setState({ data: null, failed: false, loading: true });
    void load()
      .then((data) => {
        if (!cancelled) {
          setState({ data, failed: false, loading: false });
        }
      })
      .catch(() => {
        if (!cancelled) {
          setState({ data: null, failed: true, loading: false });
        }
      });
    return () => {
      cancelled = true;
    };
  }, [load]);

  return state;
}

export function TemplateContextFields({
  errors,
  form,
  onChange,
}: {
  errors: AutomationTemplateFormErrors;
  form: AutomationTemplateForm;
  onChange: (patch: Partial<AutomationTemplateForm>) => void;
}) {
  const { t } = useTranslation("workspace");
  const workspaceCatalog = useAsyncCatalog(listWorkspaceDirectories);
  const profileCatalog = useAsyncCatalog(listRuntimeProfiles);
  const modelCatalog = useAsyncCatalog(listRuntimeModels);
  const workspaces: RuntimeWorkspaceDirectory[] =
    workspaceCatalog.data?.directories ?? [];
  const profiles: RuntimeProfileListEntry[] =
    profileCatalog.data?.profiles ?? [];
  const providers = modelCatalog.data?.providers ?? [];

  const workspaceListUnavailable =
    workspaceCatalog.failed ||
    (!workspaceCatalog.loading && workspaces.length === 0);
  const profileListUnavailable =
    profileCatalog.failed ||
    (!profileCatalog.loading && profiles.length === 0);
  const workspaceOptions = withCurrentSelectOption(
    workspaces.map((directory) => ({
      value: directory.path,
      label: directory.name?.trim()
        ? `${directory.name.trim()} · ${directory.path}`
        : directory.path,
    })),
    form.targetWorkspace,
  );
  const profileOptions = withCurrentSelectOption(
    profiles.map((profile) => ({
      value: profile.ref,
      label: profile.name?.trim()
        ? `${profile.name.trim()} · ${profile.ref}`
        : profile.ref,
    })),
    form.targetProfile,
  );
  return (
    <TemplateSection
      description={t("panels.schedule.sectionContextHint")}
      helpLabel={t("panels.schedule.helpLabel", {
        field: t("panels.schedule.sectionContext"),
      })}
      icon={TargetIcon}
      title={t("panels.schedule.templates.sections.context")}
    >
      <div className="grid gap-3">
        <div>
          <div
            aria-label={t("panels.schedule.targetLabel")}
            className="grid gap-2 sm:grid-cols-3"
            role="radiogroup"
          >
            {TARGET_OPTIONS.map((option) => {
              const checked = form.targetKind === option.kind;
              return (
                <label
                  className={cn(
                    "flex cursor-pointer items-center gap-2 rounded-field border px-2.5 py-2 transition focus-within:ring-2 focus-within:ring-ring",
                    checked
                      ? "border-accent-primary-border bg-accent-primary-soft"
                      : "border-border bg-surface-solid hover:border-border-strong hover:bg-surface-soft",
                  )}
                  key={option.kind}
                >
                  <input
                    checked={checked}
                    className="sr-only"
                    data-testid={`automation-template-target-${option.kind}`}
                    name="automation-template-target-kind"
                    onChange={() => onChange({ targetKind: option.kind })}
                    type="radio"
                    value={option.kind}
                  />
                  <span
                    aria-hidden="true"
                    className={cn(
                      "grid size-4 shrink-0 place-items-center rounded-full border transition",
                      checked
                        ? "border-accent-primary"
                        : "border-border-strong",
                    )}
                  >
                    {checked ? (
                      <span className="size-2 rounded-full bg-accent-primary" />
                    ) : null}
                  </span>
                  <span className="min-w-0 truncate text-xs font-semibold text-foreground">
                    {t(option.labelKey)}
                  </span>
                </label>
              );
            })}
          </div>
          <p className="mt-1.5 app-text-11 leading-4 text-muted-foreground">
            {t(
              TARGET_OPTIONS.find((option) => option.kind === form.targetKind)
                ?.descriptionKey ??
                "panels.schedule.targetDescription.session",
            )}
          </p>
        </div>

        <div className="grid gap-3 sm:grid-cols-2">
          {form.targetKind === "workspace" ? (
            <TemplateField
              className="sm:col-span-2"
              error={templateErrorText(t, errors.targetWorkspace)}
              errorId={templateFieldErrorId("targetWorkspace")}
              help={t("panels.schedule.workspaceHint")}
              helpLabel={t("panels.schedule.helpLabel", {
                field: t("panels.schedule.workspaceLabel"),
              })}
              label={t("panels.schedule.workspaceLabel")}
              required
            >
              {workspaceListUnavailable ? (
                <input
                  className={templateFieldClassName}
                  data-testid="automation-template-workspace"
                  onChange={(event) =>
                    onChange({ targetWorkspace: event.target.value })
                  }
                  placeholder={t("panels.schedule.workspacePlaceholder")}
                  required
                  spellCheck={false}
                  value={form.targetWorkspace}
                  {...templateFieldA11y("targetWorkspace", errors)}
                />
              ) : (
                <Select
                  ariaDescribedBy={
                    errors.targetWorkspace
                      ? templateFieldErrorId("targetWorkspace")
                      : undefined
                  }
                  ariaInvalid={Boolean(errors.targetWorkspace)}
                  ariaLabel={t("panels.schedule.workspacePickPlaceholder")}
                  className="w-full"
                  disabled={workspaceCatalog.loading}
                  onChange={(value) => onChange({ targetWorkspace: value })}
                  options={workspaceOptions}
                  placeholder={t("panels.schedule.workspacePickPlaceholder")}
                  size="md"
                  triggerClassName={templateFieldClassName}
                  triggerTestId="automation-template-workspace"
                  value={form.targetWorkspace}
                />
              )}
              {workspaceCatalog.failed ||
              (!workspaceCatalog.loading && workspaces.length === 0) ? (
                <span className="text-xs text-muted-foreground" role="status">
                  {workspaceCatalog.failed
                    ? t("panels.schedule.templates.optionCatalogLoadFailed")
                    : t("panels.schedule.templates.optionCatalogEmpty")}
                </span>
              ) : null}
            </TemplateField>
          ) : null}

          {form.targetKind !== "session" ? (
            <TemplateField
              className="sm:col-span-2"
              help={t("panels.schedule.profileHint")}
              helpLabel={t("panels.schedule.helpLabel", {
                field: t("panels.schedule.profileLabel"),
              })}
              label={t("panels.schedule.profileLabel")}
            >
              {profileListUnavailable ? (
                <input
                  className={templateFieldClassName}
                  data-testid="automation-template-profile"
                  onChange={(event) =>
                    onChange({ targetProfile: event.target.value })
                  }
                  placeholder={t("panels.schedule.profilePlaceholder")}
                  spellCheck={false}
                  value={form.targetProfile}
                />
              ) : (
                <Select
                  ariaLabel={t("panels.schedule.profileLabel")}
                  className="w-full"
                  disabled={profileCatalog.loading}
                  onChange={(value) => onChange({ targetProfile: value })}
                  options={profileOptions}
                  placeholder={t("panels.schedule.profilePlaceholder")}
                  size="md"
                  triggerClassName={templateFieldClassName}
                  triggerTestId="automation-template-profile"
                  value={form.targetProfile}
                />
              )}
              {profileCatalog.failed ||
              (!profileCatalog.loading && profiles.length === 0) ? (
                <span className="text-xs text-muted-foreground" role="status">
                  {profileCatalog.failed
                    ? t("panels.schedule.templates.optionCatalogLoadFailed")
                    : t("panels.schedule.templates.optionCatalogEmpty")}
                </span>
              ) : null}
            </TemplateField>
          ) : null}

          <TemplateField
            className="sm:col-span-2"
            error={templateErrorText(t, errors.goal)}
            errorId={templateFieldErrorId("goal")}
            help={t("panels.schedule.help.goal")}
            helpLabel={t("panels.schedule.helpLabel", {
              field: t("panels.schedule.goalLabel"),
            })}
            label={t("panels.schedule.goalLabel")}
            required={form.targetKind !== "session"}
          >
            <input
              className={templateFieldClassName}
              data-testid="automation-template-goal"
              onChange={(event) => onChange({ goal: event.target.value })}
              required={form.targetKind !== "session"}
              value={form.goal}
              {...templateFieldA11y("goal", errors)}
            />
          </TemplateField>

          <TemplateModelFields
            failed={modelCatalog.failed}
            form={form}
            loading={modelCatalog.loading}
            onChange={onChange}
            providers={providers}
          />

          <TemplateField
            className="sm:col-span-2"
            help={t("panels.schedule.help.plan")}
            helpLabel={t("panels.schedule.helpLabel", {
              field: t("panels.schedule.planLabel"),
            })}
            label={t("panels.schedule.planLabel")}
          >
            <textarea
              className={templateCompactTextareaClassName}
              data-testid="automation-template-plan"
              onChange={(event) => onChange({ plan: event.target.value })}
              placeholder={t("panels.schedule.planPlaceholder")}
              rows={2}
              value={form.plan}
            />
          </TemplateField>
        </div>

        {form.targetKind !== "session" ? (
          <p className="flex items-start gap-2 rounded-field border border-dashed border-border bg-surface-solid px-3 py-2 text-xs leading-5 text-muted-foreground">
            <InfoIcon
              aria-hidden="true"
              className="mt-0.5 size-3.5 shrink-0 text-accent-secondary"
            />
            <span>{t("panels.schedule.targetHint")}</span>
          </p>
        ) : null}
      </div>
    </TemplateSection>
  );
}
