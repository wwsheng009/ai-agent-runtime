// 自动化任务新建对话框的字段分组。
//
// 按「任务内容 / 执行上下文 / 运行规则」拆成三个局部组件，避免弹层外壳和
// 条件字段堆在同一个文件里；所有写操作都通过同一个 patchForm 更新。

import {
  CalendarClockIcon,
  FileTextIcon,
  InfoIcon,
  PencilLineIcon,
  TargetIcon,
  type LucideIcon,
} from "lucide-react";
import { useEffect, useId, useState, type ReactNode, type RefObject } from "react";
import { useTranslation } from "react-i18next";

import { listRuntimeProfiles } from "@/api/runtime/profiles";
import { listRuntimeModels } from "@/api/runtime/models";
import { listRuntimeSessions } from "@/api/runtime/sessions";
import { listWorkspaceDirectories } from "@/api/runtime/workspace-directories";
import {
  fieldControlVariants,
  fieldTextareaVariants,
} from "@/components/ui/field-variants";
import { Select } from "@/components/ui/select";
import { scheduleKindLabelKey } from "@/components/workspace/schedule-panel-shared";
import { cn } from "@/lib/utils";
import type {
  RuntimeProfileListEntry,
  RuntimeModelsResponse,
  RuntimeScheduleKind,
  RuntimeScheduleTargetKind,
  RuntimeSessionRecord,
  RuntimeWorkspaceDirectory,
} from "@/types/runtime";

import { CronAssist } from "./cron-assist";
import { createKinds } from "./form-state";
import type {
  CreateFormState,
  ScheduleFormErrors,
  ScheduleFormField,
} from "./form-state";
import { HelpTip } from "./help-tip";
import { WeekdayPicker } from "./weekday-picker";

const fieldClassName = fieldControlVariants({ size: "md" });
// 多行框统一压低最小高度：变体自带的 min-h-24（96px）对两个短内容字段太高，
// 空框会白占两行位置，把底部按钮顶到需要滚动才能看到。内容多了仍可手动拉伸。
const textareaClassName = cn(
  fieldTextareaVariants({ size: "sm" }),
  "min-h-[4.25rem]",
);
// 执行步骤多数情况留空，给到接近单行输入的高度即可。
const compactTextareaClassName = cn(
  fieldTextareaVariants({ size: "sm" }),
  "min-h-[3.25rem]",
);

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

type PatchForm = (patch: Partial<CreateFormState>) => void;

type SectionFieldsProps = {
  createForm: CreateFormState;
  errors: ScheduleFormErrors;
  patchForm: PatchForm;
};

type ContentFieldsProps = SectionFieldsProps & {
  titleInputRef: RefObject<HTMLInputElement | null>;
};

type FormSectionProps = {
  children: ReactNode;
  description: string;
  icon: LucideIcon;
  title: string;
};

function FormSection({
  children,
  description,
  icon: Icon,
  title,
}: FormSectionProps) {
  const { t } = useTranslation("workspace");
  const titleId = useId();
  return (
    <section
      aria-labelledby={titleId}
      className="rounded-card border border-border bg-surface-softer p-3.5 sm:p-4"
    >
      {/* 标题行只留一行：图标 + 标题 + 说明入口。分区说明从常驻段落收进浮层，
          避免每个区块先来一句解释，把主字段挤到下面。 */}
      <div className="mb-3 flex items-center gap-2">
        <span className="grid size-6 shrink-0 place-items-center rounded-field border border-border bg-surface-solid text-accent-secondary">
          <Icon aria-hidden="true" size={13} />
        </span>
        <h3
          className="min-w-0 flex-1 text-sm font-semibold tracking-[-0.01em] text-foreground"
          id={titleId}
        >
          {title}
        </h3>
        <HelpTip
          label={t("panels.schedule.helpLabel", { field: title })}
          text={description}
        />
      </div>
      {children}
    </section>
  );
}

function FieldLabel({
  children,
  required = false,
}: {
  children: ReactNode;
  required?: boolean;
}) {
  return (
    <span className="flex items-center gap-1 text-xs font-medium text-foreground">
      {children}
      {required ? (
        <span aria-hidden="true" className="text-accent-orange">
          *
        </span>
      ) : null}
    </span>
  );
}

