// 会话模式常驻标识（§4.6 Web 侧）：聊天区顶部常显当前权限模式，plan 模式下补计划上下文。
//
// 契约：只消费页面既有的 `/plan` 快照（与右侧「计划」面板同源、不新增取数），
// 不承载任何裁决动作——裁决入口仍是 composer 上沿的 pending bar 与右侧计划面板，
// 避免出现第二套 pending 判定（P1-7 的口径）。

import { ScrollTextIcon, ShieldAlertIcon, ShieldIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import {
  sessionModeBannerHintLeaf,
  sessionModeBannerLabelKey,
  sessionModeBannerTone,
  sessionModeBannerToneClass,
} from "@/components/workspace/session-mode-banner-shared";
import type { RuntimeSessionPlanMode } from "@/lib/runtime-api";
import { cn } from "@/lib/utils";

type SessionModeBannerProps = {
  className?: string;
  plan: RuntimeSessionPlanMode | null;
  planStatusLabel?: string;
  sessionId?: string;
};

export function SessionModeBanner({
  className,
  plan,
  planStatusLabel,
  sessionId,
}: SessionModeBannerProps) {
  const { t } = useTranslation("workspace");
  const mode = plan?.permission_mode?.trim() ?? "";

  // 无会话 / 快照未到位：不占位（模式控件仍在下一条输入卡里）。
  if (!sessionId || !mode) {
    return null;
  }

  const tone = sessionModeBannerTone(mode);
  const labelKey = sessionModeBannerLabelKey(mode);
  const modeLabel = labelKey ? (t(labelKey as never) as string) : mode;
  const hintLeaf = sessionModeBannerHintLeaf(plan);
  const planActive = Boolean(plan?.active);
  const TitleIcon = tone === "danger" ? ShieldAlertIcon : tone === "plan" ? ScrollTextIcon : ShieldIcon;

  return (
    <div
      className={cn(
        "mb-2 flex flex-wrap items-center gap-x-2 gap-y-1 px-0.5 app-text-10 tracking-[0.08em] text-muted-foreground",
        className,
      )}
      data-mode={mode}
      data-testid="session-mode-banner"
      data-tone={tone}
    >
      <TitleIcon aria-hidden="true" className="size-3.5 shrink-0" />
      <span className="uppercase">{t("composer.modeBanner.title")}</span>
      <Badge className={sessionModeBannerToneClass(tone)}>{modeLabel}</Badge>
      {planActive && planStatusLabel ? <Badge>{planStatusLabel}</Badge> : null}
      {planActive && plan?.plan_path ? (
        <span className="truncate" title={plan.plan_path}>
          {plan.plan_path}
        </span>
      ) : null}
      {hintLeaf ? (
        <span data-testid="session-mode-hint">
          {t(`composer.modeBanner.hint.${hintLeaf}` as never) as string}
        </span>
      ) : null}
    </div>
  );
}
