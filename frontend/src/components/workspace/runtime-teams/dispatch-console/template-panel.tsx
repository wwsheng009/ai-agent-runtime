// 由 components/workspace/runtime-teams/dispatch-console.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { cn } from "@/lib/utils";
import { useTranslation } from "react-i18next";

import { consoleModeButtonClass, consolePanelClass } from "./console-styles";
import type { DispatchConsoleProps } from "./types";

type DispatchTemplatePanelProps = Pick<
  DispatchConsoleProps,
  | "dispatchTemplateMode"
  | "onDispatchTemplateModeChange"
>;

export function DispatchTemplatePanel({
  dispatchTemplateMode,
  onDispatchTemplateModeChange,
}: DispatchTemplatePanelProps) {
  const { t } = useTranslation("workspace");

  return (
    <div className={cn("mt-3", consolePanelClass)}>
      <div className="app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
        {t("panels.teamsDispatch.template.heading")}
      </div>
      <div className="mt-3 flex flex-wrap gap-2">
        <button
          type="button"
          onClick={() => onDispatchTemplateModeChange("review_implement_verify")}
          className={cn(
            consoleModeButtonClass,
            dispatchTemplateMode === "review_implement_verify"
              ? "border-accent-gold/24 bg-accent-gold/10 text-accent-gold"
              : "border-white/10 bg-white/4 text-muted-foreground hover:border-white/14 hover:bg-white/7 hover:text-foreground",
          )}
        >
          {t("panels.teamsDispatch.template.reviewImplementVerify")}
        </button>
        <button
          type="button"
          onClick={() => onDispatchTemplateModeChange("mirror")}
          className={cn(
            consoleModeButtonClass,
            dispatchTemplateMode === "mirror"
              ? "border-accent-gold/24 bg-accent-gold/10 text-accent-gold"
              : "border-white/10 bg-white/4 text-muted-foreground hover:border-white/14 hover:bg-white/7 hover:text-foreground",
          )}
        >
          {t("panels.teamsDispatch.template.mirrorSameTask")}
        </button>
      </div>
      <div className="mt-3 text-sm leading-6 text-muted-foreground">
        {dispatchTemplateMode === "mirror"
          ? t("panels.teamsDispatch.template.mirrorDescription")
          : t("panels.teamsDispatch.template.roleVariantsDescription")}
      </div>
    </div>
  );
}
