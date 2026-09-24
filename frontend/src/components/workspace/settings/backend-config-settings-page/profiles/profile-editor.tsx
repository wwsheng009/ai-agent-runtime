// Profiles 9 卡片编辑器（内联面板）：草稿 / 校验 / 预览 / 保存 / 冲突的唯一状态机。
//
// 分工纪律：
//   * 卡片层无状态，只消费 ProfileCardContext；
//   * 本文件持有 draft、本地 issue、后端 validate/preview 报告与保存流程；
//   * 保存前先跑本地校验：本地能判定的问题不打后端，直接跳到出错卡片；
//   * 任何草稿改动都会清掉上一次的 validate/preview 结果（它们只对当时的文档成立），
//     避免「改了字段但界面还显示上一次校验通过」的假象。

import {
  BookUserIcon,
  FileTextIcon,
  InfoIcon,
  LayersIcon,
  RefreshCcwIcon,
  ServerIcon,
  ShieldCheckIcon,
  SlidersHorizontalIcon,
  WrenchIcon,
  XIcon,
  type LucideIcon,
} from "lucide-react";
import { useCallback, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import {
  getRuntimeProfile,
  previewRuntimeProfile,
  updateRuntimeProfile,
  validateRuntimeProfile,
} from "@/api/runtime/profiles";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import type {
  RuntimeProfilePreviewReport,
  RuntimeProfileValidationReport,
  RuntimeProfileView,
} from "@/types/runtime";

import { SettingsNoticeCard } from "../../settings-notice-card";
import { ProfileAgentsCard, ProfilePromptsCard } from "./profile-card-agent";
import { ProfileBasicsCard, ProfilePreferencesCard } from "./profile-card-basic";
import { ProfileOverridesCard, ProfileValidationCard } from "./profile-card-overrides";
import { ProfileMcpCard, ProfileSkillsCard, ProfileToolsCard } from "./profile-card-scope";
import {
  profileCardForIssuePath,
  profileCardIds,
  profileCardIssueCount,
  type ProfileCardContext,
  type ProfileCardId,
  type ProfileValidationState,
} from "./profile-editor-context";
import {
  buildProfileDocument,
  createProfileDraft,
  diffProfileDraft,
  validateProfileDraft,
  type ProfileDraft,
  type ProfileDraftIssue,
  type ProfileDraftOverride,
  type ProfileDraftValidationOptions,
} from "./profile-draft";
import { formatProfileError, isProfileWriteConflict } from "./profile-i18n";

/**
 * 校验选项：后端不提供 override 白名单数组，只有逐键 allowed
 * （overrideKeyAllowed 用 D14 同一校验器判定，profiles_view_groups.go:242），
 * 因此这里不传 allowedOverrideKeys——新增键的判断交给 /validate 兜底，
 * 前端不维护会漂移的本地白名单。
 */
const emptyProfileValidationOptions: ProfileDraftValidationOptions = {};

const PROFILE_CARD_META = {
  basic: {
    icon: InfoIcon,
    labelKey: "profiles.cards.basic.label",
    descriptionKey: "profiles.cards.basic.description",
  },
  tools: {
    icon: WrenchIcon,
    labelKey: "profiles.cards.tools.label",
    descriptionKey: "profiles.cards.tools.description",
  },
  skills: {
    icon: LayersIcon,
    labelKey: "profiles.cards.skills.label",
    descriptionKey: "profiles.cards.skills.description",
  },
  mcp: {
    icon: ServerIcon,
    labelKey: "profiles.cards.mcp.label",
    descriptionKey: "profiles.cards.mcp.description",
  },
  prompts: {
    icon: FileTextIcon,
    labelKey: "profiles.cards.prompts.label",
    descriptionKey: "profiles.cards.prompts.description",
  },
  agents: {
    icon: BookUserIcon,
    labelKey: "profiles.cards.agents.label",
    descriptionKey: "profiles.cards.agents.description",
  },
  overrides: {
    icon: ShieldCheckIcon,
    labelKey: "profiles.cards.overrides.label",
    descriptionKey: "profiles.cards.overrides.description",
  },
  preferences: {
    icon: SlidersHorizontalIcon,
    labelKey: "profiles.cards.preferences.label",
    descriptionKey: "profiles.cards.preferences.description",
  },
  validation: {
    icon: ShieldCheckIcon,
    labelKey: "profiles.cards.validation.label",
    descriptionKey: "profiles.cards.validation.description",
  },
} as const satisfies Record<
  ProfileCardId,
  { descriptionKey: string; icon: LucideIcon; labelKey: string }
>;

export type ProfileEditorProps = {
  onClose: () => void;
  /** 保存成功后把最新 view 回传给列表（列表据此刷新该行，不整页重载）。 */
  onSaved: (view: RuntimeProfileView) => void;
  view: RuntimeProfileView;
};

export function ProfileEditor({ onClose, onSaved, view }: ProfileEditorProps) {
  const { t } = useTranslation("runtimeConfig");
  const [draft, setDraft] = useState<ProfileDraft>(() => createProfileDraft(view));
  const [activeCard, setActiveCard] = useState<ProfileCardId>("basic");
  const [isSaving, setIsSaving] = useState(false);
  const [isValidating, setIsValidating] = useState(false);
  const [isPreviewing, setIsPreviewing] = useState(false);
  const [report, setReport] = useState<RuntimeProfileValidationReport | null>(null);
  const [preview, setPreview] = useState<RuntimeProfilePreviewReport | null>(null);
  const [lastValidatedAt, setLastValidatedAt] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [statusMessage, setStatusMessage] = useState<string | null>(null);

  const validationOptions = emptyProfileValidationOptions;

  const issues = useMemo(
    () => validateProfileDraft(draft, validationOptions),
    [draft, validationOptions],
  );
  const changes = useMemo(() => diffProfileDraft(draft, view), [draft, view]);
  const isDirty = changes.length > 0;
  const busy = isSaving || isValidating || isPreviewing;

  /** 草稿改动统一入口：同时作废上一次的 validate / preview 结果。 */
  const applyDraft = useCallback((updater: (current: ProfileDraft) => ProfileDraft) => {
    setDraft((current) => updater(current));
    setReport(null);
    setPreview(null);
    setLastValidatedAt("");
    setStatusMessage(null);
  }, []);

  const update = useCallback(
    <K extends keyof ProfileDraft>(key: K, value: ProfileDraft[K]) => {
      applyDraft((current) => {
        const next: ProfileDraft = { ...current };
        next[key] = value;
        return next;
      });
    },
    [applyDraft],
  );

  const updateOverride = useCallback(
    (index: number, patch: Partial<ProfileDraftOverride>) => {
      applyDraft((current) => ({
        ...current,
        overrides: current.overrides.map((override, itemIndex) =>
          itemIndex === index ? { ...override, ...patch } : override,
        ),
      }));
    },
    [applyDraft],
  );

  const addOverride = useCallback(() => {
    applyDraft((current) => ({
      ...current,
      overrides: [...current.overrides, { key: "", value: "", allowed: true }],
    }));
  }, [applyDraft]);

  const removeOverride = useCallback(
    (index: number) => {
      applyDraft((current) => ({
        ...current,
        overrides: current.overrides.filter((_, itemIndex) => itemIndex !== index),
      }));
    },
    [applyDraft],
  );

  /** 本地校验 → 文档；失败时跳到第一处出错卡片并返回 null（不发请求）。 */
  const buildDocument = useCallback(() => {
    const built = buildProfileDocument(draft, view.document, validationOptions);
    if (built.ok) {
      return built.document;
    }
    setError(t("profiles.editor.issueHint"));
    focusFirstIssue(built.issues, setActiveCard);
    return null;
  }, [draft, t, validationOptions, view.document]);

  const validate = useCallback(async () => {
    const document = buildDocument();
    if (!document) {
      return;
    }
    setIsValidating(true);
    setError(null);
    try {
      const nextReport = await validateRuntimeProfile(view.ref, document);
      setReport(nextReport);
      setLastValidatedAt(new Date().toLocaleTimeString());
    } catch (validateError) {
      setError(formatProfileError(validateError, t("profiles.messages.validateFailed")));
    } finally {
      setIsValidating(false);
    }
  }, [buildDocument, t, view.ref]);

  const runPreview = useCallback(async () => {
    const document = buildDocument();
    if (!document) {
      return;
    }
    setIsPreviewing(true);
    setError(null);
    try {
      const nextPreview = await previewRuntimeProfile(view.ref, document);
      setPreview(nextPreview);
      setLastValidatedAt(new Date().toLocaleTimeString());
    } catch (previewError) {
      setError(formatProfileError(previewError, t("profiles.messages.previewFailed")));
    } finally {
      setIsPreviewing(false);
    }
  }, [buildDocument, t, view.ref]);

  const save = useCallback(async () => {
    const document = buildDocument();
    if (!document) {
      return;
    }
    setIsSaving(true);
    setError(null);
    setStatusMessage(null);
    try {
      // 写回必须带 mtime 基线（后端据此做冲突检测，缺失即不检查）。
      const result = await updateRuntimeProfile(view.ref, {
        document,
        expectedMtime: view.mtime,
      });
      const next = result.view;
      setDraft(createProfileDraft(next));
      setReport(null);
      setPreview(null);
      setLastValidatedAt("");
      const saved = t("profiles.editor.saveSucceeded", { name: next.name });
      // 写回成功但后端附 hint（如文件名与 name 不一致）时一并展示，不吞掉。
      setStatusMessage(result.hint ? `${saved} ${result.hint}` : saved);
      onSaved(next);
    } catch (saveError) {
      setError(
        isProfileWriteConflict(saveError)
          ? t("profiles.messages.conflictReload")
          : formatProfileError(saveError, t("profiles.editor.saveFailed")),
      );
    } finally {
      setIsSaving(false);
    }
    // view.mtime 必须进依赖：保存成功后父层会回填新 view，若闭包仍持旧基线，
    // 第二次保存会把「上一次读到的 mtime」当成当前基线，造成假冲突（409）。
  }, [buildDocument, onSaved, t, view.mtime, view.ref]);

  const reload = useCallback(async () => {
    setError(null);
    setStatusMessage(null);
    try {
      const next = await getRuntimeProfile(view.ref);
      setDraft(createProfileDraft(next));
      setReport(null);
      setPreview(null);
      setLastValidatedAt("");
      setStatusMessage(t("profiles.editor.reloaded", { name: next.name }));
    } catch (reloadError) {
      setError(formatProfileError(reloadError, t("profiles.messages.loadFailed")));
    }
  }, [t, view.ref]);

  const discard = useCallback(() => {
    applyDraft(() => createProfileDraft(view));
    setError(null);
  }, [applyDraft, view]);

  const ctx: ProfileCardContext = {
    addOverride,
    changes,
    disabled: busy,
    draft,
    issues,
    preview,
    removeOverride,
    update,
    updateOverride,
    view,
  };
  const validationState: ProfileValidationState = {
    isPreviewing,
    isValidating,
    lastValidatedAt,
    preview,
    report,
  };

  return (
    <div
      className="rounded-panel border border-border bg-surface-softer p-3"
      data-profile-editor={view.ref}
    >
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <span className="inline-flex size-6 items-center justify-center rounded-field border border-border bg-surface-solid text-accent-primary">
              <InfoIcon size={13} />
            </span>
            <div className="text-base font-semibold text-foreground">
              {t("profiles.editor.title", { name: draft.name || view.name })}
            </div>
            <Badge className={cn("normal-case", isDirty ? "text-accent-orange" : undefined)}>
              {isDirty
                ? t("profiles.editor.changeCount", { count: changes.length })
                : t("profiles.editor.noChanges")}
            </Badge>
          </div>
          <p className="mt-1.5 max-w-[46rem] text-xs leading-5 text-muted-foreground">
            {t("profiles.editor.description")}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button
            disabled={busy}
            size="sm"
            type="button"
            variant="secondary"
            onClick={() => {
              void reload();
            }}
          >
            <RefreshCcwIcon size={14} />
            {t("profiles.editor.reload")}
          </Button>
          <Button
            disabled={busy}
            size="sm"
            type="button"
            variant="ghost"
            onClick={onClose}
          >
            <XIcon size={14} />
            {t("profiles.editor.close")}
          </Button>
        </div>
      </div>

      {statusMessage ? (
        <SettingsNoticeCard className="mt-3" tone="neutral">
          <span role="status">{statusMessage}</span>
        </SettingsNoticeCard>
      ) : null}

      {error ? (
        <SettingsNoticeCard className="mt-3" tone="warning">
          <span role="alert">{error}</span>
        </SettingsNoticeCard>
      ) : null}

      {isDirty ? (
        <div className="mt-3 text-xs leading-5 text-accent-orange">
          {t("profiles.editor.dirtyHint")}
        </div>
      ) : null}

      <div className="mt-3 grid gap-3 lg:grid-cols-[208px_minmax(0,1fr)]">
        <nav className="flex min-w-0 flex-wrap gap-1.5 lg:flex-col lg:flex-nowrap" aria-label={t("profiles.editor.nav")}>
          {profileCardIds.map((card) => {
            const meta = PROFILE_CARD_META[card];
            const Icon = meta.icon;
            const issueCount = profileCardIssueCount(card, issues);
            const active = card === activeCard;
            return (
              <button
                key={card}
                type="button"
                aria-pressed={active}
                title={t(meta.descriptionKey)}
                onClick={() => {
                  setActiveCard(card);
                }}
                className={cn(
                  "flex min-w-0 items-center justify-between gap-2 rounded-card border px-2.5 py-2 text-left text-xs transition",
                  active
                    ? "border-accent-primary-border bg-accent-primary-soft text-foreground"
                    : "border-border bg-surface-solid text-muted-foreground hover:bg-surface-soft",
                )}
              >
                <span className="flex min-w-0 items-center gap-2">
                  <Icon size={13} />
                  <span className="truncate font-semibold">{t(meta.labelKey)}</span>
                </span>
                {issueCount > 0 ? (
                  <Badge className="normal-case text-accent-orange">{issueCount}</Badge>
                ) : null}
              </button>
            );
          })}
        </nav>

        <div className="min-w-0 rounded-card border border-border bg-surface-solid p-3">
          {renderProfileCard(activeCard, ctx, validationState, {
            onPreview: () => {
              void runPreview();
            },
            onValidate: () => {
              void validate();
            },
          })}
        </div>
      </div>

      <div className="mt-3 flex flex-wrap items-center justify-between gap-2 border-t border-border pt-3">
        <div className="text-xs leading-5 text-muted-foreground">
          {t("profiles.editor.issueCount", {
            errorCount: String(issues.length),
            warningCount: String(
              report?.issues.filter((issue) => issue.severity === "warning").length ?? 0,
            ),
          })}
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button
            disabled={busy || !isDirty}
            size="sm"
            type="button"
            variant="ghost"
            onClick={discard}
          >
            {t("profiles.editor.discard")}
          </Button>
          <Button
            disabled={busy}
            size="sm"
            type="button"
            onClick={() => {
              void save();
            }}
          >
            {isSaving ? t("profiles.editor.saving") : t("profiles.editor.save")}
          </Button>
        </div>
      </div>
    </div>
  );
}

/** 本地校验失败时跳到第一处出错卡片（issue 顺序即字段顺序，第一处最靠前）。 */
function focusFirstIssue(
  issues: ProfileDraftIssue[],
  setActiveCard: (card: ProfileCardId) => void,
) {
  const first = issues[0];
  if (first) {
    setActiveCard(profileCardForIssuePath(first.path));
  }
}

function renderProfileCard(
  card: ProfileCardId,
  ctx: ProfileCardContext,
  state: ProfileValidationState,
  actions: { onPreview: () => void; onValidate: () => void },
) {
  switch (card) {
    case "basic":
      return <ProfileBasicsCard ctx={ctx} />;
    case "tools":
      return <ProfileToolsCard ctx={ctx} />;
    case "skills":
      return <ProfileSkillsCard ctx={ctx} />;
    case "mcp":
      return <ProfileMcpCard ctx={ctx} />;
    case "prompts":
      return <ProfilePromptsCard ctx={ctx} />;
    case "agents":
      return <ProfileAgentsCard ctx={ctx} />;
    case "overrides":
      return <ProfileOverridesCard ctx={ctx} />;
    case "preferences":
      return <ProfilePreferencesCard ctx={ctx} />;
    case "validation":
      return (
        <ProfileValidationCard
          ctx={ctx}
          onPreview={actions.onPreview}
          onValidate={actions.onValidate}
          state={state}
        />
      );
  }
}
