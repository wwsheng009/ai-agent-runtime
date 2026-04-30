import {
  DatabaseIcon,
  FingerprintIcon,
  HardDriveDownloadIcon,
  PanelsTopLeftIcon,
  RotateCcwIcon,
  RouteIcon,
} from "lucide-react";
import type { ReactNode } from "react";
import { useLocation } from "react-router-dom";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { APP_SETTINGS_STORAGE_KEY, useAppSettings } from "@/core/settings";
import { formatRelativeTimestamp } from "@/i18n/format";
import { type RuntimeSessionsSummary } from "@/hooks/workspace/use-runtime-sessions-data";
import {
  RUNTIME_CLIENT_STORAGE_KEY,
  type RuntimeClientIdentity,
} from "@/lib/runtime-client";
import { type RuntimeTeamRecord } from "@/lib/runtime-api";

import { SettingsInfoCard } from "./settings-info-card";
import { SettingsSection } from "./settings-section";

type AboutSettingsPageProps = {
  onResetRuntimeClientIdentity: () => void;
  providerOptions: string[];
  runtimeClient: RuntimeClientIdentity;
  runtimeSessionsSummary: RuntimeSessionsSummary;
  runtimeTeams: RuntimeTeamRecord[];
  selectedModel: string;
  selectedProvider: string;
};

function resolveApiBaseLabel(fallbackLabel: string) {
  const configured = import.meta.env.VITE_API_BASE_URL?.trim();
  return configured ? configured : fallbackLabel;
}

export function AboutSettingsPage({
  onResetRuntimeClientIdentity,
  providerOptions,
  runtimeClient,
  runtimeSessionsSummary,
  runtimeTeams,
  selectedModel,
  selectedProvider,
}: AboutSettingsPageProps) {
  const { t } = useTranslation("settings");
  const { resolvedLocale } = useAppSettings();
  const location = useLocation();
  const liveTeamCount = runtimeTeams.filter(
    (team) => (team.status || "").trim().toLowerCase() === "active",
  ).length;
  const apiBaseLabel = resolveApiBaseLabel(t("about.apiBaseFallback"));

  return (
    <div className="space-y-6">
      <SettingsSection
        title={t("about.currentWorkspace")}
        description={t("about.description")}
      >
        <div className="grid gap-3 lg:grid-cols-2">
          <SettingsInfoCard
            tone="softer"
            title={t("about.apiBase")}
            icon={<RouteIcon size={16} className="text-[var(--accent-primary)]" />}
            description={apiBaseLabel}
            descriptionClassName="break-all"
          />

          <SettingsInfoCard
            tone="softer"
            title={t("about.currentRoute")}
            icon={
              <PanelsTopLeftIcon
                size={16}
                className="text-[var(--accent-secondary)]"
              />
            }
            description={location.pathname}
            descriptionClassName="break-all"
          />
        </div>
      </SettingsSection>

      <SettingsSection
        title={t("about.runtimeIdentity")}
        description={t("about.runtimeIdentityDescription")}
      >
        <div className="grid gap-3 lg:grid-cols-2">
          <SettingsInfoCard
            tone="softer"
            title={t("about.runtimeUserId")}
            icon={
              <FingerprintIcon
                size={16}
                className="text-[var(--accent-primary)]"
              />
            }
          >
            <p className="app-inline-mono break-all text-[var(--muted-foreground)]">
              {runtimeClient.userId}
            </p>
            <div className="mt-3 text-xs leading-6 text-[var(--muted-foreground)]">
              {t("about.scopeLabel")}: {runtimeClient.workspaceScope}
            </div>
          </SettingsInfoCard>

          <SettingsInfoCard
            tone="softer"
            title={t("about.workspacePath")}
            icon={<RouteIcon size={16} className="text-[var(--accent-secondary)]" />}
          >
            <p className="app-inline-mono break-all text-[var(--muted-foreground)]">
              {runtimeClient.workspacePath || t("about.notSet")}
            </p>
            <Button
              variant="secondary"
              size="sm"
              className="mt-3 gap-2"
              onClick={onResetRuntimeClientIdentity}
            >
              <RotateCcwIcon size={14} />
              {t("about.resetRuntimeClientId")}
            </Button>
          </SettingsInfoCard>
        </div>
      </SettingsSection>

      <SettingsSection
        title={t("about.runtimeOverview")}
        description={t("about.runtimeOverviewDescription")}
      >
        <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-4">
          <StatCard
            icon={<DatabaseIcon size={16} className="text-[var(--accent-primary)]" />}
            label={t("about.selectedProvider")}
            value={`${providerOptions.length}`}
            detail={`${selectedProvider || t("about.runtimeDefault")} / ${selectedModel || t("about.runtimeDefault")}`}
          />
          <StatCard
            icon={<HardDriveDownloadIcon size={16} className="text-[var(--accent-secondary)]" />}
            label={t("about.sessionCount")}
            value={`${runtimeSessionsSummary.totalCount}`}
            detail={
              runtimeSessionsSummary.latestUpdatedAt
                ? t("about.latestUpdated", {
                    time: formatRelativeTimestamp(
                      resolvedLocale,
                      runtimeSessionsSummary.latestUpdatedAt,
                    ),
                  })
                : t("about.noSessions")
            }
          />
          <StatCard
            icon={<PanelsTopLeftIcon size={16} className="text-[var(--accent-primary)]" />}
            label={t("about.recoverableSessions")}
            value={`${runtimeSessionsSummary.recoverableCount}`}
            detail={t("about.sessionBreakdown", {
              active: runtimeSessionsSummary.activeCount,
              archived: runtimeSessionsSummary.archivedCount,
            })}
          />
          <StatCard
            icon={<RouteIcon size={16} className="text-[var(--accent-secondary)]" />}
            label={t("about.activeTeams")}
            value={`${liveTeamCount}`}
            detail={t("about.activeTeamsSummary", { count: runtimeTeams.length })}
          />
        </div>
      </SettingsSection>

      <SettingsSection
        title={t("about.localStorage")}
        description={t("about.localStorageDescription")}
      >
        <div className="grid gap-3 lg:grid-cols-2">
          <SettingsInfoCard
            title={t("about.settingsKey")}
            className="rounded-[0.9rem]"
          >
            <p className="app-inline-mono break-all text-[var(--muted-foreground)]">
              {APP_SETTINGS_STORAGE_KEY}
            </p>
          </SettingsInfoCard>

          <SettingsInfoCard
            title={t("about.runtimeClientKey")}
            className="rounded-[0.9rem]"
          >
            <p className="app-inline-mono break-all text-[var(--muted-foreground)]">
              {RUNTIME_CLIENT_STORAGE_KEY}
            </p>
          </SettingsInfoCard>
        </div>
      </SettingsSection>
    </div>
  );
}

function StatCard({
  detail,
  icon,
  label,
  value,
}: {
  detail: string;
  icon: ReactNode;
  label: string;
  value: string;
}) {
  return (
    <SettingsInfoCard tone="softer" title={label} icon={icon}>
      <div className="text-xl font-semibold tracking-[-0.03em] text-[var(--foreground)]">
        {value}
      </div>
      <p className="mt-2 text-sm leading-6 text-[var(--muted-foreground)]">
        {detail}
      </p>
    </SettingsInfoCard>
  );
}
