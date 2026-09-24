// 第 1、8 张卡片：基础信息（可写身份字段）与会话偏好（后端 resolved 只读）。
//
// 依据（backend/internal/profile/spec.go + api/skills/profiles_view_groups.go）：
//   * profile.yaml 可写身份字段只有 profile.name / profile.description /
//     profile.default_agent；
//   * 会话偏好（权限模式 / provider / model）由 agentdef 权威解析后下发（只读），
//     profile 层不写这些键，唯一的硬约束是 D16：不得为 bypass_permissions。
//     因此偏好卡只展示生效值与来源，避免 UI 写出后端不认的字段。

import { InfoIcon, SlidersHorizontalIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

import type { ProfileCardContext } from "./profile-editor-context";
import {
  ProfileCardHeader,
  ProfileField,
  ProfileReadonlyText,
  ProfileTextField,
} from "./profile-form-fields";
import { profileLayerLabel } from "./profile-i18n";
import { forbiddenProfilePermissionMode } from "./profile-draft";

export function ProfileBasicsCard({ ctx }: { ctx: ProfileCardContext }) {
  const { t } = useTranslation("runtimeConfig");
  const { disabled, draft, update, view } = ctx;
  const statusLabel = view.valid ? t("profiles.list.statusOk") : t("profiles.list.statusError");
  const statusDetail = view.issues.find((issue) => issue.severity === "error")?.message ?? "";

  return (
    <div className="grid gap-3">
      <ProfileCardHeader
        description={t("profiles.basic.description")}
        icon={<InfoIcon size={13} />}
        title={t("profiles.basic.title")}
      />
      <div className="grid gap-2 sm:grid-cols-2">
        <ProfileTextField
          description={t("profiles.basic.nameHint")}
          disabled={disabled}
          label={t("profiles.basic.name")}
          onChange={(value) => {
            update("name", value);
          }}
          value={draft.name}
        />
        <ProfileTextField
          description={t("profiles.basic.descriptionHint")}
          disabled={disabled}
          label={t("profiles.basic.descriptionLabel")}
          onChange={(value) => {
            update("description", value);
          }}
          value={draft.description}
        />
      </div>
      <div className="grid gap-2 sm:grid-cols-2">
        <ProfileReadonlyText label={t("profiles.basic.ref")} value={view.ref} />
        <ProfileReadonlyText label={t("profiles.basic.path")} value={view.path} />
        <ProfileField label={t("profiles.basic.layer")}>
          <Badge className="normal-case">{profileLayerLabel(t, view.layer)}</Badge>
        </ProfileField>
        <ProfileField label={t("profiles.basic.status")}>
          <div className="flex flex-wrap items-center gap-2">
            <Badge
              className={cn(
                "normal-case",
                view.valid ? "text-accent-primary" : "text-accent-orange",
              )}
            >
              {statusLabel}
            </Badge>
          </div>
          {statusDetail ? (
            <div className="mt-1 break-all text-xs leading-5 text-muted-foreground">
              {statusDetail}
            </div>
          ) : null}
        </ProfileField>
      </div>
      <ProfileTextField
        description={t("profiles.basic.defaultAgentHint")}
        disabled={disabled}
        label={t("profiles.basic.defaultAgent")}
        onChange={(value) => {
          update("defaultAgent", value);
        }}
        value={draft.defaultAgent}
      />
    </div>
  );
}

export function ProfilePreferencesCard({ ctx }: { ctx: ProfileCardContext }) {
  const { t } = useTranslation("runtimeConfig");
  const { view } = ctx;
  const preferences = view.preferences;
  const permissionForbidden =
    preferences.permissionMode === forbiddenProfilePermissionMode;

  return (
    <div className="grid gap-3">
      <ProfileCardHeader
        description={t("profiles.preferences.description")}
        icon={<SlidersHorizontalIcon size={13} />}
        title={t("profiles.preferences.title")}
      />
      <div className="grid gap-2 sm:grid-cols-2">
        <ProfileReadonlyText
          label={t("profiles.preferences.permissionMode")}
          value={preferences.permissionMode || t("profiles.preferences.permissionModeInherit")}
        />
        <ProfileReadonlyText
          label={t("profiles.overrides.origin")}
          value={preferences.permissionModeSource || "-"}
        />
        <ProfileReadonlyText
          label={t("profiles.preferences.provider")}
          value={preferences.provider || "-"}
        />
        <ProfileReadonlyText
          label={t("profiles.preferences.model")}
          value={preferences.model || "-"}
        />
      </div>
      <div className="text-xs leading-5 text-muted-foreground">
        {t("profiles.preferences.permissionModeHint")}
      </div>
      {permissionForbidden ? (
        <div className="text-xs leading-5 text-accent-orange" role="alert">
          {t("profiles.preferences.permissionModeForbidden")}
        </div>
      ) : null}
    </div>
  );
}