function Field({
  action,
  children,
  className,
  error,
  errorId,
  help,
  helpLabel,
  label,
  required = false,
}: {
  /** 字段标题行右侧的动作入口，例如 Cron 可视化助手。 */
  action?: ReactNode;
  children: ReactNode;
  className?: string;
  error?: string;
  errorId?: string;
  /** 字段说明，展示在问号提示气泡中。 */
  help?: string;
  /** 提示按钮的无障碍名称。 */
  helpLabel?: string;
  /** 渲染在输入控件之外（仍在 label 外）的补充内容，例如 Cron 可视化区块。 */
  label: ReactNode;
  required?: boolean;
}) {
  const hasHelp = Boolean(help && helpLabel);

  return (
    <div className={cn("grid gap-1.5", className)}>
      <div className="flex items-start justify-between gap-2">
        <label className="grid min-w-0 flex-1 gap-1.5">
          <FieldLabel required={required}>{label}</FieldLabel>
          {children}
        </label>
        {action || hasHelp ? (
          <div className="mt-0.5 flex shrink-0 items-center gap-1">
            {action}
            {hasHelp ? (
              <HelpTip label={helpLabel as string} text={help as string} />
            ) : null}
          </div>
        ) : null}
      </div>
      {error ? (
        <span
          className="text-xs text-destructive"
          id={errorId}
          data-testid="schedule-field-error"
        >
          {error}
        </span>
      ) : null}
    </div>
  );
}

/** 常用时区：覆盖国内与主流出海/协作场景，其余时区仍可在下方手填。 */
const COMMON_TIME_ZONES = [
  "Asia/Shanghai",
  "Asia/Hong_Kong",
  "Asia/Taipei",
  "Asia/Tokyo",
  "Asia/Singapore",
  "Europe/London",
  "Europe/Berlin",
  "America/New_York",
  "America/Los_Angeles",
  "UTC",
] as const;

const CUSTOM_ZONE_VALUE = "__custom__";


function fieldA11y(field: ScheduleFormField, errors: ScheduleFormErrors) {
  return {
    "aria-describedby": errors[field] ? `schedule-field-error-${field}` : undefined,
    "aria-invalid": errors[field] ? true : undefined,
  };
}

export function ScheduleContentFields({
  createForm,
  errors,
  patchForm,
  titleInputRef,
}: ContentFieldsProps) {
  const { t } = useTranslation("workspace");
  return (
    <FormSection
      description={t("panels.schedule.sectionContentHint")}
      icon={FileTextIcon}
      title={t("panels.schedule.sectionContent")}
    >
      <div className="grid gap-3">
        <Field
          error={errors.title ? t("panels.schedule.validation.required") : undefined}
          errorId="schedule-field-error-title"
          label={t("panels.schedule.titleLabel")}
          required
        >
          <input
            className={fieldClassName}
            data-testid="schedule-create-title"
            onChange={(event) => patchForm({ title: event.target.value })}
            ref={titleInputRef}
            required
            value={createForm.title}
            {...fieldA11y("title", errors)}
          />
        </Field>
        <Field
          error={errors.prompt ? t("panels.schedule.validation.required") : undefined}
          errorId="schedule-field-error-prompt"
          label={t("panels.schedule.promptLabel")}
          required
        >
          <textarea
            className={textareaClassName}
            data-testid="schedule-create-prompt"
            onChange={(event) => patchForm({ prompt: event.target.value })}
            required
            rows={3}
            value={createForm.prompt}
            {...fieldA11y("prompt", errors)}
          />
        </Field>
      </div>
    </FormSection>
  );
}

