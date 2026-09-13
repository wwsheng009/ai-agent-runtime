import { LoaderCircleIcon, PlayIcon, RouteIcon } from "lucide-react";
import { type TFunction } from "i18next";

import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/select";
import { type RuntimeAgentRoutePreviewResult } from "@/types/runtime";

import { ConfigFormField } from "../config-form-field";
import { editorControlClassName } from "../editor-control-class";
import { type AgentRoutingDifficulty } from "../runtime-agent-routing-domain-utils";
import { SettingsNoticeCard } from "../settings-notice-card";

import { difficultyOptions } from "./format";
import { RoutePreviewResult } from "./route-preview-result";

export function RoutingPreviewSection({
  isPreviewing,
  previewDifficulty,
  previewError,
  previewGoal,
  previewResult,
  previewRole,
  runRoutePreview,
  setPreviewDifficulty,
  setPreviewGoal,
  setPreviewRole,
  t,
}: {
  isPreviewing: boolean;
  previewDifficulty: AgentRoutingDifficulty;
  previewError: string | null;
  previewGoal: string;
  previewResult: RuntimeAgentRoutePreviewResult | null;
  previewRole: string;
  runRoutePreview: () => Promise<void>;
  setPreviewDifficulty: (difficulty: AgentRoutingDifficulty) => void;
  setPreviewGoal: (goal: string) => void;
  setPreviewRole: (role: string) => void;
  t: TFunction<"runtimeConfig">;
}) {
  return (
    <>
      <div className="border-t border-border pt-3">
        <div className="flex items-center gap-2 font-semibold text-foreground">
          <RouteIcon size={16} className="text-accent-primary" />
          {t("editor.agentRouting.preview.title")}
        </div>
        <div className="mt-3 grid gap-3 md:grid-cols-2 xl:grid-cols-[10rem_minmax(10rem,0.8fr)_minmax(16rem,1.4fr)_auto] xl:items-end">
          <ConfigFormField label={t("editor.agentRouting.preview.difficulty")}>
            <Select
              ariaLabel={t("editor.agentRouting.preview.difficulty")}
              options={difficultyOptions(t)}
              value={previewDifficulty}
              onChange={(value) =>
                setPreviewDifficulty(value as AgentRoutingDifficulty)
              }
            />
          </ConfigFormField>
          <ConfigFormField label={t("editor.agentRouting.preview.role")}>
            <input
              className={editorControlClassName}
              placeholder={t("editor.agentRouting.preview.rolePlaceholder")}
              value={previewRole}
              onChange={(event) => setPreviewRole(event.target.value)}
            />
          </ConfigFormField>
          <ConfigFormField label={t("editor.agentRouting.preview.goal")}>
            <input
              className={editorControlClassName}
              placeholder={t("editor.agentRouting.preview.goalPlaceholder")}
              value={previewGoal}
              onChange={(event) => setPreviewGoal(event.target.value)}
            />
          </ConfigFormField>
          <Button
            className="w-full xl:w-auto"
            disabled={isPreviewing}
            onClick={() => void runRoutePreview()}
          >
            {isPreviewing ? (
              <LoaderCircleIcon size={15} className="animate-spin" />
            ) : (
              <PlayIcon size={15} />
            )}
            {t(
              isPreviewing
                ? "editor.agentRouting.preview.running"
                : "editor.agentRouting.preview.run",
            )}
          </Button>
        </div>

        {previewError ? (
          <div className="mt-3">
            <SettingsNoticeCard tone="warning-soft">
              {previewError}
            </SettingsNoticeCard>
          </div>
        ) : null}
        {previewResult ? (
          <RoutePreviewResult result={previewResult} t={t} />
        ) : null}
      </div>
    </>
  );
}
