// D29 工作区未信任提示 + 一键信任入口（Batch 14 slice 5 / Q22）。
//
// 纪律：
//   * 信任是权限提升动作：必须两步（点击 → 显式确认）才发请求，不做"点了就信任"；
//   * 只处理 grant：撤销信任会让项目级配置整体失效，走 CLI `/trust` 面，UI 不提供；
//   * 未信任只扣留 prompts（分级门控）：文案必须说清"收窄类声明仍生效"，
//     否则用户会误以为整个 profile 失效而重复排查；
//   * 重载语义：信任翻转只对**重新解析**生效——本页刷新列表即可去掉徽标，
//     已在跑的会话仍需 `/profile reload`（文案里显式给出恢复路径）。

import { ShieldAlertIcon } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";

import { SettingsNoticeCard } from "../../../settings-notice-card";

export type ProfilesTrustNoticeProps = {
  /** 本次列表响应回显的工作区路径（空串表示未声明工作区，调用方不渲染本卡片）。 */
  workspacePath: string;
  /** 授予信任请求进行中：两个按钮一起禁用，避免重复提交。 */
  granting: boolean;
  /** 宿主负责调 API + 刷新列表；本组件只负责"显式确认"这一步。 */
  onGrant: () => void;
};

export function ProfilesTrustNotice({
  workspacePath,
  granting,
  onGrant,
}: ProfilesTrustNoticeProps) {
  const { t } = useTranslation("runtimeConfig");
  const [confirming, setConfirming] = useState(false);

  return (
    <SettingsNoticeCard className="mt-0" tone="warning-soft">
      <div className="flex flex-wrap items-start gap-2" data-testid="profiles-trust-notice">
        <ShieldAlertIcon size={16} className="mt-0.5 shrink-0 text-accent-orange" />
        <div className="min-w-0 flex-1 space-y-1">
          <div className="font-semibold">{t("profiles.trust.title")}</div>
          <p className="text-xs leading-5 text-muted-foreground">
            {t("profiles.trust.description")}
          </p>
          <p className="app-inline-mono break-all text-xs leading-5 text-muted-foreground">
            {t("profiles.trust.workspace", { path: workspacePath })}
          </p>
          {confirming ? (
            <p className="text-xs leading-5 text-accent-orange">
              {t("profiles.trust.confirmHint")}
            </p>
          ) : null}
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {confirming ? (
            <>
              <Button
                data-testid="profiles-trust-confirm"
                disabled={granting}
                size="sm"
                variant="primary"
                onClick={() => {
                  onGrant();
                }}
              >
                {granting ? t("profiles.trust.granting") : t("profiles.trust.confirm")}
              </Button>
              <Button
                data-testid="profiles-trust-cancel"
                disabled={granting}
                size="sm"
                variant="secondary"
                onClick={() => {
                  setConfirming(false);
                }}
              >
                {t("profiles.trust.cancel")}
              </Button>
            </>
          ) : (
            <Button
              data-testid="profiles-trust-grant"
              disabled={granting}
              size="sm"
              variant="secondary"
              onClick={() => {
                setConfirming(true);
              }}
            >
              {t("profiles.trust.action")}
            </Button>
          )}
        </div>
      </div>
    </SettingsNoticeCard>
  );
}