export function ScheduleContextFields({
  createForm,
  errors,
  patchForm,
}: SectionFieldsProps) {
  const { t } = useTranslation("workspace");
  const [sessions, setSessions] = useState<RuntimeSessionRecord[]>([]);
  const [sessionsLoading, setSessionsLoading] = useState(false);
  const [sessionsLoadFailed, setSessionsLoadFailed] = useState(false);

  useEffect(() => {
    let cancelled = false;
    setSessionsLoading(true);
    setSessionsLoadFailed(false);
    void listRuntimeSessions()
      .then((response) => {
        if (!cancelled) {
          setSessions(response.sessions);
        }
      })
      .catch(() => {
        if (!cancelled) {
          setSessionsLoadFailed(true);
        }
      })
      .finally(() => {
        if (!cancelled) {
          setSessionsLoading(false);
        }
      });
    return () => {
      cancelled = true;
    };
  }, []);

  // 工作区目录：给「目标工作区」选择器供数。列表不可用时字段退回手输。
  const [workspaces, setWorkspaces] = useState<RuntimeWorkspaceDirectory[]>([]);
  const [workspacesLoading, setWorkspacesLoading] = useState(false);
  const [workspacesLoadFailed, setWorkspacesLoadFailed] = useState(false);

  useEffect(() => {
    let cancelled = false;
    setWorkspacesLoading(true);
    setWorkspacesLoadFailed(false);
    void listWorkspaceDirectories()
      .then((response) => {
        if (!cancelled) {
          setWorkspaces(response.directories);
        }
      })
      .catch(() => {
        if (!cancelled) {
          setWorkspacesLoadFailed(true);
        }
      })
      .finally(() => {
        if (!cancelled) {
          setWorkspacesLoading(false);
        }
      });
    return () => {
      cancelled = true;
    };
  }, []);

  // Profile 清单：只在「无人值守 / 工作区」用到，仍统一加载一次，
  // 切换执行上下文时无需再等待。
  const [profiles, setProfiles] = useState<RuntimeProfileListEntry[]>([]);
  const [profilesLoading, setProfilesLoading] = useState(false);
  const [profilesLoadFailed, setProfilesLoadFailed] = useState(false);
  const [runtimeModels, setRuntimeModels] = useState<RuntimeModelsResponse | null>(null);
  const [modelsLoading, setModelsLoading] = useState(false);

  useEffect(() => {
    let cancelled = false;
    setProfilesLoading(true);
    setProfilesLoadFailed(false);
    void listRuntimeProfiles()
      .then((response) => {
        if (!cancelled) {
          setProfiles(response.profiles);
        }
      })
      .catch(() => {
        if (!cancelled) {
          setProfilesLoadFailed(true);
        }
      })
      .finally(() => {
        if (!cancelled) {
          setProfilesLoading(false);
        }
      });
    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    let cancelled = false;
    setModelsLoading(true);
    void listRuntimeModels()
      .then((response) => {
        if (!cancelled) setRuntimeModels(response);
      })
      .catch(() => {
        if (!cancelled) setRuntimeModels(null);
      })
      .finally(() => {
        if (!cancelled) setModelsLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const workspaceListUnavailable =
    workspacesLoadFailed || (!workspacesLoading && workspaces.length === 0);
  const profileListUnavailable =
    profilesLoadFailed || (!profilesLoading && profiles.length === 0);
  const providerOptions = (runtimeModels?.providers ?? []).map((provider) => ({
    value: provider.name,
    label: provider.name,
  }));
  const selectedProvider = runtimeModels?.providers.find(
    (provider) => provider.name === createForm.provider,
  );
  const modelOptions = (selectedProvider?.models ?? []).map((model) => ({
    value: model,
    label: model,
  }));
  const reasoningOptions =
    selectedProvider?.model_capabilities?.[createForm.model]?.reasoning_efforts ?? [];

  // 值用目录路径（后端按路径识别工作区），标签优先显示目录名。
  const workspaceOptions = [
    ...(workspacesLoading
      ? [
          {
            value: "",
            label: t("panels.schedule.workspaceLoading"),
            disabled: true,
          },
        ]
      : []),
    ...workspaces.map((directory) => ({
      value: directory.path,
      label: directory.name?.trim()
        ? `${directory.name.trim()} · ${directory.path}`
        : directory.path,
    })),
  ].filter(
    (option) =>
      option.value !== "" ||
      createForm.targetWorkspace === "" ||
      workspacesLoading,
  );

  // 已填但不在清单里的值也要能显示出来，否则选中的工作区会显示成空。
  const hasSelectedWorkspace =
    createForm.targetWorkspace.length > 0 &&
    !workspaces.some((directory) => directory.path === createForm.targetWorkspace);
  if (hasSelectedWorkspace) {
    workspaceOptions.unshift({
      value: createForm.targetWorkspace,
      label: createForm.targetWorkspace,
    });
  }

  const profileOptions = [
    ...profiles.map((profile) => ({
      value: profile.ref,
      label: profile.name?.trim()
        ? `${profile.name.trim()} · ${profile.ref}`
        : profile.ref,
    })),
  ];
  const hasSelectedProfile =
    createForm.targetProfile.length > 0 &&
    !profiles.some((profile) => profile.ref === createForm.targetProfile);
  if (hasSelectedProfile) {
    profileOptions.unshift({
      value: createForm.targetProfile,
      label: createForm.targetProfile,
    });
  }

  const [manualSession, setManualSession] = useState(false);
  const sessionListUnavailable =
    sessionsLoadFailed || (!sessionsLoading && sessions.length === 0);
  const showSessionInput = manualSession || sessionListUnavailable;

  const hasSelectedSession =
    createForm.sessionId.length > 0 &&
    !sessions.some((session) => session.id === createForm.sessionId);
  const sessionOptions = [
    {
      value: "",
      label: sessionsLoading
        ? t("panels.schedule.sessionLoading")
        : t("panels.schedule.sessionSelectPlaceholder"),
    },
    ...(hasSelectedSession
      ? [{ value: createForm.sessionId, label: createForm.sessionId }]
      : []),
    ...sessions.map((session) => ({
      value: session.id,
      label: session.metadata?.title?.trim()
        ? `${session.metadata.title.trim()} · ${session.id}`
        : session.id,
    })),
  ];

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

export function ScheduleRuleFields({
  createForm,
  errors,
  patchForm,
}: SectionFieldsProps) {
  const { t } = useTranslation("workspace");
  // 是否处于「自定义时区」手输模式。默认收起输入框：常用时区走下拉，
  // 两个同值控件并排会让用户误以为要填两遍。
  const [zoneCustom, setZoneCustom] = useState(false);
  const zoneIsCustom =
    zoneCustom ||
    !(COMMON_TIME_ZONES as readonly string[]).includes(createForm.timeZone);

  return (
    <FormSection
      description={t("panels.schedule.sectionRuleHint")}
      icon={CalendarClockIcon}
      title={t("panels.schedule.sectionRule")}
    >
      <div className="grid gap-3 sm:grid-cols-2">
        <Field
          className="sm:col-span-2"
          label={t("panels.schedule.kindLabel")}
          required
        >
          <Select
            ariaLabel={t("panels.schedule.kindLabel")}
            className="w-full"
            onChange={(kind) =>
              patchForm({
                kind: kind as RuntimeScheduleKind,
                atTime: "",
                clockTime: "",
              })
            }
            options={createKinds.map((kind) => ({
              value: kind,
              label: t(scheduleKindLabelKey(kind)),
            }))}
            triggerClassName={fieldClassName}
            triggerTestId="schedule-kind"
            value={createForm.kind}
          />
        </Field>

        {createForm.kind === "after" || createForm.kind === "every" ? (
          <Field
            error={errors.amount ? t("panels.schedule.validation.positive") : undefined}
            errorId="schedule-field-error-amount"
            label={t(
              createForm.kind === "after"
                ? "panels.schedule.minutesAfter"
                : "panels.schedule.minutesEvery",
            )}
            required
          >
            <input
              className={fieldClassName}
              data-testid="schedule-amount"
              inputMode="numeric"
              min={createForm.kind === "after" ? 0.1 : 1}
              onChange={(event) => patchForm({ amount: event.target.value })}
              required
              step="any"
              type="number"
              value={createForm.amount}
              {...fieldA11y("amount", errors)}
            />
          </Field>
        ) : null}

        {createForm.kind === "at" ? (
          <Field
            className="sm:col-span-2"
            error={errors.atTime ? t("panels.schedule.validation.required") : undefined}
            errorId="schedule-field-error-atTime"
            label={t("panels.schedule.atLabel")}
            required
          >
            <input
              className={fieldClassName}
              data-testid="schedule-at-time"
              onChange={(event) => patchForm({ atTime: event.target.value })}
              required
              step={1}
              type="datetime-local"
              value={createForm.atTime}
              {...fieldA11y("atTime", errors)}
            />
          </Field>
        ) : null}

        {createForm.kind === "daily" || createForm.kind === "weekly" ? (
          <Field
            error={errors.clockTime ? t("panels.schedule.validation.clock") : undefined}
            errorId="schedule-field-error-clockTime"
            label={t("panels.schedule.timeLabel")}
            required
          >
            <input
              className={fieldClassName}
              data-testid="schedule-clock-time"
              inputMode="numeric"
              onChange={(event) =>
                patchForm({ clockTime: event.target.value })
              }
              placeholder={t("panels.schedule.timePlaceholder")}
              required
              value={createForm.clockTime}
              {...fieldA11y("clockTime", errors)}
            />
          </Field>
        ) : null}

        {createForm.kind === "daily" ||
        createForm.kind === "weekly" ||
        createForm.kind === "cron" ? (
          <Field
            className="sm:col-span-2"
            error={errors.timeZone ? t("panels.schedule.validation.required") : undefined}
            errorId="schedule-field-error-timeZone"
            help={t("panels.schedule.help.timeZone")}
            helpLabel={t("panels.schedule.helpLabel", { field: t("panels.schedule.zoneLabel") })}
            label={t("panels.schedule.zoneLabel")}
            required
          >
            <Select
              ariaLabel={t("panels.schedule.zonePresetLabel")}
              className="w-full"
              onChange={(zone) => {
                if (zone === CUSTOM_ZONE_VALUE) {
                  setZoneCustom(true);
                  return;
                }
                setZoneCustom(false);
                patchForm({ timeZone: zone });
              }}
              options={[
                ...COMMON_TIME_ZONES.map((zone) => ({ value: zone, label: zone })),
                {
                  value: CUSTOM_ZONE_VALUE,
                  label: t("panels.schedule.zoneCustomOption"),
                },
              ]}
              triggerClassName={fieldClassName}
              triggerTestId="schedule-time-zone-preset"
              value={
                zoneIsCustom ? CUSTOM_ZONE_VALUE : createForm.timeZone
              }
            />
            {zoneIsCustom ? (
              <input
                className={cn(fieldClassName, "mt-1.5")}
                data-testid="schedule-time-zone"
                onChange={(event) => patchForm({ timeZone: event.target.value })}
                placeholder={t("panels.schedule.zonePlaceholder")}
                required
                spellCheck={false}
                value={createForm.timeZone}
                {...fieldA11y("timeZone", errors)}
              />
            ) : null}
          </Field>
        ) : null}

        {createForm.kind === "weekly" ? (
          <div className="sm:col-span-2">
            <WeekdayPicker
              describedBy={
                errors.weekdays ? "schedule-field-error-weekdays" : undefined
              }
              invalid={Boolean(errors.weekdays)}
              onToggle={(day) =>
                patchForm({
                  weekdays: createForm.weekdays.includes(day)
                    ? createForm.weekdays.filter((value) => value !== day)
                    : [...createForm.weekdays, day],
                })
              }
              selected={createForm.weekdays}
            />
            {errors.weekdays ? (
              <p
                className="mt-1 text-xs text-destructive"
                id="schedule-field-error-weekdays"
                role="alert"
              >
                {t("panels.schedule.validation.requiredWeekday")}
              </p>
            ) : null}
          </div>
        ) : null}

        {createForm.kind === "cron" ? (
          <Field
            className="sm:col-span-2"
            error={errors.cron ? t("panels.schedule.validation.required") : undefined}
            errorId="schedule-field-error-cron"
            help={t("panels.schedule.help.cron")}
            helpLabel={t("panels.schedule.helpLabel", {
              field: t("panels.schedule.cronLabel"),
            })}
            label={t("panels.schedule.cronLabel")}
            required
            action={
              <CronAssist
                onPick={(expression) => patchForm({ cron: expression })}
                value={createForm.cron}
              />
            }
          >
            <input
              className={fieldClassName}
              data-testid="schedule-cron"
              onChange={(event) =>
                patchForm({ cron: event.target.value })
              }
              placeholder="*/5 * * * *"
              required
              spellCheck={false}
              value={createForm.cron}
              {...fieldA11y("cron", errors)}
            />
          </Field>
        ) : null}
      </div>
    </FormSection>
  );
}