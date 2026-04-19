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

import { Button } from "@/components/ui/button";
import { APP_SETTINGS_STORAGE_KEY } from "@/core/settings";
import { type RuntimeSessionsSummary } from "@/hooks/workspace/use-runtime-sessions-data";
import {
  RUNTIME_CLIENT_STORAGE_KEY,
  type RuntimeClientIdentity,
} from "@/lib/runtime-client";
import { type RuntimeTeamRecord } from "@/lib/runtime-api";
import { formatRelativeTimestamp } from "@/lib/utils";

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

function resolveApiBaseLabel() {
  const configured = import.meta.env.VITE_API_BASE_URL?.trim();
  return configured ? configured : "same-origin /api proxy";
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
  const location = useLocation();
  const liveTeamCount = runtimeTeams.filter(
    (team) => (team.status || "").trim().toLowerCase() === "active",
  ).length;

  return (
    <div className="space-y-6">
      <SettingsSection
        title="当前工作区"
        description="这些信息用于快速确认当前前端会把请求发到哪里，以及本地设置保存在什么位置。"
      >
        <div className="grid gap-3 lg:grid-cols-2">
          <SettingsInfoCard
            tone="softer"
            title="API 基址"
            icon={<RouteIcon size={16} className="text-[var(--accent-primary)]" />}
            description={resolveApiBaseLabel()}
            descriptionClassName="break-all"
          />

          <SettingsInfoCard
            tone="softer"
            title="当前路由"
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
        title="Runtime identity"
        description="前端会为当前浏览器生成一个持久化 runtime client id，并用它派生 userId。重置后会切换到新的会话命名空间。"
      >
        <div className="grid gap-3 lg:grid-cols-2">
          <SettingsInfoCard
            tone="softer"
            title="Runtime user id"
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
              scope: {runtimeClient.workspaceScope}
            </div>
          </SettingsInfoCard>

          <SettingsInfoCard
            tone="softer"
            title="Workspace path"
            icon={<RouteIcon size={16} className="text-[var(--accent-secondary)]" />}
          >
            <p className="app-inline-mono break-all text-[var(--muted-foreground)]">
              {runtimeClient.workspacePath || "not set"}
            </p>
            <Button
              variant="secondary"
              size="sm"
              className="mt-3 gap-2"
              onClick={onResetRuntimeClientIdentity}
            >
              <RotateCcwIcon size={14} />
              重置本地 runtime client id
            </Button>
          </SettingsInfoCard>
        </div>
      </SettingsSection>

      <SettingsSection
        title="运行时概览"
        description="这里读的是当前页面已经加载到的运行时摘要，不会额外发新请求。"
      >
        <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-4">
          <StatCard
            icon={<DatabaseIcon size={16} className="text-[var(--accent-primary)]" />}
            label="可选 provider"
            value={`${providerOptions.length}`}
            detail={`${selectedProvider || "runtime default"} / ${selectedModel || "runtime default"}`}
          />
          <StatCard
            icon={<HardDriveDownloadIcon size={16} className="text-[var(--accent-secondary)]" />}
            label="会话数"
            value={`${runtimeSessionsSummary.totalCount}`}
            detail={
              runtimeSessionsSummary.latestUpdatedAt
                ? `最近更新 ${formatRelativeTimestamp(runtimeSessionsSummary.latestUpdatedAt)}`
                : "尚未发现会话"
            }
          />
          <StatCard
            icon={<PanelsTopLeftIcon size={16} className="text-[var(--accent-primary)]" />}
            label="可恢复会话"
            value={`${runtimeSessionsSummary.recoverableCount}`}
            detail={`${runtimeSessionsSummary.activeCount} active / ${runtimeSessionsSummary.archivedCount} archived`}
          />
          <StatCard
            icon={<RouteIcon size={16} className="text-[var(--accent-secondary)]" />}
            label="运行中团队"
            value={`${liveTeamCount}`}
            detail={`共加载 ${runtimeTeams.length} 个团队摘要`}
          />
        </div>
      </SettingsSection>

      <SettingsSection
        title="本地存储"
        description="设置数据存于浏览器 localStorage，不会写回仓库配置文件。"
      >
        <div className="grid gap-3 lg:grid-cols-2">
          <SettingsInfoCard
            title="settings localStorage key"
            className="rounded-[0.9rem]"
          >
            <p className="app-inline-mono break-all text-[var(--muted-foreground)]">
              {APP_SETTINGS_STORAGE_KEY}
            </p>
          </SettingsInfoCard>

          <SettingsInfoCard
            title="runtime client localStorage key"
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
