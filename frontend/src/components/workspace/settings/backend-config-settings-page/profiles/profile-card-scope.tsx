// 第 2、3、4 张卡片：工具策略 / 技能 / MCP。
//
// 契约（backend/internal/api/skills/profiles_view_groups.go）：
//   * 可写面只有 allowlist / denylist（tools、skills）与 use_servers / exclude_servers（mcp）；
//   * 只读面（effective/excluded/sources、技能 dirs/exposure_mode/top_k、server 选择结果）
//     来自后端 resolved view，前端不得把它们混回草稿——否则「全局配置」会被误写进 profile。

import { LayersIcon, ServerIcon, WrenchIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import type { ProfileCardContext } from "./profile-editor-context";
import {
  ProfileCardHeader,
  ProfileReadonlyChips,
  ProfileReadonlyText,
  ProfileTextAreaField,
} from "./profile-form-fields";
import { profileOriginLabel } from "./profile-i18n";

export function ProfileToolsCard({ ctx }: { ctx: ProfileCardContext }) {
  const { t } = useTranslation("runtimeConfig");
  const { disabled, draft, update, view } = ctx;

  return (
    <div className="grid gap-3">
      <ProfileCardHeader
        description={t("profiles.tools.description")}
        icon={<WrenchIcon size={13} />}
        title={t("profiles.tools.title")}
      />
      <div className="grid gap-2 sm:grid-cols-2">
        <ProfileTextAreaField
          description={t("profiles.tools.allowHint")}
          disabled={disabled}
          label={t("profiles.tools.allow")}
          value={draft.toolsAllowlist}
          onChange={(value) => {
            update("toolsAllowlist", value);
          }}
        />
        <ProfileTextAreaField
          description={t("profiles.tools.denyHint")}
          disabled={disabled}
          label={t("profiles.tools.deny")}
          value={draft.toolsDenylist}
          onChange={(value) => {
            update("toolsDenylist", value);
          }}
        />
      </div>
      <div className="grid gap-2 sm:grid-cols-2">
        <ProfileReadonlyChips
          description={t("profiles.tools.effectiveHint")}
          emptyLabel={t("profiles.tools.empty")}
          label={t("profiles.tools.effective")}
          toneClassName="text-accent-primary"
          values={view.tools.effective}
        />
        <ProfileReadonlyChips
          description={t("profiles.tools.excludedHint")}
          emptyLabel={t("profiles.tools.empty")}
          label={t("profiles.tools.excluded")}
          toneClassName="text-accent-orange"
          values={view.tools.excluded}
        />
      </div>
      <div className="grid gap-2 sm:grid-cols-2">
        <ProfileReadonlyText
          description={t("profiles.tools.effectiveHint")}
          label={t("profiles.cards.agents.label")}
          value={view.tools.agentId}
        />
        <ProfileReadonlyChips
          description={t("profiles.tools.effectiveHint")}
          emptyLabel={t("profiles.tools.empty")}
          label={t("profiles.tools.origins")}
          values={view.tools.sources.map((source) => profileOriginLabel(t, source))}
        />
      </div>
    </div>
  );
}

export function ProfileSkillsCard({ ctx }: { ctx: ProfileCardContext }) {
  const { t } = useTranslation("runtimeConfig");
  const { disabled, draft, update, view } = ctx;
  const hasAnyConfig =
    draft.skillsAllowlist.trim() !== "" || draft.skillsDenylist.trim() !== "";

  return (
    <div className="grid gap-3">
      <ProfileCardHeader
        description={t("profiles.skills.description")}
        icon={<LayersIcon size={13} />}
        title={t("profiles.skills.title")}
      />
      <div className="grid gap-2 sm:grid-cols-2">
        <ProfileTextAreaField
          description={t("profiles.skills.allowHint")}
          disabled={disabled}
          label={t("profiles.skills.allow")}
          rows={3}
          value={draft.skillsAllowlist}
          onChange={(value) => {
            update("skillsAllowlist", value);
          }}
        />
        <ProfileTextAreaField
          description={t("profiles.skills.denyHint")}
          disabled={disabled}
          label={t("profiles.skills.deny")}
          rows={3}
          value={draft.skillsDenylist}
          onChange={(value) => {
            update("skillsDenylist", value);
          }}
        />
      </div>
      <ProfileReadonlyChips
        emptyLabel={t("profiles.skills.empty")}
        label={t("profiles.skills.dirs")}
        values={view.skills.dirs}
      />
      <div className="grid gap-2 sm:grid-cols-2">
        <ProfileReadonlyText
          description={t("profiles.editor.inheritedHint")}
          label={t("profiles.skills.exposureMode")}
          value={view.skills.exposureMode}
        />
        <ProfileReadonlyText
          description={t("profiles.skills.topKHint")}
          label={t("profiles.skills.topK")}
          value={view.skills.topK > 0 ? String(view.skills.topK) : "-"}
        />
      </div>
      {hasAnyConfig ? null : (
        <div className="text-xs leading-5 text-muted-foreground">
          {t("profiles.skills.empty")}
        </div>
      )}
    </div>
  );
}

export function ProfileMcpCard({ ctx }: { ctx: ProfileCardContext }) {
  const { t } = useTranslation("runtimeConfig");
  const { disabled, draft, update, view } = ctx;
  const usedServers = view.mcp.servers.filter((server) => server.used).map((server) => server.name);
  const excludedServers = view.mcp.servers
    .filter((server) => server.excluded)
    .map((server) => server.name);
  const isEmpty =
    draft.mcpUseServers.trim() === "" &&
    draft.mcpExcludeServers.trim() === "" &&
    view.mcp.serverCount === 0;

  return (
    <div className="grid gap-3">
      <ProfileCardHeader
        description={t("profiles.mcp.description")}
        icon={<ServerIcon size={13} />}
        title={t("profiles.mcp.title")}
      />
      <div className="grid gap-2 sm:grid-cols-2">
        <ProfileTextAreaField
          description={t("profiles.mcp.useHint")}
          disabled={disabled}
          label={t("profiles.mcp.use")}
          rows={3}
          value={draft.mcpUseServers}
          onChange={(value) => {
            update("mcpUseServers", value);
          }}
        />
        <ProfileTextAreaField
          description={t("profiles.mcp.excludeHint")}
          disabled={disabled}
          label={t("profiles.mcp.exclude")}
          rows={3}
          value={draft.mcpExcludeServers}
          onChange={(value) => {
            update("mcpExcludeServers", value);
          }}
        />
      </div>
      <div className="grid gap-2 sm:grid-cols-2">
        <ProfileReadonlyChips
          description={t("profiles.mcp.connectedHint")}
          emptyLabel={t("profiles.mcp.empty")}
          label={t("profiles.mcp.connected")}
          toneClassName="text-accent-primary"
          values={usedServers}
        />
        <ProfileReadonlyChips
          description={t("profiles.mcp.excludeHint")}
          emptyLabel={t("profiles.mcp.empty")}
          label={t("profiles.mcp.exclude")}
          toneClassName="text-accent-orange"
          values={excludedServers}
        />
      </div>
      <ProfileReadonlyText
        description={t("profiles.mcp.connectedHint")}
        label={t("profiles.mcp.toolCount")}
        value={`${view.mcp.usedCount} / ${view.mcp.serverCount}`}
      />
      {isEmpty ? (
        <div className="text-xs leading-5 text-muted-foreground">
          {t("profiles.mcp.empty")}
        </div>
      ) : null}
    </div>
  );
}
