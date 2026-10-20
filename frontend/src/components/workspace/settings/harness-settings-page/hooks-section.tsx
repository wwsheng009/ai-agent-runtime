import { type TFunction } from "i18next";
import { WebhookIcon } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  type RuntimeHarnessHook,
  type RuntimeHarnessHookRun,
} from "@/lib/runtime-api";

import { SettingsEmptyState } from "../settings-empty-state";
import { SettingsPanelCard } from "../settings-panel-card";
import { SettingsSection } from "../settings-section";

export function HarnessHooksSection({
  actionPending,
  hooks,
  runs,
  sources,
  updateHook,
  warnings,
  t,
}: {
  actionPending: boolean;
  hooks: RuntimeHarnessHook[];
  runs: RuntimeHarnessHookRun[];
  sources: string[];
  updateHook: (hook: RuntimeHarnessHook, enabled: boolean) => Promise<void>;
  warnings: string[];
  t: TFunction<"settings">;
}) {
  return (
    <SettingsSection
      title={t("harness.hooksTitle")}
      description={t("harness.hooksDescription")}
    >
      {(warnings.length > 0 || sources.length > 0) && (
        <div className="mb-3 space-y-1 text-xs text-muted-foreground">
          {sources.length > 0 ? (
            <div>
              {t("harness.hookSources", { count: sources.length })}
            </div>
          ) : null}
          {warnings.map((warning) => (
            <div key={warning} className="break-all">
              {warning}
            </div>
          ))}
        </div>
      )}
      {hooks.length > 0 ? (
        <div className="space-y-2">
          {hooks.map((hook) => (
            <SettingsPanelCard
              key={hook.id}
              title={hook.event}
              icon={<WebhookIcon size={16} className="text-accent-secondary" />}
              description={hook.matcher || t("harness.hookNoMatcher")}
              headerAside={
                <div className="flex flex-wrap gap-2">
                  <Badge>{hook.handler_type}</Badge>
                  <Badge>{hook.execution_mode}</Badge>
                  <Badge>
                    {hook.enabled
                      ? t("harness.hookEnabled")
                      : t("harness.hookDisabled")}
                  </Badge>
                </div>
              }
            >
              <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                <span className="app-inline-mono break-all">{hook.id}</span>
                {hook.source ? <span>{hook.source}</span> : null}
                {hook.timeout_seconds ? (
                  <span>
                    {t("harness.hookTimeout", {
                      seconds: String(hook.timeout_seconds),
                    })}
                  </span>
                ) : null}
              </div>
              {hook.command || hook.url ? (
                <pre className="mt-2 overflow-x-auto whitespace-pre-wrap break-all rounded bg-muted/40 p-2 text-xs">
                  {hook.command || hook.url}
                </pre>
              ) : null}
              {hook.status_message ? (
                <div className="mt-2 text-xs text-muted-foreground">
                  {hook.status_message}
                </div>
              ) : null}
              {hook.source_path ? (
                <div className="mt-2 break-all text-xs text-muted-foreground">
                  {hook.source_path}
                </div>
              ) : null}
              {hook.source_path &&
              (hook.source === "user" || hook.source === "project") ? (
                <div className="mt-3">
                  <Button
                    variant="secondary"
                    size="sm"
                    disabled={actionPending}
                    onClick={() => {
                      void updateHook(hook, !hook.enabled);
                    }}
                  >
                    {hook.enabled
                      ? t("harness.hookDisable")
                      : t("harness.hookEnable")}
                  </Button>
                </div>
              ) : null}
            </SettingsPanelCard>
          ))}
        </div>
      ) : (
        <SettingsEmptyState variant="dashed">
          {t("harness.noHooks")}
        </SettingsEmptyState>
      )}
      {runs.length > 0 ? (
        <div className="mt-4 space-y-1">
          <div className="text-xs font-medium text-muted-foreground">
            {t("harness.hookRecentRuns")}
          </div>
          {runs
            .slice(-10)
            .reverse()
            .map((run, index) => (
              <div
                key={`${run.hook_id}-${run.at}-${run.phase}-${index}`}
                className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground"
              >
                <span>{run.event}</span>
                <span>{run.phase}</span>
                {run.status_message ? <span>{run.status_message}</span> : null}
                <span className="app-inline-mono break-all">{run.hook_id}</span>
              </div>
            ))}
        </div>
      ) : null}
    </SettingsSection>
  );
}

