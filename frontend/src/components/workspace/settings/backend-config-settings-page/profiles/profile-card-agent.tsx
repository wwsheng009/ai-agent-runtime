// 第 5、6 张卡片：提示词 / Agent。
//
// 契约（backend/internal/api/skills/profiles_view_groups.go）：
//   * prompts：只有 mode（replace/append）可写；system/role/tools 的 path+exists
//     由后端解析（相对 profile 文件目录），UI 只读展示；
//   * agents：default_agent 是 profile 层可写字段（在基础信息卡编辑），本卡只读
//     展示 available 与 entries（id/provider/model + 各自工具面）。
//
// 「影响面」一律复用编辑器算出的变更路径（changes），不自己拼 diff——
// /preview 返回的是解析后的视图，不是 diff 文本。

import { BookUserIcon, FileTextIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";

import type { ProfileCardContext } from "./profile-editor-context";
import {
  ProfileCardHeader,
  ProfileField,
  ProfileReadonlyChips,
  ProfileReadonlyText,
  ProfileSelectField,
} from "./profile-form-fields";
import { PROFILE_PROMPT_MODE_KEY } from "./profile-i18n";
import { profilePromptModes, type ProfilePromptMode } from "./profile-draft";

export function ProfilePromptsCard({ ctx }: { ctx: ProfileCardContext }) {
  const { t } = useTranslation("runtimeConfig");
  const { changes, disabled, draft, update, view } = ctx;
  const promptChanges = changes.filter((path) => path.startsWith("prompts."));
  const layers = [
    { key: "system", label: t("profiles.prompts.systemPath"), layer: view.prompts.system },
    { key: "role", label: t("profiles.prompts.rolePath"), layer: view.prompts.role },
    { key: "tools", label: t("profiles.prompts.toolsPath"), layer: view.prompts.tools },
  ];

  return (
    <div className="grid gap-3">
      <ProfileCardHeader
        description={t("profiles.prompts.description")}
        icon={<FileTextIcon size={13} />}
        title={t("profiles.prompts.title")}
      />
      <div className="grid gap-2 sm:grid-cols-2">
        {layers.map(({ key, label, layer }) => (
          <ProfileReadonlyText
            key={key}
            label={label}
            value={layer.path ? `${layer.path}${layer.exists ? "" : " (missing)"}` : "-"}
          />
        ))}
        <ProfileSelectField
          description={t("profiles.prompts.modeHint")}
          disabled={disabled}
          label={t("profiles.prompts.mode")}
          options={profilePromptModes.map((mode: ProfilePromptMode) => ({
            value: mode,
            label: t(PROFILE_PROMPT_MODE_KEY[mode]),
          }))}
          value={draft.promptMode}
          onChange={(value) => {
            update("promptMode", value === "replace" ? "replace" : "append");
          }}
        />
      </div>
      <ProfileField
        description={t("profiles.prompts.previewHint")}
        label={t("profiles.prompts.preview")}
      >
        {promptChanges.length > 0 ? (
          <div className="flex flex-wrap gap-1.5">
            {promptChanges.map((path) => (
              <Badge key={path} className="normal-case">
                {path}
              </Badge>
            ))}
          </div>
        ) : (
          <div className="text-xs leading-5 text-muted-foreground">
            {t("profiles.prompts.empty")}
          </div>
        )}
      </ProfileField>
    </div>
  );
}

export function ProfileAgentsCard({ ctx }: { ctx: ProfileCardContext }) {
  const { t } = useTranslation("runtimeConfig");
  const { view } = ctx;
  const agents = view.agents;
  const isEmpty = agents.available.length === 0 && agents.entries.length === 0;

  return (
    <div className="grid gap-3">
      <ProfileCardHeader
        description={t("profiles.agents.description")}
        icon={<BookUserIcon size={13} />}
        title={t("profiles.agents.title")}
      />
      <ProfileReadonlyText
        label={t("profiles.agents.defaultAgent")}
        value={agents.defaultAgent || t("profiles.editor.inheritedHint")}
      />
      <ProfileReadonlyChips
        description={t("profiles.agents.availableHint")}
        emptyLabel={t("profiles.agents.empty")}
        label={t("profiles.agents.available")}
        toneClassName="text-accent-primary"
        values={agents.available}
      />
      {agents.entries.length > 0 ? (
        <ProfileField label={t("profiles.cards.agents.label")}>
          <div className="grid gap-1.5" data-profile-agent-entries={agents.entries.length}>
            {agents.entries.map((entry) => (
              <div
                key={entry.id}
                className="flex flex-wrap items-center gap-2 rounded-card border border-border bg-surface-softer px-2 py-1.5 text-xs leading-5"
              >
                <span className="font-mono text-foreground">{entry.id}</span>
                {entry.provider ? (
                  <Badge className="normal-case">{entry.provider}</Badge>
                ) : null}
                {entry.model ? <Badge className="normal-case">{entry.model}</Badge> : null}
                <span className="text-muted-foreground">
                  {t("profiles.tools.allow")}: {entry.tools.allowlist.length} ·{" "}
                  {t("profiles.tools.deny")}: {entry.tools.denylist.length}
                </span>
              </div>
            ))}
          </div>
        </ProfileField>
      ) : null}
      {isEmpty ? (
        <div className="text-xs leading-5 text-muted-foreground">{t("profiles.agents.empty")}</div>
      ) : null}
    </div>
  );
}
