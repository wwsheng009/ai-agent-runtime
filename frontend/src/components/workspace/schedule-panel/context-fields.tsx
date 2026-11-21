// 自动化任务新建对话框「执行上下文」分区：目标类型、会话/工作区、
// profile 与模型路由。远端数据加载与下拉选项派生在
// use-context-field-options.ts 中完成。

import { InfoIcon, PencilLineIcon, TargetIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Select } from "@/components/ui/select";
import { cn } from "@/lib/utils";

import { Field, FormSection } from "./form-dialog-field-primitives";
import {
  fieldA11y,
  fieldClassName,
  compactTextareaClassName,
  TARGET_OPTIONS,
  type SectionFieldsProps,
} from "./form-dialog-field-support";
import { useScheduleContextOptions } from "./use-context-field-options";

export function ScheduleContextFields({
  createForm,
  errors,
  patchForm,
}: SectionFieldsProps) {
  const { t } = useTranslation("workspace");
  const {
    modelOptions,
    modelsLoading,
    profileListUnavailable,
    profileOptions,
    profilesLoading,
    providerOptions,
    reasoningOptions,
    runtimeModels,
    selectedProvider,
    sessionListUnavailable,
    sessionOptions,
    sessionsLoadFailed,
    sessionsLoading,
    setManualSession,
    showSessionInput,
    workspaceListUnavailable,
    workspaceOptions,
    workspacesLoadFailed,
    workspacesLoading,
  } = useScheduleContextOptions(createForm);

  return (
    <FormSection
      description={t("panels.schedule.sectionContextHint")}
      icon={TargetIcon}
      title={t("panels.schedule.sectionContext")}
    >
      <div className="grid gap-3">
        {/* 选项组不再额外加一行标签：分区标题已经是「执行上下文」，
            再重复一次只是占掉一行高度；可访问名称由 aria-label 提供。 */}
        <div>
          <div
            aria-label={t("panels.schedule.targetLabel")}
            className="grid gap-2 sm:grid-cols-3"
            role="radiogroup"
          >
            {TARGET_OPTIONS.map((option) => {
              const checked = createForm.targetKind === option.kind;
              return (
                <label
                  className={cn(
                    // 卡片只放标题，说明统一下移到组下方一行：
                    // 否则带说明的选中项会比邻居高一截，三张卡片底边参差不齐。
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
                    data-testid={`schedule-target-${option.kind}`}
                    name="schedule-target-kind"
                    onChange={() => patchForm({ targetKind: option.kind })}
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
          {/* 当前选项的说明只在组下方出现一次，保持三张卡片等高、信息也不丢。 */}
          <p className="mt-1.5 app-text-11 leading-4 text-muted-foreground">
            {t(
              TARGET_OPTIONS.find((option) => option.kind === createForm.targetKind)
                ?.descriptionKey ?? "panels.schedule.targetDescription.session",
            )}
          </p>
        </div>

        <div className="grid gap-3 sm:grid-cols-2">
          {createForm.targetKind === "session" ? (
            <Field
              className="sm:col-span-2"
              error={errors.sessionId ? t("panels.schedule.validation.required") : undefined}
              errorId="schedule-field-error-sessionId"
              help={t("panels.schedule.sessionManualHint")}
              helpLabel={t("panels.schedule.helpLabel", {
                field: t("panels.schedule.sessionLabel"),
              })}
              label={t("panels.schedule.sessionLabel")}
              required
            >
              <div className="grid gap-2">
                {/* 下拉与输入框不再并列：两者都是 sessionId，同值并排会让人以为要填两遍。
                    默认只用下拉选会话；列表不可用或用户主动切换时才显示输入框。 */}
                {showSessionInput ? (
                  <input
                    className={fieldClassName}
                    data-testid="schedule-session-id"
                    onChange={(event) =>
                      patchForm({ sessionId: event.target.value })
                    }
                    placeholder={t("panels.schedule.sessionPlaceholder")}
                    required
                    value={createForm.sessionId}
                    {...fieldA11y("sessionId", errors)}
                  />
                ) : (
                  <Select
                    ariaLabel={t("panels.schedule.sessionSelectPlaceholder")}
                    className="w-full"
                    disabled={sessionsLoading}
                    onChange={(sessionId) => patchForm({ sessionId })}
                    options={sessionOptions}
                    size="md"
                    triggerClassName={fieldClassName}
                    triggerTestId="schedule-session-select"
                    value={createForm.sessionId}
                  />
                )}
                {sessionListUnavailable ? (
                  <span className="text-xs text-muted-foreground" role="status">
                    {sessionsLoadFailed
                      ? t("panels.schedule.sessionLoadFailed")
                      : t("panels.schedule.sessionListEmpty")}
                  </span>
                ) : (
                  <button
                    aria-expanded={showSessionInput}
                    className={cn(
                      "-mt-0.5 inline-flex w-fit cursor-pointer items-center gap-1 rounded-control px-1 py-0.5",
                      "app-text-11 text-muted-foreground transition-colors",
                      "hover:bg-surface-soft hover:text-foreground",
                      "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                    )}
                    onClick={() => setManualSession((current) => !current)}
                    type="button"
                  >
                    <PencilLineIcon aria-hidden="true" size={11} />
                    {showSessionInput
                      ? t("panels.schedule.sessionPickToggle")
                      : t("panels.schedule.sessionManualToggle")}
                  </button>
                )}
              </div>
            </Field>
          ) : null}

          {createForm.targetKind === "workspace" ? (
            <Field
              className="sm:col-span-2"
              error={errors.targetWorkspace ? t("panels.schedule.validation.required") : undefined}
              errorId="schedule-field-error-targetWorkspace"
              help={t("panels.schedule.workspaceHint")}
              helpLabel={t("panels.schedule.helpLabel", {
                field: t("panels.schedule.workspaceLabel"),
              })}
              label={t("panels.schedule.workspaceLabel")}
              required
            >
              <div className="grid gap-2">
                {/* 工作区目录是可枚举的既有资源，直接给选择器，避免手敲路径出错。
                    目录列表不可用时退回手输，保证「无人值守/工作区」仍能创建。 */}
                {workspaceListUnavailable || workspacesLoading ? (
                  <input
                    className={fieldClassName}
                    data-testid="schedule-target-workspace"
                    onChange={(event) =>
                      patchForm({ targetWorkspace: event.target.value })
                    }
                    placeholder={t("panels.schedule.workspacePlaceholder")}
                    required
                    spellCheck={false}
                    value={createForm.targetWorkspace}
                    {...fieldA11y("targetWorkspace", errors)}
                  />
                ) : (
                  <Select
                    ariaLabel={t("panels.schedule.workspacePickPlaceholder")}
                    className="w-full"
                    disabled={workspacesLoading}
                    onChange={(value) => patchForm({ targetWorkspace: value })}
                    options={workspaceOptions}
                    placeholder={t("panels.schedule.workspacePickPlaceholder")}
                    size="md"
                    triggerClassName={fieldClassName}
                    triggerTestId="schedule-target-workspace"
                    value={createForm.targetWorkspace}
                  />
                )}
                {workspaceListUnavailable && workspacesLoadFailed ? (
                  <span className="text-xs text-muted-foreground" role="status">
                    {t("panels.schedule.workspaceLoadFailed")}
                  </span>
                ) : null}
              </div>
            </Field>
          ) : null}

          {createForm.targetKind !== "session" ? (
            <Field
              help={t("panels.schedule.profileHint")}
              helpLabel={t("panels.schedule.helpLabel", {
                field: t("panels.schedule.profileLabel"),
              })}
              label={t("panels.schedule.profileLabel")}
            >
              {/* Profile 取值来自运行时 profile 清单，用下拉挑比记名字可靠；
                  清单拉取失败时退回手输，保持可用。 */}
              {profileListUnavailable ? (
                <input
                  className={fieldClassName}
                  data-testid="schedule-target-profile"
                  onChange={(event) =>
                    patchForm({ targetProfile: event.target.value })
                  }
                  placeholder={t("panels.schedule.profilePlaceholder")}
                  spellCheck={false}
                  value={createForm.targetProfile}
                />
              ) : (
                <Select
                  ariaLabel={t("panels.schedule.profileLabel")}
                  className="w-full"
                  disabled={profilesLoading}
                  onChange={(value) => patchForm({ targetProfile: value })}
                  options={profileOptions}
                  placeholder={t("panels.schedule.profilePlaceholder")}
                  size="md"
                  triggerClassName={fieldClassName}
                  triggerTestId="schedule-target-profile"
                  value={createForm.targetProfile}
                />
              )}
            </Field>
          ) : null}

          <Field
            className="sm:col-span-2"
            error={errors.goal ? t("panels.schedule.validation.required") : undefined}
            errorId="schedule-field-error-goal"
            help={t("panels.schedule.help.goal")}
            helpLabel={t("panels.schedule.helpLabel", {
              field: t("panels.schedule.goalLabel"),
            })}
            label={t("panels.schedule.goalLabel")}
            required={createForm.targetKind !== "session"}
          >
            <input
              className={fieldClassName}
              data-testid="schedule-goal"
              onChange={(event) => patchForm({ goal: event.target.value })}
              required={createForm.targetKind !== "session"}
              value={createForm.goal}
              {...fieldA11y("goal", errors)}
            />
          </Field>

          <Field
            help={t("panels.schedule.modelHint")}
            helpLabel={t("panels.schedule.helpLabel", {
              field: t("panels.schedule.providerLabel"),
            })}
            label={t("panels.schedule.providerLabel")}
          >
            <Select
              ariaLabel={t("panels.schedule.providerLabel")}
              disabled={modelsLoading}
              onChange={(value) => {
                const provider = runtimeModels?.providers.find((item) => item.name === value);
                const model = provider?.models.includes(createForm.model)
                  ? createForm.model
                  : "";
                const reasoning = provider?.model_capabilities?.[model]?.reasoning_efforts ?? [];
                patchForm({
                  provider: value,
                  model,
                  reasoningEffort: reasoning.includes(createForm.reasoningEffort)
                    ? createForm.reasoningEffort
                    : "",
                });
              }}
              options={[
                { value: "", label: t("panels.schedule.providerDefault") },
                ...providerOptions,
              ]}
              triggerClassName={fieldClassName}
              triggerTestId="schedule-provider"
              value={createForm.provider}
            />
          </Field>

          <Field
            help={t("panels.schedule.modelHint")}
            helpLabel={t("panels.schedule.helpLabel", {
              field: t("panels.schedule.modelLabel"),
            })}
            label={t("panels.schedule.modelLabel")}
          >
            <Select
              ariaLabel={t("panels.schedule.modelLabel")}
              disabled={modelsLoading || !createForm.provider}
              onChange={(model) => {
                const reasoning =
                  selectedProvider?.model_capabilities?.[model]?.reasoning_efforts ?? [];
                patchForm({
                  model,
                  reasoningEffort: reasoning.includes(createForm.reasoningEffort)
                    ? createForm.reasoningEffort
                    : "",
                });
              }}
              options={[
                { value: "", label: t("panels.schedule.modelDefault") },
                ...modelOptions,
              ]}
              triggerClassName={fieldClassName}
              triggerTestId="schedule-model"
              value={createForm.model}
            />
          </Field>

          {reasoningOptions.length > 0 ? (
            <Field
              help={t("panels.schedule.modelHint")}
              helpLabel={t("panels.schedule.helpLabel", {
                field: t("panels.schedule.reasoningEffortLabel"),
              })}
              label={t("panels.schedule.reasoningEffortLabel")}
            >
              <Select
                ariaLabel={t("panels.schedule.reasoningEffortLabel")}
                onChange={(reasoningEffort) => patchForm({ reasoningEffort })}
                options={[
                  { value: "", label: t("panels.schedule.reasoningEffortDefault") },
                  ...reasoningOptions.map((effort) => ({ value: effort, label: effort })),
                ]}
                triggerClassName={fieldClassName}
                triggerTestId="schedule-reasoning-effort"
                value={createForm.reasoningEffort}
              />
            </Field>
          ) : null}

          <Field
            className="sm:col-span-2"
            help={t("panels.schedule.help.plan")}
            helpLabel={t("panels.schedule.helpLabel", {
              field: t("panels.schedule.planLabel"),
            })}
            label={t("panels.schedule.planLabel")}
          >
            <textarea
              className={compactTextareaClassName}
              data-testid="schedule-plan"
              onChange={(event) => patchForm({ plan: event.target.value })}
              placeholder={t("panels.schedule.planPlaceholder")}
              rows={2}
              value={createForm.plan}
            />
          </Field>
        </div>

        {createForm.targetKind !== "session" ? (
          <p className="flex items-start gap-2 rounded-field border border-dashed border-border bg-surface-solid px-3 py-2 text-xs leading-5 text-muted-foreground">
            <InfoIcon
              aria-hidden="true"
              className="mt-0.5 size-3.5 shrink-0 text-accent-secondary"
            />
            <span>{t("panels.schedule.targetHint")}</span>
          </p>
        ) : null}
      </div>
    </FormSection>
  );
}