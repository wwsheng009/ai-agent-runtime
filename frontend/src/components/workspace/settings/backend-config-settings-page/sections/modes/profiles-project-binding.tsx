// FR-14 项目绑定卡片（只读发现）。
//
// 纪律：
//   * 只读：这里**不提供**切换/设默认按钮——设置页没有会话上下文，而 apply 必须
//     显式给出 session_id（服务端不推断「当前会话」，A12），所以文案把用户指向
//     会话内的 `/profile <ref>`，而不是给一个点了必然报错的按钮；
//   * 不把绑定说成 default：default 只由「设为默认」维护，绑定只是候选指针；
//   * 无绑定（present=false）不渲染：由调用方决定挂载，本组件不制造"没有绑定"的噪音；
//   * 绑定不可用（valid=false）如实展示错误，不隐藏、也不降级成"没有绑定"。

import { LinkIcon, ShieldAlertIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";
import type { RuntimeProfileProjectBinding } from "@/types/runtime";

import { SettingsNoticeCard } from "../../../settings-notice-card";
import { profileLayerLabel } from "../../profiles/profile-i18n";

export type ProfilesProjectBindingCardProps = {
  binding: RuntimeProfileProjectBinding;
};

export function ProfilesProjectBindingCard({ binding }: ProfilesProjectBindingCardProps) {
  const { t } = useTranslation("runtimeConfig");

  return (
    <SettingsNoticeCard className="mt-0" tone={binding.valid ? "muted" : "warning-soft"}>
      <div className="space-y-1" data-testid="profiles-project-binding">
        <div className="flex flex-wrap items-center gap-2">
          <LinkIcon className="shrink-0 text-muted-foreground" size={14} />
          <span className="font-semibold">{t("profiles.projectBinding.title")}</span>
          <Badge
            className={cn(
              "normal-case",
              binding.valid ? "text-accent-primary" : "text-accent-orange",
            )}
          >
            {binding.valid
              ? t("profiles.projectBinding.statusValid")
              : t("profiles.projectBinding.statusInvalid")}
          </Badge>
          <Badge className="normal-case">{profileLayerLabel(t, binding.layer)}</Badge>
        </div>
        <p className="text-xs leading-5 text-muted-foreground">
          {t("profiles.projectBinding.description")}
        </p>
        <p className="break-all font-mono text-xs leading-5 text-muted-foreground">
          {t("profiles.projectBinding.refLabel")}: {binding.ref || "-"}
        </p>
        <p className="break-all font-mono text-xs leading-5 text-muted-foreground">
          {t("profiles.projectBinding.workspaceLabel")}: {binding.workspacePath || "-"}
        </p>
        <p className="break-all font-mono text-xs leading-5 text-muted-foreground">
          {t("profiles.projectBinding.pointerLabel")}: {binding.path || "-"}
        </p>
        <p className="break-all font-mono text-xs leading-5 text-muted-foreground">
          {t("profiles.projectBinding.targetLabel")}: {binding.profileRoot || "-"}
        </p>
        {!binding.valid && binding.error ? (
          <p
            className="text-xs leading-5 text-accent-orange"
            data-testid="profiles-project-binding-error"
            role="alert"
          >
            {binding.error}
          </p>
        ) : null}
        {binding.promptSuppressed ? (
          <p
            className="flex items-start gap-1.5 text-xs leading-5 text-accent-orange"
            data-testid="profiles-project-binding-suppressed"
            title={binding.promptSuppressionReason || undefined}
          >
            <ShieldAlertIcon className="mt-0.5 shrink-0" size={13} />
            <span>{t("profiles.projectBinding.suppressed")}</span>
          </p>
        ) : null}
        {binding.valid && binding.ref ? (
          <p
            className="text-xs leading-5 text-muted-foreground"
            data-testid="profiles-project-binding-apply-hint"
          >
            {t("profiles.projectBinding.applyHint", { ref: binding.ref })}
          </p>
        ) : null}
      </div>
    </SettingsNoticeCard>
  );
}
