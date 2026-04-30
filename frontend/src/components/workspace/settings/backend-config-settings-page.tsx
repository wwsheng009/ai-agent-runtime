import {
  ActivityIcon,
  BotIcon,
  FileTextIcon,
  GaugeIcon,
  HardDriveDownloadIcon,
  InfoIcon,
  LoaderCircleIcon,
  RefreshCcwIcon,
  RotateCcwIcon,
  RouteIcon,
  Settings2Icon,
  WifiIcon,
  type LucideIcon,
} from "lucide-react";
import {
  lazy,
  Suspense,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  getRuntimeConfigDocument,
  getRuntimeServiceStatus,
  previewRuntimeConfigDocument,
  restartRuntimeService,
  saveRuntimeConfigDocument,
  type RuntimeConfigDocument,
  type RuntimeServiceStatus,
} from "@/lib/runtime-api";
import { cn } from "@/lib/utils";

import { editorControlClassName } from "./editor-control-class";
import { buildConfigLineDiff } from "./runtime-config-diff";
import {
  getConfigValueAtPath,
  removeConfigValueAtPath,
  setConfigValueAtPath,
} from "./runtime-config-editor-utils";
import {
  getRuntimeCircuitBreakerConfig,
  type RuntimeCircuitBreakerConfigSummary,
} from "./runtime-circuit-breaker-domain-utils";
import { type ConcurrencyProviderLimitDraftInput } from "./runtime-concurrency-domain-editor";
import {
  getRuntimeConcurrencyConfig,
  listRuntimeConcurrencyProviderLimits,
  type RuntimeConcurrencyConfigSummary,
} from "./runtime-concurrency-domain-utils";
import {
  getRuntimeAuthConfig,
  listRuntimeProviderGroupSummaries,
  type RuntimeAuthConfigSummary,
} from "./runtime-config-domain-utils";
import {
  getRuntimeDefaultProvider,
  isConfigRecord,
  listRuntimeProviderSummaries,
} from "./runtime-provider-config-utils";
import {
  buildProviderRecordFromDraft,
  type ProviderDraftInput,
} from "./runtime-provider-domain-form-utils";
import {
  buildProviderGroupRecordFromDraft,
  type ProviderGroupDraftInput,
} from "./runtime-provider-groups-domain-form-utils";
import {
  buildRateLimitApiKeyRecordFromDraft,
  buildRateLimitPathRecordFromDraft,
  type RateLimitApiKeyDraftInput,
  type RateLimitPathDraftInput,
} from "./runtime-rate-limit-domain-form-utils";
import {
  getRuntimeRateLimitConfig,
  listRuntimeRateLimitApiKeyLimits,
  listRuntimeRateLimitPathLimits,
  type RuntimeRateLimitConfigSummary,
} from "./runtime-rate-limit-domain-utils";
import {
  buildProviderQueueProviderRecordFromDraft,
  type ProviderQueueProviderDraftInput,
} from "./runtime-provider-queue-domain-form-utils";
import {
  getRuntimeProviderQueueConfig,
  listRuntimeProviderQueueProviders,
  type RuntimeProviderQueueConfigSummary,
} from "./runtime-provider-queue-domain-utils";
import {
  getRuntimeResourceManagerConfig,
  type RuntimeResourceManagerConfigSummary,
} from "./runtime-resource-manager-domain-utils";
import {
  getRuntimeMonitorConfig,
  normalizeMonitorChannels,
  type RuntimeMonitorConfigSummary,
} from "./runtime-monitor-domain-utils";
import {
  buildRuntimeProxyRecord,
  getRuntimeProxyConfig,
  hasRuntimeProxyConfig,
  summarizeRuntimeProxyConfig,
  type RuntimeProxyConfigSummary,
} from "./runtime-proxy-domain-utils";
import {
  getRuntimeWebsocketConfig,
  normalizeWebsocketProtocols,
  type RuntimeWebsocketConfigSummary,
} from "./runtime-websocket-domain-utils";
import { notifyRuntimeModelCatalogChanged } from "@/lib/runtime-model-catalog-sync";
import {
  buildRouteRecordFromDraft,
  type RouteDraftInput,
} from "./runtime-routing-domain-form-utils";
import {
  getRuntimeRoutingConfig,
  listRuntimeRouteSummaries,
  type RuntimeRoutingConfigSummary,
} from "./runtime-routing-domain-utils";
import { type RetryRuleDraftInput } from "./runtime-retry-domain-editor";
import {
  getRuntimeRetryConfig,
  listRuntimeRetryRules,
  normalizeRetryMatchList,
  type RuntimeRetryConfigSummary,
} from "./runtime-retry-domain-utils";
import {
  buildTransformerModifierRecordFromDraft,
  type TransformerModifierDraftInput,
} from "./runtime-transformer-domain-form-utils";
import {
  getRuntimeTransformerConfig,
  listRuntimeTransformerModifierSummaries,
  type RuntimeTransformerConfigSummary,
  type TransformerModifierScope,
} from "./runtime-transformer-domain-utils";
import { SettingsSection } from "./settings-section";

const RuntimeProviderDomainEditor = lazy(() =>
  import("./runtime-provider-domain-editor").then((module) => ({
    default: module.RuntimeProviderDomainEditor,
  })),
);
const RuntimeProviderGroupsDomainEditor = lazy(() =>
  import("./runtime-provider-groups-domain-editor").then((module) => ({
    default: module.RuntimeProviderGroupsDomainEditor,
  })),
);
const RuntimeAuthDomainEditor = lazy(() =>
  import("./runtime-auth-domain-editor").then((module) => ({
    default: module.RuntimeAuthDomainEditor,
  })),
);
const RuntimeRoutingDomainEditor = lazy(() =>
  import("./runtime-routing-domain-editor").then((module) => ({
    default: module.RuntimeRoutingDomainEditor,
  })),
);
const RuntimeRateLimitDomainEditor = lazy(() =>
  import("./runtime-rate-limit-domain-editor").then((module) => ({
    default: module.RuntimeRateLimitDomainEditor,
  })),
);
const RuntimeResourceManagerDomainEditor = lazy(() =>
  import("./runtime-resource-manager-domain-editor").then((module) => ({
    default: module.RuntimeResourceManagerDomainEditor,
  })),
);
const RuntimeProviderQueueDomainEditor = lazy(() =>
  import("./runtime-provider-queue-domain-editor").then((module) => ({
    default: module.RuntimeProviderQueueDomainEditor,
  })),
);
const RuntimeConcurrencyDomainEditor = lazy(() =>
  import("./runtime-concurrency-domain-editor").then((module) => ({
    default: module.RuntimeConcurrencyDomainEditor,
  })),
);
const RuntimeRetryDomainEditor = lazy(() =>
  import("./runtime-retry-domain-editor").then((module) => ({
    default: module.RuntimeRetryDomainEditor,
  })),
);
const RuntimeMonitorDomainEditor = lazy(() =>
  import("./runtime-monitor-domain-editor").then((module) => ({
    default: module.RuntimeMonitorDomainEditor,
  })),
);
const RuntimeProxyDomainEditor = lazy(() =>
  import("./runtime-proxy-domain-editor").then((module) => ({
    default: module.RuntimeProxyDomainEditor,
  })),
);
const RuntimeWebsocketDomainEditor = lazy(() =>
  import("./runtime-websocket-domain-editor").then((module) => ({
    default: module.RuntimeWebsocketDomainEditor,
  })),
);
const RuntimeCircuitBreakerDomainEditor = lazy(() =>
  import("./runtime-circuit-breaker-domain-editor").then((module) => ({
    default: module.RuntimeCircuitBreakerDomainEditor,
  })),
);
const RuntimeTransformerDomainEditor = lazy(() =>
  import("./runtime-transformer-domain-editor").then((module) => ({
    default: module.RuntimeTransformerDomainEditor,
  })),
);

type EditorMode =
  | "providers"
  | "providerGroups"
  | "networkProxy"
  | "routing"
  | "rateLimit"
  | "resourceManager"
  | "providerQueue"
  | "concurrency"
  | "retry"
  | "monitor"
  | "websocket"
  | "circuitBreaker"
  | "transformer"
  | "auth"
  | "source";

const modeMenuEntries: Array<{
  descriptionKey: string;
  icon: LucideIcon;
  labelKey: string;
  mode: EditorMode;
}> = [
  {
    mode: "providers",
    labelKey: "runtimeConfig.editor.modes.providers.label",
    descriptionKey: "runtimeConfig.editor.modes.providers.description",
    icon: BotIcon,
  },
  {
    mode: "providerGroups",
    labelKey: "runtimeConfig.editor.modes.providerGroups.label",
    descriptionKey: "runtimeConfig.editor.modes.providerGroups.description",
    icon: RouteIcon,
  },
  {
    mode: "networkProxy",
    labelKey: "runtimeConfig.editor.modes.networkProxy.label",
    descriptionKey: "runtimeConfig.editor.modes.networkProxy.description",
    icon: RouteIcon,
  },
  {
    mode: "auth",
    labelKey: "runtimeConfig.editor.modes.auth.label",
    descriptionKey: "runtimeConfig.editor.modes.auth.description",
    icon: Settings2Icon,
  },
  {
    mode: "routing",
    labelKey: "runtimeConfig.editor.modes.routing.label",
    descriptionKey: "runtimeConfig.editor.modes.routing.description",
    icon: RouteIcon,
  },
  {
    mode: "rateLimit",
    labelKey: "runtimeConfig.editor.modes.rateLimit.label",
    descriptionKey: "runtimeConfig.editor.modes.rateLimit.description",
    icon: GaugeIcon,
  },
  {
    mode: "resourceManager",
    labelKey: "runtimeConfig.editor.modes.resourceManager.label",
    descriptionKey: "runtimeConfig.editor.modes.resourceManager.description",
    icon: RouteIcon,
  },
  {
    mode: "providerQueue",
    labelKey: "runtimeConfig.editor.modes.providerQueue.label",
    descriptionKey: "runtimeConfig.editor.modes.providerQueue.description",
    icon: GaugeIcon,
  },
  {
    mode: "concurrency",
    labelKey: "runtimeConfig.editor.modes.concurrency.label",
    descriptionKey: "runtimeConfig.editor.modes.concurrency.description",
    icon: GaugeIcon,
  },
  {
    mode: "retry",
    labelKey: "runtimeConfig.editor.modes.retry.label",
    descriptionKey: "runtimeConfig.editor.modes.retry.description",
    icon: RefreshCcwIcon,
  },
  {
    mode: "monitor",
    labelKey: "runtimeConfig.editor.modes.monitor.label",
    descriptionKey: "runtimeConfig.editor.modes.monitor.description",
    icon: ActivityIcon,
  },
  {
    mode: "websocket",
    labelKey: "runtimeConfig.editor.modes.websocket.label",
    descriptionKey: "runtimeConfig.editor.modes.websocket.description",
    icon: WifiIcon,
  },
  {
    mode: "circuitBreaker",
    labelKey: "runtimeConfig.editor.modes.circuitBreaker.label",
    descriptionKey: "runtimeConfig.editor.modes.circuitBreaker.description",
    icon: ActivityIcon,
  },
  {
    mode: "transformer",
    labelKey: "runtimeConfig.editor.modes.transformer.label",
    descriptionKey: "runtimeConfig.editor.modes.transformer.description",
    icon: Settings2Icon,
  },
  {
    mode: "source",
    labelKey: "runtimeConfig.editor.modes.source.label",
    descriptionKey: "runtimeConfig.editor.modes.source.description",
    icon: FileTextIcon,
  },
];

function formatBytes(value: number) {
  if (value < 1024) {
    return `${value} B`;
  }
  if (value < 1024 * 1024) {
    return `${(value / 1024).toFixed(1)} KB`;
  }
  return `${(value / (1024 * 1024)).toFixed(1)} MB`;
}

function formatTimestamp(value?: string) {
  if (!value) {
    return "未写入";
  }
  return new Intl.DateTimeFormat("zh-CN", {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(new Date(value));
}

function getModeLabel(
  mode: EditorMode,
  entries: Array<{ label: string; mode: EditorMode }>,
) {
  return entries.find((entry) => entry.mode === mode)?.label ?? mode;
}

function sleep(ms: number) {
  return new Promise((resolve) => {
    window.setTimeout(resolve, ms);
  });
}

function parseLooseScalar(value: string) {
  const trimmed = value.trim();
  if (!trimmed) {
    return "";
  }
  if (/^-?\d+(?:\.\d+)?$/.test(trimmed)) {
    return Number(trimmed);
  }
  return value;
}

function StatCard({
  detail,
  icon: Icon,
  label,
  value,
}: {
  detail: string;
  icon: LucideIcon;
  label: string;
  value: string;
}) {
  return (
    <div
      title={detail}
      className="rounded-[0.85rem] border border-[var(--border)] bg-[var(--surface-softer)] px-3 py-2.5"
    >
      <div className="flex items-center gap-3">
        <span className="inline-flex size-7 shrink-0 items-center justify-center rounded-[0.7rem] border border-[var(--border)] bg-[var(--surface-solid)] text-[var(--accent-primary)]">
          <Icon size={14} />
        </span>
        <div className="min-w-0">
          <div className="app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
            {label}
          </div>
          <div className="mt-0.5 truncate text-sm font-semibold text-[var(--foreground)]">
            {value}
          </div>
        </div>
      </div>
      <p className="mt-2 truncate text-xs leading-5 text-[var(--muted-foreground)]">
        {detail}
      </p>
    </div>
  );
}

function SummaryPill({ label, value }: { label: string; value: string }) {
  return (
    <span className="inline-flex items-center gap-1.5 rounded-[0.65rem] border border-[var(--border)] bg-[var(--surface-solid)] px-2 py-0.5">
      <span className="app-text-10 uppercase tracking-[0.12em] text-[var(--muted-foreground)]">
        {label}
      </span>
      <span className="text-xs font-semibold text-[var(--foreground)]">
        {value}
      </span>
    </span>
  );
}

function countImpactPaths(paths?: string[]) {
  return Array.isArray(paths) ? paths.length : 0;
}

function buildSaveStatusMessage(document: RuntimeConfigDocument) {
  const targetPath = document.path?.trim() || "当前 runtime 配置文档";
  const impact = document.runtime_impact;
  const appliedCount = countImpactPaths(impact?.applied_paths);
  const hotReloadCount = countImpactPaths(impact?.hot_reload_paths);
  const restartCount = countImpactPaths(impact?.restart_required_paths);
  const inactiveCount = countImpactPaths(impact?.inactive_paths);

  const parts: string[] = [];
  if (appliedCount > 0) {
    parts.push(`${appliedCount} 处改动已即时应用`);
  } else if (hotReloadCount > 0) {
    parts.push(`${hotReloadCount} 处改动支持热重载`);
  }
  if (restartCount > 0) {
    parts.push(`${restartCount} 处改动仍需重启`);
  }
  if (inactiveCount > 0) {
    parts.push(`${inactiveCount} 处改动当前不会影响 runtime-server`);
  }

  if (parts.length === 0) {
    return `配置已写回 ${targetPath}。`;
  }
  return `配置已写回 ${targetPath}。${parts.join("，")}。`;
}

function ImpactStat({
  accentClassName,
  detail,
  label,
  value,
}: {
  accentClassName: string;
  detail: string;
  label: string;
  value: string;
}) {
  return (
    <div className="rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-solid)] px-3 py-2.5">
      <div className="flex items-center justify-between gap-3">
        <div className="app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
          {label}
        </div>
        <span className={cn("text-sm font-semibold", accentClassName)}>
          {value}
        </span>
      </div>
      <div className="mt-1.5 text-xs leading-5 text-[var(--muted-foreground)]">
        {detail}
      </div>
    </div>
  );
}

function ImpactPathList({
  emptyText,
  paths,
  title,
  toneClassName,
}: {
  emptyText: string;
  paths?: string[];
  title: string;
  toneClassName: string;
}) {
  const visiblePaths = Array.isArray(paths) ? paths.slice(0, 6) : [];
  const hiddenCount = Math.max(
    0,
    countImpactPaths(paths) - visiblePaths.length,
  );

  return (
    <div className="rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-solid)] p-3">
      <div className="flex items-center justify-between gap-3">
        <div className="text-sm font-semibold text-[var(--foreground)]">
          {title}
        </div>
        <Badge className={toneClassName}>{countImpactPaths(paths)} 项</Badge>
      </div>
      {visiblePaths.length > 0 ? (
        <div className="mt-2 grid gap-1.5">
          {visiblePaths.map((path) => (
            <div
              key={path}
              className="rounded-[0.7rem] border border-[var(--border)] bg-[var(--surface-softer)] px-2.5 py-1.5 font-mono app-text-12 text-[var(--foreground)]"
            >
              {path}
            </div>
          ))}
          {hiddenCount > 0 ? (
            <div className="text-xs text-[var(--muted-foreground)]">
              还有 {hiddenCount} 项未展开。
            </div>
          ) : null}
        </div>
      ) : (
        <div className="mt-2 text-sm leading-6 text-[var(--muted-foreground)]">
          {emptyText}
        </div>
      )}
    </div>
  );
}

function RuntimeImpactPanel({
  document,
  onRestart,
  serviceRunning,
}: {
  document: RuntimeConfigDocument;
  onRestart: () => void;
  serviceRunning: boolean;
}) {
  const impact = document.runtime_impact;
  if (!impact || countImpactPaths(impact.changed_paths) === 0) {
    return null;
  }

  const changedCount = countImpactPaths(impact.changed_paths);
  const hotReloadCount = countImpactPaths(impact.hot_reload_paths);
  const restartCount = countImpactPaths(impact.restart_required_paths);
  const inactiveCount = countImpactPaths(impact.inactive_paths);
  const appliedCount = countImpactPaths(impact.applied_paths);
  const warnings = document.warnings ?? [];
  const isPreview = Boolean(
    document !== null &&
    document.updated_at == null &&
    warnings[0] === "这是预览结果，尚未写入磁盘。",
  );

  return (
    <SettingsSection
      title={isPreview ? "预览影响" : "运行时影响"}
      description={
        isPreview
          ? "这次草稿若保存，会有多少配置即时生效、多少仍需重启。"
          : "这里展示最近一次已保存配置对当前 runtime-server 的实际影响。"
      }
    >
      <div className="rounded-[0.95rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
        <div className="flex flex-wrap items-center justify-between gap-2.5">
          <div className="flex flex-wrap items-center gap-2">
            <Badge>{isPreview ? "基于预览" : "最近保存结果"}</Badge>
            <Badge>{`${changedCount} 处变更`}</Badge>
            <Badge>
              {document.restart_required ? "含需重启项" : "无需重启"}
            </Badge>
            {appliedCount > 0 ? (
              <Badge>{`${appliedCount} 处已热应用`}</Badge>
            ) : null}
          </div>
          {document.restart_required ? (
            <Button
              variant={serviceRunning ? "primary" : "secondary"}
              size="sm"
              onClick={onRestart}
            >
              <RotateCcwIcon size={14} />
              重启使配置生效
            </Button>
          ) : null}
        </div>

        <div className="mt-3 grid gap-2.5 md:grid-cols-2 xl:grid-cols-4">
          <ImpactStat
            label="Changed"
            value={`${changedCount}`}
            detail="这次配置差异命中的总路径数。"
            accentClassName="text-[var(--foreground)]"
          />
          <ImpactStat
            label="Hot Reload"
            value={`${hotReloadCount}`}
            detail={
              appliedCount > 0
                ? "这些路径已在当前进程中立即应用。"
                : "这些路径支持即时热重载。"
            }
            accentClassName="text-[#8fd0c6]"
          />
          <ImpactStat
            label="Restart"
            value={`${restartCount}`}
            detail="这些路径属于启动期注入配置，仍需重启。"
            accentClassName="text-[#f59e7d]"
          />
          <ImpactStat
            label="Inactive"
            value={`${inactiveCount}`}
            detail="这些路径当前不会影响 runtime-server 进程。"
            accentClassName="text-[#e7d58c]"
          />
        </div>

        <div className="mt-3 grid gap-3 xl:grid-cols-2">
          <ImpactPathList
            title="即时应用"
            paths={impact.applied_paths}
            emptyText="本次保存没有即时应用的运行时路径。"
            toneClassName="border-[#8fd0c6]/24 bg-[#8fd0c6]/10 text-[#d6fff6]"
          />
          <ImpactPathList
            title="需重启"
            paths={impact.restart_required_paths}
            emptyText="本次变更没有命中需重启的路径。"
            toneClassName="border-[#f59e7d]/24 bg-[#f59e7d]/10 text-[#ffd9ce]"
          />
          <ImpactPathList
            title="可热重载"
            paths={impact.hot_reload_paths}
            emptyText="本次变更没有命中热重载路径。"
            toneClassName="border-[#8fd0c6]/24 bg-[#8fd0c6]/10 text-[#d6fff6]"
          />
          <ImpactPathList
            title="当前不生效"
            paths={impact.inactive_paths}
            emptyText="本次变更没有命中 inactive 路径。"
            toneClassName="border-[#e7d58c]/24 bg-[#e7d58c]/10 text-[#fff3c4]"
          />
        </div>

        {warnings.length > 0 ? (
          <div className="mt-3 rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-solid)] p-3">
            <div className="text-sm font-semibold text-[var(--foreground)]">
              后端提示
            </div>
            <div className="mt-2 grid gap-2">
              {warnings.map((warning, index) => (
                <div
                  key={`${warning}-${index}`}
                  className="rounded-[0.7rem] border border-[var(--border)] bg-[var(--surface-softer)] px-2.5 py-2 text-sm leading-6 text-[var(--muted-foreground)]"
                >
                  {warning}
                </div>
              ))}
            </div>
          </div>
        ) : null}
      </div>
    </SettingsSection>
  );
}

function ControlPanel({
  children,
  description,
  title,
}: {
  children: ReactNode;
  description: string;
  title: string;
}) {
  return (
    <div className="rounded-[0.9rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
      <div className="flex items-center gap-2">
        <div className="app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
          {title}
        </div>
        <span
          title={description}
          className="inline-flex size-5 items-center justify-center rounded-[0.6rem] border border-[var(--border)] bg-[var(--surface-solid)] text-[var(--muted-foreground)]"
        >
          <InfoIcon size={12} />
        </span>
      </div>
      <div className="mt-3">{children}</div>
    </div>
  );
}

function MenuButton({
  active,
  badge,
  description,
  disabled = false,
  icon: Icon,
  label,
  onClick,
}: {
  active: boolean;
  badge?: string;
  description: string;
  disabled?: boolean;
  icon: LucideIcon;
  label: string;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      disabled={disabled}
      title={description}
      onClick={onClick}
      className={cn(
        "min-w-0 max-w-full w-full rounded-[0.8rem] border px-3 py-2 text-left transition disabled:cursor-not-allowed disabled:opacity-60",
        active
          ? "border-[var(--accent-primary-border)] bg-[var(--accent-primary-soft)]"
          : "border-[var(--border)] bg-[var(--surface-solid)] hover:border-[var(--border-strong)] hover:bg-[var(--surface-soft)]",
      )}
    >
      <div className="flex items-center justify-between gap-3">
        <div className="flex min-w-0 items-center gap-3">
          <span className="rounded-[0.65rem] border border-[var(--border)] bg-[var(--surface-softer)] p-1.5 text-[var(--accent-primary)]">
            <Icon size={14} />
          </span>
          <div className="min-w-0 truncate text-[13px] font-semibold text-[var(--foreground)]">
            {label}
          </div>
        </div>
        {badge ? <Badge>{badge}</Badge> : null}
      </div>
    </button>
  );
}

export function BackendConfigSettingsPage() {
  const { t } = useTranslation("runtimeConfig");
  const [document, setDocument] = useState<RuntimeConfigDocument | null>(null);
  const [draftParsed, setDraftParsed] = useState<unknown>(null);
  const [draftRaw, setDraftRaw] = useState("");
  const [previewDocument, setPreviewDocument] =
    useState<RuntimeConfigDocument | null>(null);
  const [mode, setMode] = useState<EditorMode>("providers");
  const [isLoading, setIsLoading] = useState(true);
  const [isSaving, setIsSaving] = useState(false);
  const [isPreviewLoading, setIsPreviewLoading] = useState(false);
  const [isRestarting, setIsRestarting] = useState(false);
  const [isModeSwitching, setIsModeSwitching] = useState(false);
  const [serviceStatus, setServiceStatus] =
    useState<RuntimeServiceStatus | null>(null);
  const [serviceError, setServiceError] = useState<string | null>(null);
  const [statusMessage, setStatusMessage] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const providers = useMemo(
    () => listRuntimeProviderSummaries(draftParsed),
    [draftParsed],
  );
  const providerGroups = useMemo(
    () => listRuntimeProviderGroupSummaries(draftParsed),
    [draftParsed],
  );
  const proxyConfig = useMemo(
    () => getRuntimeProxyConfig(draftParsed),
    [draftParsed],
  );
  const routes = useMemo(
    () => listRuntimeRouteSummaries(draftParsed),
    [draftParsed],
  );
  const routingConfig = useMemo(
    () => getRuntimeRoutingConfig(draftParsed),
    [draftParsed],
  );
  const rateLimitConfig = useMemo(
    () => getRuntimeRateLimitConfig(draftParsed),
    [draftParsed],
  );
  const resourceManagerConfig = useMemo(
    () => getRuntimeResourceManagerConfig(draftParsed),
    [draftParsed],
  );
  const providerQueueConfig = useMemo(
    () => getRuntimeProviderQueueConfig(draftParsed),
    [draftParsed],
  );
  const providerQueueProviders = useMemo(
    () => listRuntimeProviderQueueProviders(draftParsed),
    [draftParsed],
  );
  const apiKeyLimits = useMemo(
    () => listRuntimeRateLimitApiKeyLimits(draftParsed),
    [draftParsed],
  );
  const pathLimits = useMemo(
    () => listRuntimeRateLimitPathLimits(draftParsed),
    [draftParsed],
  );
  const concurrencyConfig = useMemo(
    () => getRuntimeConcurrencyConfig(draftParsed),
    [draftParsed],
  );
  const concurrencyProviderLimits = useMemo(
    () => listRuntimeConcurrencyProviderLimits(draftParsed),
    [draftParsed],
  );
  const retryConfig = useMemo(
    () => getRuntimeRetryConfig(draftParsed),
    [draftParsed],
  );
  const retryRules = useMemo(
    () => listRuntimeRetryRules(draftParsed),
    [draftParsed],
  );
  const transformerConfig = useMemo(
    () => getRuntimeTransformerConfig(draftParsed),
    [draftParsed],
  );
  const requestTransformerModifiers = useMemo(
    () => listRuntimeTransformerModifierSummaries(draftParsed, "request"),
    [draftParsed],
  );
  const responseTransformerModifiers = useMemo(
    () => listRuntimeTransformerModifierSummaries(draftParsed, "response"),
    [draftParsed],
  );
  const monitorConfig = useMemo(
    () => getRuntimeMonitorConfig(draftParsed),
    [draftParsed],
  );
  const websocketConfig = useMemo(
    () => getRuntimeWebsocketConfig(draftParsed),
    [draftParsed],
  );
  const circuitBreakerConfig = useMemo(
    () => getRuntimeCircuitBreakerConfig(draftParsed),
    [draftParsed],
  );
  const authConfig = useMemo(
    () => getRuntimeAuthConfig(draftParsed),
    [draftParsed],
  );
  const defaultProvider = useMemo(
    () => getRuntimeDefaultProvider(draftParsed),
    [draftParsed],
  );
  const translatedModeMenuEntries = useMemo(
    () =>
      modeMenuEntries.map((entry) => ({
        ...entry,
        description: t(entry.descriptionKey),
        label: t(entry.labelKey),
      })),
    [t],
  );

  const hasDomainChanges =
    JSON.stringify(document?.parsed ?? null) !==
    JSON.stringify(draftParsed ?? null);
  const hasSourceChanges = draftRaw !== (document?.raw ?? "");
  const hasUnsavedChanges = hasDomainChanges || hasSourceChanges;
  const enabledProviderCount = providers.filter(
    (provider) => provider.enabled,
  ).length;
  const draftLineCount = draftRaw === "" ? 0 : draftRaw.split(/\r?\n/).length;

  function getModeBadge(modeValue: EditorMode) {
    switch (modeValue) {
      case "providers":
        return `${providers.length} 个`;
      case "providerGroups":
        return `${providerGroups.length} 组`;
      case "networkProxy":
        return summarizeRuntimeProxyConfig(proxyConfig);
      case "routing":
        return `${routes.length} 条`;
      case "rateLimit":
        return `${apiKeyLimits.length + pathLimits.length} 条`;
      case "resourceManager":
        return resourceManagerConfig.enabled ? "已启用" : "已关闭";
      case "providerQueue":
        return providerQueueConfig.enabled
          ? `${providerQueueProviders.length} 条`
          : "已关闭";
      case "concurrency":
        return concurrencyConfig.enabled
          ? `${concurrencyProviderLimits.length} 条`
          : "已关闭";
      case "retry":
        return retryConfig.enabled ? `${retryRules.length} 条` : "已关闭";
      case "monitor":
        return monitorConfig.enabled ? "已启用" : "已关闭";
      case "websocket":
        return websocketConfig.enabled ? "已启用" : "已关闭";
      case "circuitBreaker":
        return circuitBreakerConfig.failureThreshold || "--";
      case "transformer":
        return `${
          requestTransformerModifiers.length +
          responseTransformerModifiers.length
        } 条`;
      case "auth":
        return authConfig.accessAuthEnabled ? "已启用" : "已关闭";
      case "source":
      default:
        return `${draftLineCount} 行`;
    }
  }

  function getModeSummary(modeValue: EditorMode) {
    switch (modeValue) {
      case "providers":
        return "Provider 改动优先走列表和弹窗表单，默认 provider 也在表格内维护。";
      case "providerGroups":
        return "Provider group 用成员列表编辑，复杂 root 级扩展字段仍可保留在 JSON 区。";
      case "networkProxy":
        return "网络代理支持全局 HTTP / HTTPS / SOCKS5 代理与 no_proxy；留空并关闭时可回退到环境变量。";
      case "routing":
        return "Routing 同时维护根配置和 route 顺序，命中优先级不再需要回 YAML 手工调数组。";
      case "rateLimit":
        return "Rate Limit 现在可直接维护默认/全局限流，以及 API Key 和路径级覆盖规则。";
      case "resourceManager":
        return "Resource Manager 现在可直接维护默认算法、跨 Provider Key 选择、健康检查和统计保留。";
      case "providerQueue":
        return "Provider Queue 现在可直接维护默认槽位、overflow、wait_heartbeat，以及 provider 级覆盖。";
      case "concurrency":
        return "Concurrency 现在可直接维护全局并发上限、排队参数和 provider 级并发限制。";
      case "retry":
        return "Retry 现在可直接维护默认重试策略、增强策略，以及按顺序匹配的 retry rules。";
      case "monitor":
        return "Monitor 现在可直接维护 metrics、tracing、alert、pprof 和 memory 五组配置。";
      case "websocket":
        return "WebSocket 现在可直接维护 responses / realtime、HTTP bridge 和握手失败处理。";
      case "circuitBreaker":
        return "Circuit Breaker 现在可直接维护熔断阈值、失败率、时间窗口和半开恢复参数。";
      case "transformer":
        return "Transformer 现在可直接维护 HTTPTransformer 开关，以及 request/response body modifier 列表。";
      case "auth":
        return "Auth 走专用表单，未覆盖字段会继续保留在原有 auth 节点中。";
      case "source":
      default:
        return "YAML 模式保留注释和原始排版，适合作为最后兜底。";
    }
  }

  const previewDiff = useMemo(
    () =>
      document && previewDocument
        ? buildConfigLineDiff(document.raw, previewDocument.raw)
        : [],
    [document, previewDocument],
  );
  const previewAdditions = previewDiff.filter(
    (line) => line.type === "add",
  ).length;
  const previewRemovals = previewDiff.filter(
    (line) => line.type === "remove",
  ).length;
  const isPreviewFresh =
    previewDocument == null
      ? false
      : mode === "source"
        ? previewDocument.raw === draftRaw
        : JSON.stringify(previewDocument.parsed ?? null) ===
          JSON.stringify(draftParsed ?? null);
  const impactDocument =
    previewDocument &&
    countImpactPaths(previewDocument.runtime_impact?.changed_paths) > 0
      ? previewDocument
      : document;
  const shouldShowImpactPanel = Boolean(
    impactDocument &&
    countImpactPaths(impactDocument.runtime_impact?.changed_paths) > 0,
  );
  const savedRequiresRestart = Boolean(document?.restart_required);
  const previewRequiresRestart = Boolean(
    previewDocument?.restart_required && isPreviewFresh,
  );
  const canSaveAndRestart = previewRequiresRestart;

  useEffect(() => {
    async function load() {
      setIsLoading(true);
      setError(null);
      try {
        const [nextDocument, nextService] = await Promise.all([
          getRuntimeConfigDocument(),
          getRuntimeServiceStatus().catch((serviceLoadError) => {
            setServiceError(
              serviceLoadError instanceof Error
                ? serviceLoadError.message
                : "加载服务状态失败",
            );
            return null;
          }),
        ]);
        setDocument(nextDocument);
        setDraftParsed(nextDocument.parsed);
        setDraftRaw(nextDocument.raw);
        setPreviewDocument(null);
        setServiceStatus(nextService);
      } catch (loadError) {
        setError(
          loadError instanceof Error ? loadError.message : "加载后端配置失败",
        );
      } finally {
        setIsLoading(false);
      }
    }

    void load();
  }, []);

  async function switchMode(nextMode: EditorMode) {
    if (nextMode === mode) {
      return;
    }

    const needsSync =
      (mode === "source" && nextMode !== "source" && hasSourceChanges) ||
      (mode !== "source" && nextMode === "source" && hasDomainChanges);

    if (!needsSync) {
      setMode(nextMode);
      return;
    }

    setIsModeSwitching(true);
    setError(null);
    try {
      const syncedDocument = await previewRuntimeConfigDocument(
        mode === "source"
          ? { changed_by: "workspace_frontend", mode: "raw", raw: draftRaw }
          : {
              changed_by: "workspace_frontend",
              mode: "structured",
              parsed: draftParsed,
            },
      );
      setDraftParsed(syncedDocument.parsed);
      setDraftRaw(syncedDocument.raw);
      setPreviewDocument(syncedDocument);
      setMode(nextMode);
      setStatusMessage(
        nextMode === "source"
          ? "已根据当前配置域草稿同步 YAML 视图。"
          : "已根据当前 YAML 草稿同步专用配置视图。",
      );
    } catch (switchError) {
      setError(
        switchError instanceof Error
          ? switchError.message
          : "切换编辑模式前同步草稿失败",
      );
    } finally {
      setIsModeSwitching(false);
    }
  }

  async function reloadDocument() {
    if (
      hasUnsavedChanges &&
      !window.confirm("重新加载会丢弃未保存的草稿，是否继续？")
    ) {
      return;
    }
    setIsLoading(true);
    setError(null);
    try {
      const nextDocument = await getRuntimeConfigDocument();
      setDocument(nextDocument);
      setDraftParsed(nextDocument.parsed);
      setDraftRaw(nextDocument.raw);
      setPreviewDocument(null);
      setStatusMessage("已从磁盘重新加载后端配置。");
    } catch (loadError) {
      setError(
        loadError instanceof Error ? loadError.message : "重新加载后端配置失败",
      );
    } finally {
      setIsLoading(false);
    }
  }

  async function saveDocument(options?: { suppressStatusMessage?: boolean }) {
    setIsSaving(true);
    setError(null);
    try {
      const nextDocument = await saveRuntimeConfigDocument(
        mode === "source"
          ? { changed_by: "workspace_frontend", mode: "raw", raw: draftRaw }
          : {
              changed_by: "workspace_frontend",
              mode: "structured",
              parsed: draftParsed,
            },
      );
      setDocument(nextDocument);
      setDraftParsed(nextDocument.parsed);
      setDraftRaw(nextDocument.raw);
      setPreviewDocument(null);
      notifyRuntimeModelCatalogChanged();
      if (!options?.suppressStatusMessage) {
        setStatusMessage(buildSaveStatusMessage(nextDocument));
      }
      return nextDocument;
    } catch (saveError) {
      setError(
        saveError instanceof Error ? saveError.message : "保存后端配置失败",
      );
      return null;
    } finally {
      setIsSaving(false);
    }
  }

  async function generatePreview() {
    setIsPreviewLoading(true);
    setError(null);
    try {
      const nextPreview = await previewRuntimeConfigDocument(
        mode === "source"
          ? { changed_by: "workspace_frontend", mode: "raw", raw: draftRaw }
          : {
              changed_by: "workspace_frontend",
              mode: "structured",
              parsed: draftParsed,
            },
      );
      setPreviewDocument(nextPreview);
    } catch (previewError) {
      setError(
        previewError instanceof Error ? previewError.message : "生成预览失败",
      );
    } finally {
      setIsPreviewLoading(false);
    }
  }

  async function restartService(options?: {
    skipConfirm?: boolean;
    refreshDocument?: boolean;
  }) {
    if (
      !options?.skipConfirm &&
      !window.confirm("runtime-server 会短暂中断连接后重启，是否继续？")
    ) {
      return false;
    }
    setIsRestarting(true);
    setServiceError(null);
    try {
      await restartRuntimeService();
      for (let index = 0; index < 10; index += 1) {
        await sleep(1200);
        try {
          const nextStatus = await getRuntimeServiceStatus();
          setServiceStatus(nextStatus);
          if (nextStatus.running) {
            if (options?.refreshDocument ?? !hasUnsavedChanges) {
              try {
                const nextDocument = await getRuntimeConfigDocument();
                setDocument(nextDocument);
                setDraftParsed(nextDocument.parsed);
                setDraftRaw(nextDocument.raw);
                setPreviewDocument(null);
              } catch {
                // ignore document refresh failures after restart
              }
            }
            notifyRuntimeModelCatalogChanged();
            setStatusMessage("runtime-server 已重新连通。");
            return true;
          }
        } catch {
          // ignore poll errors while service restarts
        }
      }
      setStatusMessage("已发起 runtime-server 重启，请稍后确认服务状态。");
      return true;
    } catch (restartError) {
      setServiceError(
        restartError instanceof Error
          ? restartError.message
          : "重启 runtime-server 失败",
      );
      return false;
    } finally {
      setIsRestarting(false);
    }
  }

  async function saveAndRestartDocument() {
    if (
      !window.confirm(
        "保存配置后将自动重启 runtime-server。需要重启的变更会在新进程中生效，是否继续？",
      )
    ) {
      return;
    }

    const nextDocument = await saveDocument({ suppressStatusMessage: true });
    if (!nextDocument) {
      return;
    }

    if (!nextDocument.restart_required) {
      setStatusMessage(buildSaveStatusMessage(nextDocument));
      return;
    }

    const restarted = await restartService({
      skipConfirm: true,
      refreshDocument: true,
    });
    if (restarted) {
      setStatusMessage(
        "配置已保存并已重启 runtime-server，需要重启的变更已进入新进程。",
      );
    }
  }

  function handleSetDefaultProvider(name: string) {
    setDraftParsed((current: unknown) =>
      setConfigValueAtPath(current, ["providers", "default_provider"], name),
    );
    setStatusMessage(`已将默认 provider 草稿切换为 "${name}"。`);
  }

  function handleProxyConfigChange(nextProxyConfig: RuntimeProxyConfigSummary) {
    setDraftParsed((current: unknown) => {
      if (!hasRuntimeProxyConfig(nextProxyConfig)) {
        return removeConfigValueAtPath(current, ["providers", "proxy"]);
      }

      return setConfigValueAtPath(
        current,
        ["providers", "proxy"],
        buildRuntimeProxyRecord(nextProxyConfig),
      );
    });
    setStatusMessage(
      hasRuntimeProxyConfig(nextProxyConfig)
        ? `已更新全局代理草稿：${summarizeRuntimeProxyConfig(nextProxyConfig)}。`
        : "已清空全局代理草稿，运行时将回退到环境变量或直连。",
    );
  }

  function handleSaveProvider(
    draft: ProviderDraftInput,
    previousName: string | null,
  ) {
    const name = draft.name.trim();
    if (!name) {
      return "Provider 名称不能为空。";
    }
    if (
      providers.some(
        (provider) => provider.name === name && provider.name !== previousName,
      )
    ) {
      return `Provider "${name}" 已存在，请更换一个名称。`;
    }

    const nextProvider = buildProviderRecordFromDraft(draft);
    if (!nextProvider.record) {
      return nextProvider.error ?? "Provider 配置无效。";
    }

    setDraftParsed((current: unknown) => {
      const previousDefault = getRuntimeDefaultProvider(current);
      let nextValue = current;
      if (previousName && previousName !== name) {
        nextValue = removeConfigValueAtPath(nextValue, [
          "providers",
          "items",
          previousName,
        ]);
      }
      nextValue = setConfigValueAtPath(
        nextValue,
        ["providers", "items", name],
        nextProvider.record,
      );
      if (
        draft.setAsDefault ||
        !getRuntimeDefaultProvider(nextValue) ||
        (previousName && previousDefault === previousName)
      ) {
        nextValue = setConfigValueAtPath(
          nextValue,
          ["providers", "default_provider"],
          name,
        );
      }
      return nextValue;
    });
    setError(null);
    setStatusMessage(
      previousName
        ? `已更新 provider "${name}" 草稿。`
        : `已创建 provider "${name}" 草稿。`,
    );
    return null;
  }

  function handleDeleteProvider(name: string) {
    const relatedGroups = providerGroups.filter((group) =>
      group.providers.some((provider) => provider.name === name),
    );
    const relatedHint =
      relatedGroups.length > 0
        ? ` 它仍被 ${relatedGroups.length} 个 provider group 引用。`
        : "";
    if (!window.confirm(`确认删除 provider "${name}" 吗？${relatedHint}`)) {
      return;
    }

    setDraftParsed((current: unknown) => {
      let nextValue = removeConfigValueAtPath(current, [
        "providers",
        "items",
        name,
      ]);
      if (getRuntimeDefaultProvider(nextValue) === name) {
        const nextProviders = listRuntimeProviderSummaries(nextValue);
        nextValue = setConfigValueAtPath(
          nextValue,
          ["providers", "default_provider"],
          nextProviders[0]?.name ?? "",
        );
      }
      return nextValue;
    });
    setStatusMessage(`已从草稿中删除 provider "${name}"。`);
  }

  function handleSaveProviderGroup(
    draft: ProviderGroupDraftInput,
    previousName: string | null,
  ) {
    const name = draft.name.trim();
    if (
      providerGroups.some(
        (group) => group.name === name && group.name !== previousName,
      )
    ) {
      return `Provider group "${name}" 已存在，请更换一个名称。`;
    }

    const nextGroup = buildProviderGroupRecordFromDraft(draft);
    const groupRecord = nextGroup.record;
    if (!groupRecord) {
      return nextGroup.error ?? "Provider group 配置无效。";
    }

    setDraftParsed((current: unknown) =>
      upsertNamedArrayRecord(
        current,
        "provider_groups",
        previousName,
        name,
        groupRecord,
      ),
    );
    setError(null);
    setStatusMessage(
      previousName
        ? `已更新 provider group "${name}" 草稿。`
        : `已创建 provider group "${name}" 草稿。`,
    );
    return null;
  }

  function handleDeleteProviderGroup(name: string) {
    if (!window.confirm(`确认删除 provider group "${name}" 吗？`)) {
      return;
    }
    setDraftParsed((current: unknown) =>
      removeNamedArrayRecord(current, "provider_groups", name),
    );
    setStatusMessage(`已从草稿中删除 provider group "${name}"。`);
  }

  function handleAuthChange(nextAuthConfig: RuntimeAuthConfigSummary) {
    setDraftParsed((current: unknown) => {
      const currentAuthValue = getConfigValueAtPath(current, ["auth"]);
      const currentAuth = isConfigRecord(currentAuthValue)
        ? currentAuthValue
        : {};
      const currentAccessAuth = isConfigRecord(currentAuth.access_auth)
        ? currentAuth.access_auth
        : {};

      return setConfigValueAtPath(current, ["auth"], {
        ...currentAuth,
        jwt_secret: nextAuthConfig.jwtSecret,
        access_key_secret: nextAuthConfig.accessKeySecret,
        jwt_expire: nextAuthConfig.jwtExpire,
        session_timeout: nextAuthConfig.sessionTimeout,
        max_api_create_times: parseLooseScalar(
          nextAuthConfig.maxApiCreateTimes,
        ),
        admin_auth_enabled: nextAuthConfig.adminAuthEnabled,
        admin_token: nextAuthConfig.adminToken,
        access_auth: {
          ...currentAccessAuth,
          enabled: nextAuthConfig.accessAuthEnabled,
          allow_anonymous: nextAuthConfig.accessAuthAllowAnonymous,
        },
      });
    });
  }

  function handleRoutingConfigChange(
    nextRoutingConfig: RuntimeRoutingConfigSummary,
  ) {
    setDraftParsed((current: unknown) => {
      const currentRoutingValue = getConfigValueAtPath(current, ["routing"]);
      const currentRouting = isConfigRecord(currentRoutingValue)
        ? currentRoutingValue
        : {};

      return setConfigValueAtPath(current, ["routing"], {
        ...currentRouting,
        strategy: nextRoutingConfig.strategy,
        failover: nextRoutingConfig.failover,
      });
    });
  }

  function handleSaveRoute(
    draft: RouteDraftInput,
    editingIndex: number | null,
  ) {
    const nextRoute = buildRouteRecordFromDraft(draft);
    const routeRecord = nextRoute.record;
    if (!routeRecord) {
      return nextRoute.error ?? "Route 配置无效。";
    }

    setDraftParsed((current: unknown) => {
      const currentRoutingValue = getConfigValueAtPath(current, ["routing"]);
      const currentRouting = isConfigRecord(currentRoutingValue)
        ? currentRoutingValue
        : {};
      const currentRoutes = Array.isArray(currentRouting.routes)
        ? [...currentRouting.routes]
        : [];

      if (editingIndex == null) {
        currentRoutes.push(routeRecord);
      } else {
        currentRoutes[editingIndex] = routeRecord;
      }

      return setConfigValueAtPath(current, ["routing"], {
        ...currentRouting,
        routes: currentRoutes,
      });
    });
    setError(null);
    setStatusMessage(
      editingIndex == null
        ? "已创建 routing route 草稿。"
        : `已更新 Route #${editingIndex + 1} 草稿。`,
    );
    return null;
  }

  function handleDeleteRoute(index: number) {
    if (!window.confirm(`确认删除 Route #${index + 1} 吗？`)) {
      return;
    }

    setDraftParsed((current: unknown) => {
      const currentRoutingValue = getConfigValueAtPath(current, ["routing"]);
      const currentRouting = isConfigRecord(currentRoutingValue)
        ? currentRoutingValue
        : {};
      const currentRoutes = Array.isArray(currentRouting.routes)
        ? [...currentRouting.routes]
        : [];
      currentRoutes.splice(index, 1);

      return setConfigValueAtPath(current, ["routing"], {
        ...currentRouting,
        routes: currentRoutes,
      });
    });
    setStatusMessage(`已从草稿中删除 Route #${index + 1}。`);
  }

  function handleMoveRoute(index: number, direction: "up" | "down") {
    setDraftParsed((current: unknown) => {
      const currentRoutingValue = getConfigValueAtPath(current, ["routing"]);
      const currentRouting = isConfigRecord(currentRoutingValue)
        ? currentRoutingValue
        : {};
      const currentRoutes = Array.isArray(currentRouting.routes)
        ? [...currentRouting.routes]
        : [];
      const targetIndex = direction === "up" ? index - 1 : index + 1;

      if (
        index < 0 ||
        index >= currentRoutes.length ||
        targetIndex < 0 ||
        targetIndex >= currentRoutes.length
      ) {
        return current;
      }

      const [route] = currentRoutes.splice(index, 1);
      currentRoutes.splice(targetIndex, 0, route);

      return setConfigValueAtPath(current, ["routing"], {
        ...currentRouting,
        routes: currentRoutes,
      });
    });
    setStatusMessage(
      direction === "up"
        ? `已上移 Route #${index + 1}。`
        : `已下移 Route #${index + 1}。`,
    );
  }

  function handleRateLimitConfigChange(
    nextRateLimitConfig: RuntimeRateLimitConfigSummary,
  ) {
    setDraftParsed((current: unknown) => {
      const currentRateLimitValue = getConfigValueAtPath(current, [
        "rate_limit",
      ]);
      const currentRateLimit = isConfigRecord(currentRateLimitValue)
        ? currentRateLimitValue
        : {};
      const currentDefaultLimits = isConfigRecord(
        currentRateLimit.default_limits,
      )
        ? currentRateLimit.default_limits
        : {};
      const currentGlobalLimits = isConfigRecord(currentRateLimit.global_limits)
        ? currentRateLimit.global_limits
        : {};

      return setConfigValueAtPath(current, ["rate_limit"], {
        ...currentRateLimit,
        enabled: nextRateLimitConfig.enabled,
        storage: nextRateLimitConfig.storage,
        algorithm: nextRateLimitConfig.algorithm,
        default_limits: {
          ...currentDefaultLimits,
          qps: parseLooseScalar(nextRateLimitConfig.defaultQps),
          tpm: parseLooseScalar(nextRateLimitConfig.defaultTpm),
          daily_tokens: parseLooseScalar(
            nextRateLimitConfig.defaultDailyTokens,
          ),
          monthly_tokens: parseLooseScalar(
            nextRateLimitConfig.defaultMonthlyTokens,
          ),
        },
        global_limits: {
          ...currentGlobalLimits,
          global_qps: parseLooseScalar(nextRateLimitConfig.globalQps),
          global_tpm: parseLooseScalar(nextRateLimitConfig.globalTpm),
          global_daily_tokens: parseLooseScalar(
            nextRateLimitConfig.globalDailyTokens,
          ),
          global_monthly_tokens: parseLooseScalar(
            nextRateLimitConfig.globalMonthlyTokens,
          ),
        },
      });
    });
  }

  function handleSaveApiKeyLimit(
    draft: RateLimitApiKeyDraftInput,
    editingIndex: number | null,
  ) {
    const nextLimit = buildRateLimitApiKeyRecordFromDraft(draft);
    const limitRecord = nextLimit.record;
    if (!limitRecord) {
      return nextLimit.error ?? "API Key 限流规则无效。";
    }

    setDraftParsed((current: unknown) => {
      const currentRateLimitValue = getConfigValueAtPath(current, [
        "rate_limit",
      ]);
      const currentRateLimit = isConfigRecord(currentRateLimitValue)
        ? currentRateLimitValue
        : {};
      const currentLimits = Array.isArray(currentRateLimit.api_key_limits)
        ? [...currentRateLimit.api_key_limits]
        : [];

      if (editingIndex == null) {
        currentLimits.push(limitRecord);
      } else {
        currentLimits[editingIndex] = limitRecord;
      }

      return setConfigValueAtPath(current, ["rate_limit"], {
        ...currentRateLimit,
        api_key_limits: currentLimits,
      });
    });
    setError(null);
    setStatusMessage(
      editingIndex == null
        ? "已创建 API Key 限流规则草稿。"
        : `已更新 API Key 规则 #${editingIndex + 1} 草稿。`,
    );
    return null;
  }

  function handleDeleteApiKeyLimit(index: number) {
    if (!window.confirm(`确认删除 API Key 规则 #${index + 1} 吗？`)) {
      return;
    }

    setDraftParsed((current: unknown) => {
      const currentRateLimitValue = getConfigValueAtPath(current, [
        "rate_limit",
      ]);
      const currentRateLimit = isConfigRecord(currentRateLimitValue)
        ? currentRateLimitValue
        : {};
      const currentLimits = Array.isArray(currentRateLimit.api_key_limits)
        ? [...currentRateLimit.api_key_limits]
        : [];
      currentLimits.splice(index, 1);

      return setConfigValueAtPath(current, ["rate_limit"], {
        ...currentRateLimit,
        api_key_limits: currentLimits,
      });
    });
    setStatusMessage(`已从草稿中删除 API Key 规则 #${index + 1}。`);
  }

  function handleSavePathLimit(
    draft: RateLimitPathDraftInput,
    previousPath: string | null,
  ) {
    const nextPathLimit = buildRateLimitPathRecordFromDraft(draft);
    const limitPath = nextPathLimit.path;
    const limitRecord = nextPathLimit.record;
    if (!limitPath || !limitRecord) {
      return nextPathLimit.error ?? "路径限流规则无效。";
    }
    if (
      pathLimits.some(
        (item) => item.path === limitPath && item.path !== previousPath,
      )
    ) {
      return `路径规则 "${limitPath}" 已存在。`;
    }

    setDraftParsed((current: unknown) => {
      const currentRateLimitValue = getConfigValueAtPath(current, [
        "rate_limit",
      ]);
      const currentRateLimit = isConfigRecord(currentRateLimitValue)
        ? currentRateLimitValue
        : {};
      const currentPathLimits = isConfigRecord(currentRateLimit.path_limits)
        ? { ...currentRateLimit.path_limits }
        : {};

      if (previousPath && previousPath !== limitPath) {
        delete currentPathLimits[previousPath];
      }
      currentPathLimits[limitPath] = limitRecord;

      return setConfigValueAtPath(current, ["rate_limit"], {
        ...currentRateLimit,
        path_limits: currentPathLimits,
      });
    });
    setError(null);
    setStatusMessage(
      previousPath
        ? `已更新路径限流规则 "${limitPath}"。`
        : `已创建路径限流规则 "${limitPath}"。`,
    );
    return null;
  }

  function handleDeletePathLimit(path: string) {
    if (!window.confirm(`确认删除路径规则 "${path}" 吗？`)) {
      return;
    }

    setDraftParsed((current: unknown) => {
      const currentRateLimitValue = getConfigValueAtPath(current, [
        "rate_limit",
      ]);
      const currentRateLimit = isConfigRecord(currentRateLimitValue)
        ? currentRateLimitValue
        : {};
      const currentPathLimits = isConfigRecord(currentRateLimit.path_limits)
        ? { ...currentRateLimit.path_limits }
        : {};
      delete currentPathLimits[path];

      return setConfigValueAtPath(current, ["rate_limit"], {
        ...currentRateLimit,
        path_limits: currentPathLimits,
      });
    });
    setStatusMessage(`已从草稿中删除路径限流规则 "${path}"。`);
  }

  function handleResourceManagerConfigChange(
    nextResourceManagerConfig: RuntimeResourceManagerConfigSummary,
  ) {
    setDraftParsed((current: unknown) => {
      const currentResourceManagerValue = getConfigValueAtPath(current, [
        "resource_manager",
      ]);
      const currentResourceManager = isConfigRecord(currentResourceManagerValue)
        ? currentResourceManagerValue
        : {};
      const currentHealthCheck = isConfigRecord(
        currentResourceManager.health_check,
      )
        ? currentResourceManager.health_check
        : {};

      return setConfigValueAtPath(current, ["resource_manager"], {
        ...currentResourceManager,
        enabled: nextResourceManagerConfig.enabled,
        default_group_algorithm:
          nextResourceManagerConfig.defaultGroupAlgorithm,
        default_provider_algorithm:
          nextResourceManagerConfig.defaultProviderAlgorithm,
        default_key_algorithm: nextResourceManagerConfig.defaultKeyAlgorithm,
        cross_provider_key_selection:
          nextResourceManagerConfig.crossProviderKeySelection,
        health_check: {
          ...currentHealthCheck,
          enabled: nextResourceManagerConfig.healthCheckEnabled,
          interval: nextResourceManagerConfig.healthCheckInterval,
          auto_recovery: nextResourceManagerConfig.healthCheckAutoRecovery,
          recovery_threshold: parseLooseScalar(
            nextResourceManagerConfig.healthCheckRecoveryThreshold,
          ),
        },
        enable_stats: nextResourceManagerConfig.enableStats,
        stats_retention: nextResourceManagerConfig.statsRetention,
      });
    });
  }

  function handleProviderQueueConfigChange(
    nextProviderQueueConfig: RuntimeProviderQueueConfigSummary,
  ) {
    setDraftParsed((current: unknown) => {
      const currentProviderQueueValue = getConfigValueAtPath(current, [
        "provider_queue",
      ]);
      const currentProviderQueue = isConfigRecord(currentProviderQueueValue)
        ? currentProviderQueueValue
        : {};
      const currentDefaultSlot = isConfigRecord(
        currentProviderQueue.default_slot,
      )
        ? currentProviderQueue.default_slot
        : {};
      const currentOverflow = isConfigRecord(currentProviderQueue.overflow)
        ? currentProviderQueue.overflow
        : {};
      const currentWaitHeartbeat = isConfigRecord(
        currentProviderQueue.wait_heartbeat,
      )
        ? currentProviderQueue.wait_heartbeat
        : {};

      return setConfigValueAtPath(current, ["provider_queue"], {
        ...currentProviderQueue,
        enabled: nextProviderQueueConfig.enabled,
        default_slot: {
          ...currentDefaultSlot,
          max_concurrency: parseLooseScalar(
            nextProviderQueueConfig.defaultMaxConcurrency,
          ),
          queue_size: parseLooseScalar(
            nextProviderQueueConfig.defaultQueueSize,
          ),
          queue_timeout: nextProviderQueueConfig.defaultQueueTimeout,
          overflow_strategy: nextProviderQueueConfig.defaultOverflowStrategy,
        },
        overflow: {
          ...currentOverflow,
          enabled: nextProviderQueueConfig.overflowEnabled,
          max_attempts: parseLooseScalar(
            nextProviderQueueConfig.overflowMaxAttempts,
          ),
          strategy: nextProviderQueueConfig.overflowStrategy,
        },
        wait_heartbeat: {
          ...currentWaitHeartbeat,
          enabled: nextProviderQueueConfig.waitHeartbeatEnabled,
          interval: nextProviderQueueConfig.waitHeartbeatInterval,
          comment: nextProviderQueueConfig.waitHeartbeatComment,
          max_wait_time: nextProviderQueueConfig.waitHeartbeatMaxWaitTime,
        },
      });
    });
  }

  function handleSaveProviderQueueProvider(
    draft: ProviderQueueProviderDraftInput,
    previousProvider: string | null,
  ) {
    const nextProvider = buildProviderQueueProviderRecordFromDraft(draft);
    const providerName = nextProvider.provider;
    const providerRecord = nextProvider.record;
    if (!providerName || !providerRecord) {
      return nextProvider.error ?? "Provider queue 覆盖配置无效。";
    }
    if (
      providerQueueProviders.some(
        (item) =>
          item.provider === providerName && item.provider !== previousProvider,
      )
    ) {
      return `Provider "${providerName}" 已存在队列覆盖。`;
    }

    setDraftParsed((current: unknown) => {
      const currentProviderQueueValue = getConfigValueAtPath(current, [
        "provider_queue",
      ]);
      const currentProviderQueue = isConfigRecord(currentProviderQueueValue)
        ? currentProviderQueueValue
        : {};
      const currentProviders = isConfigRecord(currentProviderQueue.providers)
        ? { ...currentProviderQueue.providers }
        : {};

      if (previousProvider && previousProvider !== providerName) {
        delete currentProviders[previousProvider];
      }
      currentProviders[providerName] = providerRecord;

      return setConfigValueAtPath(current, ["provider_queue"], {
        ...currentProviderQueue,
        providers: currentProviders,
      });
    });
    setError(null);
    setStatusMessage(
      previousProvider
        ? `已更新 provider queue 覆盖 "${providerName}"。`
        : `已创建 provider queue 覆盖 "${providerName}"。`,
    );
    return null;
  }

  function handleDeleteProviderQueueProvider(provider: string) {
    if (!window.confirm(`确认删除 provider queue 覆盖 "${provider}" 吗？`)) {
      return;
    }

    setDraftParsed((current: unknown) => {
      const currentProviderQueueValue = getConfigValueAtPath(current, [
        "provider_queue",
      ]);
      const currentProviderQueue = isConfigRecord(currentProviderQueueValue)
        ? currentProviderQueueValue
        : {};
      const currentProviders = isConfigRecord(currentProviderQueue.providers)
        ? { ...currentProviderQueue.providers }
        : {};
      delete currentProviders[provider];

      return setConfigValueAtPath(current, ["provider_queue"], {
        ...currentProviderQueue,
        providers: currentProviders,
      });
    });
    setStatusMessage(`已从草稿中删除 provider queue 覆盖 "${provider}"。`);
  }

  function handleConcurrencyConfigChange(
    nextConcurrencyConfig: RuntimeConcurrencyConfigSummary,
  ) {
    setDraftParsed((current: unknown) => {
      const currentConcurrencyValue = getConfigValueAtPath(current, [
        "concurrency",
      ]);
      const currentConcurrency = isConfigRecord(currentConcurrencyValue)
        ? currentConcurrencyValue
        : {};

      return setConfigValueAtPath(current, ["concurrency"], {
        ...currentConcurrency,
        enabled: nextConcurrencyConfig.enabled,
        max_concurrent_requests: parseLooseScalar(
          nextConcurrencyConfig.maxConcurrentRequests,
        ),
        queue_size: parseLooseScalar(nextConcurrencyConfig.queueSize),
        queue_timeout: nextConcurrencyConfig.queueTimeout,
      });
    });
  }

  function handleSaveConcurrencyProviderLimit(
    draft: ConcurrencyProviderLimitDraftInput,
    previousProvider: string | null,
  ) {
    const provider = draft.provider.trim();
    if (!provider) {
      return "Provider 名称不能为空。";
    }
    if (
      concurrencyProviderLimits.some(
        (item) =>
          item.provider === provider && item.provider !== previousProvider,
      )
    ) {
      return `Provider "${provider}" 已存在并发限制。`;
    }

    const limit = draft.limit.trim();
    if (!limit) {
      return "并发限制不能为空。";
    }

    setDraftParsed((current: unknown) => {
      const currentConcurrencyValue = getConfigValueAtPath(current, [
        "concurrency",
      ]);
      const currentConcurrency = isConfigRecord(currentConcurrencyValue)
        ? currentConcurrencyValue
        : {};
      const currentLimits = isConfigRecord(
        currentConcurrency.per_provider_limits,
      )
        ? { ...currentConcurrency.per_provider_limits }
        : {};

      if (previousProvider && previousProvider !== provider) {
        delete currentLimits[previousProvider];
      }
      currentLimits[provider] = parseLooseScalar(limit);

      return setConfigValueAtPath(current, ["concurrency"], {
        ...currentConcurrency,
        per_provider_limits: currentLimits,
      });
    });
    setError(null);
    setStatusMessage(
      previousProvider
        ? `已更新 provider 并发限制 "${provider}"。`
        : `已创建 provider 并发限制 "${provider}"。`,
    );
    return null;
  }

  function handleDeleteConcurrencyProviderLimit(provider: string) {
    if (!window.confirm(`确认删除 provider 并发限制 "${provider}" 吗？`)) {
      return;
    }

    setDraftParsed((current: unknown) => {
      const currentConcurrencyValue = getConfigValueAtPath(current, [
        "concurrency",
      ]);
      const currentConcurrency = isConfigRecord(currentConcurrencyValue)
        ? currentConcurrencyValue
        : {};
      const currentLimits = isConfigRecord(
        currentConcurrency.per_provider_limits,
      )
        ? { ...currentConcurrency.per_provider_limits }
        : {};
      delete currentLimits[provider];

      return setConfigValueAtPath(current, ["concurrency"], {
        ...currentConcurrency,
        per_provider_limits: currentLimits,
      });
    });
    setStatusMessage(`已从草稿中删除 provider 并发限制 "${provider}"。`);
  }

  function handleRetryConfigChange(nextRetryConfig: RuntimeRetryConfigSummary) {
    setDraftParsed((current: unknown) => {
      const currentRetryValue = getConfigValueAtPath(current, ["retry"]);
      const currentRetry = isConfigRecord(currentRetryValue)
        ? currentRetryValue
        : {};
      const currentRecovery = isConfigRecord(
        currentRetry.invalid_encrypted_content_recovery,
      )
        ? currentRetry.invalid_encrypted_content_recovery
        : {};
      const currentEnhancedStrategy = isConfigRecord(
        currentRetry.enhanced_strategy,
      )
        ? currentRetry.enhanced_strategy
        : {};

      return setConfigValueAtPath(current, ["retry"], {
        ...currentRetry,
        enabled: nextRetryConfig.enabled,
        default_max_retries: parseLooseScalar(
          nextRetryConfig.defaultMaxRetries,
        ),
        default_retry_delay_ms: parseLooseScalar(
          nextRetryConfig.defaultRetryDelayMs,
        ),
        default_backoff_multiplier: parseLooseScalar(
          nextRetryConfig.defaultBackoffMultiplier,
        ),
        invalid_encrypted_content_recovery: {
          ...currentRecovery,
          strip_client_state_once:
            nextRetryConfig.invalidEncryptedContentStripClientStateOnce,
        },
        enhanced_strategy: {
          ...currentEnhancedStrategy,
          enabled: nextRetryConfig.enhancedStrategyEnabled,
          secondary_threshold: parseLooseScalar(
            nextRetryConfig.enhancedStrategySecondaryThreshold,
          ),
          fallback_threshold: parseLooseScalar(
            nextRetryConfig.enhancedStrategyFallbackThreshold,
          ),
          primary_min_score: parseLooseScalar(
            nextRetryConfig.enhancedStrategyPrimaryMinScore,
          ),
          secondary_excluded_score: parseLooseScalar(
            nextRetryConfig.enhancedStrategySecondaryExcludedScore,
          ),
        },
      });
    });
  }

  function handleSaveRetryRule(
    draft: RetryRuleDraftInput,
    editingIndex: number | null,
  ) {
    const name = draft.name.trim();
    if (!name) {
      return "规则名称不能为空。";
    }
    if (
      retryRules.some(
        (rule) => rule.name === name && rule.index !== editingIndex,
      )
    ) {
      return `规则 "${name}" 已存在。`;
    }

    setDraftParsed((current: unknown) => {
      const currentRetryValue = getConfigValueAtPath(current, ["retry"]);
      const currentRetry = isConfigRecord(currentRetryValue)
        ? currentRetryValue
        : {};
      const currentRules = Array.isArray(currentRetry.rules)
        ? [...currentRetry.rules]
        : [];
      const currentRuleValue =
        editingIndex != null &&
        editingIndex >= 0 &&
        editingIndex < currentRules.length &&
        isConfigRecord(currentRules[editingIndex])
          ? currentRules[editingIndex]
          : {};
      const currentRule = isConfigRecord(currentRuleValue)
        ? currentRuleValue
        : {};
      const currentErrorCode = isConfigRecord(currentRule.error_code)
        ? { ...currentRule.error_code }
        : {};
      const currentKeyword = isConfigRecord(currentRule.keyword)
        ? { ...currentRule.keyword }
        : {};
      const currentStatusCode = isConfigRecord(currentRule.status_code)
        ? { ...currentRule.status_code }
        : {};
      const nextRule: Record<string, unknown> = {
        ...currentRule,
        name,
        description: draft.description.trim(),
        enabled: draft.enabled,
        max_retries: parseLooseScalar(draft.maxRetries),
        retry_delay_ms: parseLooseScalar(draft.retryDelayMs),
        backoff_multiplier: parseLooseScalar(draft.backoffMultiplier),
      };

      const errorCodeCodes = normalizeRetryMatchList(draft.errorCodeCodesText);
      if (errorCodeCodes.length > 0) {
        currentErrorCode.codes = errorCodeCodes;
      } else {
        delete currentErrorCode.codes;
      }
      if (draft.errorCodePattern.trim()) {
        currentErrorCode.pattern = draft.errorCodePattern.trim();
      } else {
        delete currentErrorCode.pattern;
      }
      if (Object.keys(currentErrorCode).length > 0) {
        nextRule.error_code = currentErrorCode;
      } else {
        delete nextRule.error_code;
      }

      const keywordValues = normalizeRetryMatchList(draft.keywordValuesText);
      const keywordPatterns = normalizeRetryMatchList(
        draft.keywordPatternsText,
      );
      if (keywordValues.length > 0) {
        currentKeyword.values = keywordValues;
      } else {
        delete currentKeyword.values;
      }
      if (keywordPatterns.length > 0) {
        currentKeyword.patterns = keywordPatterns;
      } else {
        delete currentKeyword.patterns;
      }
      if (
        keywordValues.length > 0 ||
        keywordPatterns.length > 0 ||
        draft.keywordCaseSensitive
      ) {
        currentKeyword.case_sensitive = draft.keywordCaseSensitive;
      } else {
        delete currentKeyword.case_sensitive;
      }
      if (Object.keys(currentKeyword).length > 0) {
        nextRule.keyword = currentKeyword;
      } else {
        delete nextRule.keyword;
      }

      if (draft.statusCodeRange.trim()) {
        currentStatusCode.range = draft.statusCodeRange.trim();
      } else {
        delete currentStatusCode.range;
      }
      if (Object.keys(currentStatusCode).length > 0) {
        nextRule.status_code = currentStatusCode;
      } else {
        delete nextRule.status_code;
      }

      if (editingIndex == null) {
        currentRules.push(nextRule);
      } else {
        currentRules[editingIndex] = nextRule;
      }

      return setConfigValueAtPath(current, ["retry"], {
        ...currentRetry,
        rules: currentRules,
      });
    });
    setError(null);
    setStatusMessage(
      editingIndex == null
        ? `已创建 retry 规则 "${name}"。`
        : `已更新 retry 规则 "${name}"。`,
    );
    return null;
  }

  function handleDeleteRetryRule(index: number) {
    if (!window.confirm(`确认删除 Retry 规则 #${index + 1} 吗？`)) {
      return;
    }

    setDraftParsed((current: unknown) => {
      const currentRetryValue = getConfigValueAtPath(current, ["retry"]);
      const currentRetry = isConfigRecord(currentRetryValue)
        ? currentRetryValue
        : {};
      const currentRules = Array.isArray(currentRetry.rules)
        ? [...currentRetry.rules]
        : [];
      currentRules.splice(index, 1);

      return setConfigValueAtPath(current, ["retry"], {
        ...currentRetry,
        rules: currentRules,
      });
    });
    setStatusMessage(`已从草稿中删除 Retry 规则 #${index + 1}。`);
  }

  function handleMoveRetryRule(index: number, direction: "up" | "down") {
    setDraftParsed((current: unknown) => {
      const currentRetryValue = getConfigValueAtPath(current, ["retry"]);
      const currentRetry = isConfigRecord(currentRetryValue)
        ? currentRetryValue
        : {};
      const currentRules = Array.isArray(currentRetry.rules)
        ? [...currentRetry.rules]
        : [];
      const targetIndex = direction === "up" ? index - 1 : index + 1;

      if (
        index < 0 ||
        index >= currentRules.length ||
        targetIndex < 0 ||
        targetIndex >= currentRules.length
      ) {
        return current;
      }

      const [rule] = currentRules.splice(index, 1);
      currentRules.splice(targetIndex, 0, rule);

      return setConfigValueAtPath(current, ["retry"], {
        ...currentRetry,
        rules: currentRules,
      });
    });
    setStatusMessage(
      direction === "up"
        ? `已上移 Retry 规则 #${index + 1}。`
        : `已下移 Retry 规则 #${index + 1}。`,
    );
  }

  function handleTransformerConfigChange(
    nextTransformerConfig: RuntimeTransformerConfigSummary,
  ) {
    setDraftParsed((current: unknown) => {
      const currentTransformerValue = getConfigValueAtPath(current, [
        "transformer",
      ]);
      const currentTransformer = isConfigRecord(currentTransformerValue)
        ? currentTransformerValue
        : {};

      return setConfigValueAtPath(current, ["transformer"], {
        ...currentTransformer,
        high_perf: nextTransformerConfig.highPerf,
        http_transform_stage_enabled:
          nextTransformerConfig.httpTransformStageEnabled,
        cache_adapters: nextTransformerConfig.cacheAdapters,
        stream_null_filter: nextTransformerConfig.streamNullFilter,
      });
    });
  }

  function handleSaveTransformerModifier(
    scope: TransformerModifierScope,
    draft: TransformerModifierDraftInput,
    editingIndex: number | null,
  ) {
    const nextModifier = buildTransformerModifierRecordFromDraft(draft, scope);
    const modifierRecord = nextModifier.record;
    if (!modifierRecord) {
      return nextModifier.error ?? "Transformer modifier 配置无效。";
    }

    setDraftParsed((current: unknown) => {
      const currentTransformerValue = getConfigValueAtPath(current, [
        "transformer",
      ]);
      const currentTransformer = isConfigRecord(currentTransformerValue)
        ? currentTransformerValue
        : {};
      const currentBodyModifiers = isConfigRecord(
        currentTransformer.body_modifiers,
      )
        ? currentTransformer.body_modifiers
        : {};
      const currentScopeItems = Array.isArray(currentBodyModifiers[scope])
        ? [...currentBodyModifiers[scope]]
        : [];

      if (editingIndex == null) {
        currentScopeItems.push(modifierRecord);
      } else {
        currentScopeItems[editingIndex] = modifierRecord;
      }

      return setConfigValueAtPath(current, ["transformer"], {
        ...currentTransformer,
        body_modifiers: {
          ...currentBodyModifiers,
          [scope]: currentScopeItems,
        },
      });
    });
    setError(null);
    setStatusMessage(
      editingIndex == null
        ? `已创建 ${scope === "request" ? "请求" : "响应"} transformer modifier。`
        : `已更新 ${scope === "request" ? "请求" : "响应"} transformer modifier #${editingIndex + 1}。`,
    );
    return null;
  }

  function handleDeleteTransformerModifier(
    scope: TransformerModifierScope,
    index: number,
  ) {
    if (
      !window.confirm(
        `确认删除${scope === "request" ? "请求" : "响应"} modifier #${index + 1} 吗？`,
      )
    ) {
      return;
    }

    setDraftParsed((current: unknown) => {
      const currentTransformerValue = getConfigValueAtPath(current, [
        "transformer",
      ]);
      const currentTransformer = isConfigRecord(currentTransformerValue)
        ? currentTransformerValue
        : {};
      const currentBodyModifiers = isConfigRecord(
        currentTransformer.body_modifiers,
      )
        ? currentTransformer.body_modifiers
        : {};
      const currentScopeItems = Array.isArray(currentBodyModifiers[scope])
        ? [...currentBodyModifiers[scope]]
        : [];
      currentScopeItems.splice(index, 1);

      return setConfigValueAtPath(current, ["transformer"], {
        ...currentTransformer,
        body_modifiers: {
          ...currentBodyModifiers,
          [scope]: currentScopeItems,
        },
      });
    });
    setStatusMessage(
      `已从草稿中删除${scope === "request" ? "请求" : "响应"} modifier #${index + 1}。`,
    );
  }

  function handleMoveTransformerModifier(
    scope: TransformerModifierScope,
    index: number,
    direction: "up" | "down",
  ) {
    setDraftParsed((current: unknown) => {
      const currentTransformerValue = getConfigValueAtPath(current, [
        "transformer",
      ]);
      const currentTransformer = isConfigRecord(currentTransformerValue)
        ? currentTransformerValue
        : {};
      const currentBodyModifiers = isConfigRecord(
        currentTransformer.body_modifiers,
      )
        ? currentTransformer.body_modifiers
        : {};
      const currentScopeItems = Array.isArray(currentBodyModifiers[scope])
        ? [...currentBodyModifiers[scope]]
        : [];
      const targetIndex = direction === "up" ? index - 1 : index + 1;

      if (
        index < 0 ||
        index >= currentScopeItems.length ||
        targetIndex < 0 ||
        targetIndex >= currentScopeItems.length
      ) {
        return current;
      }

      const [item] = currentScopeItems.splice(index, 1);
      currentScopeItems.splice(targetIndex, 0, item);

      return setConfigValueAtPath(current, ["transformer"], {
        ...currentTransformer,
        body_modifiers: {
          ...currentBodyModifiers,
          [scope]: currentScopeItems,
        },
      });
    });
    setStatusMessage(
      direction === "up"
        ? `已上移${scope === "request" ? "请求" : "响应"} modifier #${index + 1}。`
        : `已下移${scope === "request" ? "请求" : "响应"} modifier #${index + 1}。`,
    );
  }

  function handleMonitorConfigChange(
    nextMonitorConfig: RuntimeMonitorConfigSummary,
  ) {
    setDraftParsed((current: unknown) => {
      const currentMonitorValue = getConfigValueAtPath(current, ["monitor"]);
      const currentMonitor = isConfigRecord(currentMonitorValue)
        ? currentMonitorValue
        : {};
      const currentMetrics = isConfigRecord(currentMonitor.metrics)
        ? currentMonitor.metrics
        : {};
      const currentTracing = isConfigRecord(currentMonitor.tracing)
        ? currentMonitor.tracing
        : {};
      const currentAlert = isConfigRecord(currentMonitor.alert)
        ? currentMonitor.alert
        : {};
      const currentPprof = isConfigRecord(currentMonitor.pprof)
        ? currentMonitor.pprof
        : {};
      const currentMemory = isConfigRecord(currentMonitor.memory)
        ? currentMonitor.memory
        : {};

      return setConfigValueAtPath(current, ["monitor"], {
        ...currentMonitor,
        enabled: nextMonitorConfig.enabled,
        metrics: {
          ...currentMetrics,
          enabled: nextMonitorConfig.metricsEnabled,
          path: nextMonitorConfig.metricsPath,
          aggregation: nextMonitorConfig.metricsAggregation,
        },
        tracing: {
          ...currentTracing,
          enabled: nextMonitorConfig.tracingEnabled,
          sampler: parseLooseScalar(nextMonitorConfig.tracingSampler),
          exporter: nextMonitorConfig.tracingExporter,
          server_addr: nextMonitorConfig.tracingServerAddr,
        },
        alert: {
          ...currentAlert,
          enabled: nextMonitorConfig.alertEnabled,
          webhook_url: nextMonitorConfig.alertWebhookUrl,
          channels: normalizeMonitorChannels(
            nextMonitorConfig.alertChannelsText,
          ),
          min_threshold: parseLooseScalar(nextMonitorConfig.alertMinThreshold),
          severity: nextMonitorConfig.alertSeverity,
        },
        pprof: {
          ...currentPprof,
          enabled: nextMonitorConfig.pprofEnabled,
          listen_addr: nextMonitorConfig.pprofListenAddr,
          gc_interval: nextMonitorConfig.pprofGcInterval,
        },
        memory: {
          ...currentMemory,
          enabled: nextMonitorConfig.memoryEnabled,
          sample_interval: nextMonitorConfig.memorySampleInterval,
          alert_threshold_mb: parseLooseScalar(
            nextMonitorConfig.memoryAlertThresholdMb,
          ),
          leak_threshold_percent: parseLooseScalar(
            nextMonitorConfig.memoryLeakThresholdPercent,
          ),
        },
      });
    });
  }

  function handleWebsocketConfigChange(
    nextWebsocketConfig: RuntimeWebsocketConfigSummary,
  ) {
    setDraftParsed((current: unknown) => {
      const currentWebsocketValue = getConfigValueAtPath(current, [
        "websocket",
      ]);
      const currentWebsocket = isConfigRecord(currentWebsocketValue)
        ? currentWebsocketValue
        : {};
      const currentResponses = isConfigRecord(currentWebsocket.responses)
        ? currentWebsocket.responses
        : {};
      const currentResponsesCapacity = isConfigRecord(currentResponses.capacity)
        ? currentResponses.capacity
        : {};
      const currentResponsesMetrics = isConfigRecord(currentResponses.metrics)
        ? currentResponses.metrics
        : {};
      const currentRealtime = isConfigRecord(currentWebsocket.realtime)
        ? currentWebsocket.realtime
        : {};
      const currentRealtimeCapacity = isConfigRecord(currentRealtime.capacity)
        ? currentRealtime.capacity
        : {};
      const currentRealtimeMetrics = isConfigRecord(currentRealtime.metrics)
        ? currentRealtime.metrics
        : {};

      return setConfigValueAtPath(current, ["websocket"], {
        ...currentWebsocket,
        enabled: nextWebsocketConfig.enabled,
        responses: {
          ...currentResponses,
          ingress_enabled: nextWebsocketConfig.responsesIngressEnabled,
          http_bridge_enabled: nextWebsocketConfig.responsesHttpBridgeEnabled,
          compat_bridge_enabled:
            nextWebsocketConfig.responsesCompatBridgeEnabled,
          compat_bridge_source_protocols: normalizeWebsocketProtocols(
            nextWebsocketConfig.responsesCompatBridgeSourceProtocolsText,
          ),
          allow_passthrough_only:
            nextWebsocketConfig.responsesAllowPassthroughOnly,
          capacity: {
            ...currentResponsesCapacity,
            max_active_connections: parseLooseScalar(
              nextWebsocketConfig.responsesMaxActiveConnections,
            ),
          },
          metrics: {
            ...currentResponsesMetrics,
            enabled: nextWebsocketConfig.responsesMetricsEnabled,
            close_code_labels_enabled:
              nextWebsocketConfig.responsesCloseCodeLabelsEnabled,
          },
          connection_pooling_enabled:
            nextWebsocketConfig.responsesConnectionPoolingEnabled,
          affinity_ttl: nextWebsocketConfig.responsesAffinityTtl,
          pre_first_event_retry_once:
            nextWebsocketConfig.responsesPreFirstEventRetryOnce,
          handshake_max_retries: parseLooseScalar(
            nextWebsocketConfig.responsesHandshakeMaxRetries,
          ),
          failover_on_handshake_error:
            nextWebsocketConfig.responsesFailoverOnHandshakeError,
        },
        realtime: {
          ...currentRealtime,
          ingress_enabled: nextWebsocketConfig.realtimeIngressEnabled,
          capacity: {
            ...currentRealtimeCapacity,
            max_active_connections: parseLooseScalar(
              nextWebsocketConfig.realtimeMaxActiveConnections,
            ),
          },
          metrics: {
            ...currentRealtimeMetrics,
            enabled: nextWebsocketConfig.realtimeMetricsEnabled,
            close_code_labels_enabled:
              nextWebsocketConfig.realtimeCloseCodeLabelsEnabled,
          },
          handshake_max_retries: parseLooseScalar(
            nextWebsocketConfig.realtimeHandshakeMaxRetries,
          ),
          failover_on_handshake_error:
            nextWebsocketConfig.realtimeFailoverOnHandshakeError,
        },
      });
    });
  }

  function handleCircuitBreakerConfigChange(
    nextCircuitBreakerConfig: RuntimeCircuitBreakerConfigSummary,
  ) {
    setDraftParsed((current: unknown) => {
      const currentCircuitBreakerValue = getConfigValueAtPath(current, [
        "circuit_breaker",
      ]);
      const currentCircuitBreaker = isConfigRecord(currentCircuitBreakerValue)
        ? currentCircuitBreakerValue
        : {};

      return setConfigValueAtPath(current, ["circuit_breaker"], {
        ...currentCircuitBreaker,
        failure_threshold: parseLooseScalar(
          nextCircuitBreakerConfig.failureThreshold,
        ),
        failure_rate: parseLooseScalar(nextCircuitBreakerConfig.failureRate),
        sample_threshold: parseLooseScalar(
          nextCircuitBreakerConfig.sampleThreshold,
        ),
        window_duration: nextCircuitBreakerConfig.windowDuration,
        open_timeout: nextCircuitBreakerConfig.openTimeout,
        half_open_max_calls: parseLooseScalar(
          nextCircuitBreakerConfig.halfOpenMaxCalls,
        ),
      });
    });
  }

  return (
    <div className="space-y-6">
      <SettingsSection
        title={t("editor.title")}
        description={t("editor.description")}
      >
        <div className="rounded-[0.9rem] border border-[var(--border)] bg-[var(--surface-softer)] px-3 py-2.5">
          <div className="flex flex-wrap items-center justify-between gap-2.5">
            <div className="flex flex-wrap items-center gap-2">
              <Badge>{t("editor.independentBadge")}</Badge>
              <Badge>{getModeLabel(mode, translatedModeMenuEntries)}</Badge>
              {hasUnsavedChanges ? <Badge>{t("editor.unsavedBadge")}</Badge> : null}
            </div>
            <details className="rounded-[0.75rem] border border-[var(--border)] bg-[var(--surface-solid)] px-2.5 py-1.5">
              <summary className="flex cursor-pointer list-none items-center gap-2 text-xs text-[var(--muted-foreground)]">
                <InfoIcon size={14} className="text-[var(--accent-primary)]" />
                {t("editor.usage.title")}
              </summary>
              <div className="mt-2.5 max-w-[32rem] text-sm leading-6 text-[var(--muted-foreground)]">
                {t("editor.usage.body")}
              </div>
            </details>
          </div>
        </div>

        <div className="grid gap-2 md:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-12">
          <StatCard
            icon={BotIcon}
            label="Provider"
            value={`${providers.length}`}
            detail={
              defaultProvider
                ? `默认 provider: ${defaultProvider}`
                : "尚未设置默认 provider"
            }
          />
          <StatCard
            icon={RouteIcon}
            label="Provider Group"
            value={`${providerGroups.length}`}
            detail={
              providerGroups.length > 0
                ? `${providerGroups.reduce((total, group) => total + group.providerCount, 0)} 个成员引用`
                : "尚未创建 provider group"
            }
          />
          <StatCard
            icon={Settings2Icon}
            label="Auth"
            value={
              authConfig.accessAuthEnabled ? "Access 鉴权开" : "Access 鉴权关"
            }
            detail={
              authConfig.adminAuthEnabled
                ? "管理端鉴权已启用"
                : "管理端鉴权未启用"
            }
          />
          <StatCard
            icon={RouteIcon}
            label="Routing"
            value={`${routes.length} 条`}
            detail={
              routingConfig.strategy
                ? `strategy: ${routingConfig.strategy}`
                : "尚未设置 routing.strategy"
            }
          />
          <StatCard
            icon={GaugeIcon}
            label="Rate Limit"
            value={rateLimitConfig.enabled ? "已启用" : "已关闭"}
            detail={
              apiKeyLimits.length > 0 || pathLimits.length > 0
                ? `${apiKeyLimits.length} 条 API Key 规则 · ${pathLimits.length} 条路径规则`
                : "尚未配置覆盖规则"
            }
          />
          <StatCard
            icon={RouteIcon}
            label="Resource Manager"
            value={resourceManagerConfig.enabled ? "已启用" : "已关闭"}
            detail={
              resourceManagerConfig.enabled
                ? `${resourceManagerConfig.defaultGroupAlgorithm || "--"} / ${resourceManagerConfig.defaultProviderAlgorithm || "--"} / ${resourceManagerConfig.defaultKeyAlgorithm || "--"}`
                : "当前仍使用传统负载均衡路径"
            }
          />
          <StatCard
            icon={GaugeIcon}
            label="Provider Queue"
            value={providerQueueConfig.enabled ? "已启用" : "已关闭"}
            detail={
              providerQueueProviders.length > 0
                ? `${providerQueueProviders.length} 条 provider 覆盖 · 默认并发 ${providerQueueConfig.defaultMaxConcurrency || "--"}`
                : providerQueueConfig.waitHeartbeatEnabled
                  ? `wait_heartbeat ${providerQueueConfig.waitHeartbeatInterval || "--"}`
                  : "尚未配置 provider 级覆盖"
            }
          />
          <StatCard
            icon={GaugeIcon}
            label="Concurrency"
            value={concurrencyConfig.enabled ? "已启用" : "已关闭"}
            detail={
              concurrencyProviderLimits.length > 0
                ? `global ${concurrencyConfig.maxConcurrentRequests || "--"} · ${concurrencyProviderLimits.length} 条 provider 限制`
                : concurrencyConfig.queueTimeout
                  ? `queue_timeout ${concurrencyConfig.queueTimeout}`
                  : "尚未配置 provider 级并发限制"
            }
          />
          <StatCard
            icon={RefreshCcwIcon}
            label="Retry"
            value={retryConfig.enabled ? "已启用" : "已关闭"}
            detail={
              retryRules.length > 0
                ? `${retryRules.length} 条规则 · default ${retryConfig.defaultMaxRetries || "--"} 次`
                : "尚未配置 retry 规则"
            }
          />
          <StatCard
            icon={ActivityIcon}
            label="Monitor"
            value={monitorConfig.enabled ? "已启用" : "已关闭"}
            detail={
              monitorConfig.metricsEnabled || monitorConfig.tracingEnabled
                ? `metrics ${monitorConfig.metricsEnabled ? "开" : "关"} · tracing ${monitorConfig.tracingEnabled ? "开" : "关"}`
                : "metrics 与 tracing 当前都未启用"
            }
          />
          <StatCard
            icon={WifiIcon}
            label="WebSocket"
            value={websocketConfig.enabled ? "已启用" : "已关闭"}
            detail={
              websocketConfig.enabled
                ? `responses ${websocketConfig.responsesIngressEnabled ? "开" : "关"} · realtime ${websocketConfig.realtimeIngressEnabled ? "开" : "关"}`
                : "WebSocket 当前整体关闭"
            }
          />
          <StatCard
            icon={ActivityIcon}
            label="Circuit Breaker"
            value={circuitBreakerConfig.failureThreshold || "--"}
            detail={
              circuitBreakerConfig.openTimeout
                ? `open_timeout ${circuitBreakerConfig.openTimeout} · failure_rate ${circuitBreakerConfig.failureRate || "--"}`
                : "尚未配置熔断时间参数"
            }
          />
          <StatCard
            icon={Settings2Icon}
            label="Transformer"
            value={
              transformerConfig.httpTransformStageEnabled
                ? "HTTP Stage 开"
                : "HTTP Stage 关"
            }
            detail={
              requestTransformerModifiers.length +
                responseTransformerModifiers.length >
              0
                ? `${requestTransformerModifiers.length} 条 request modifier · ${responseTransformerModifiers.length} 条 response modifier`
                : transformerConfig.highPerf
                  ? "高性能转换模式已启用"
                  : "尚未配置 body modifier"
            }
          />
          <StatCard
            icon={HardDriveDownloadIcon}
            label="配置文件"
            value={document ? formatBytes(document.size_bytes) : "--"}
            detail={
              document?.updated_at
                ? `${document.path} · ${formatTimestamp(document.updated_at)}`
                : (document?.path ?? "尚未加载")
            }
          />
          <StatCard
            icon={RefreshCcwIcon}
            label="runtime-server"
            value={serviceStatus?.running ? "在线" : "离线"}
            detail={
              serviceStatus?.listen_addr || serviceError || "尚未返回监听信息"
            }
          />
        </div>

        <div className="sticky top-2 z-20 mt-2.5 rounded-[0.95rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
          <div className="flex flex-wrap items-center justify-between gap-2.5">
            <div className="flex flex-wrap items-center gap-2">
              <Badge>{hasUnsavedChanges ? "未保存草稿" : "已同步"}</Badge>
              <Badge>
                {mode === "source" ? t("editor.sourceFocus") : t("editor.structuredFocus")}
              </Badge>
              {previewDocument ? (
                <Badge>
                  {isPreviewFresh
                    ? `${t("editor.preview.latest")} ${previewDiff.length} 行`
                    : t("editor.preview.expired")}
                </Badge>
              ) : null}
              {previewRequiresRestart ? <Badge>{t("editor.preview.needsRestart")}</Badge> : null}
              {savedRequiresRestart && !hasUnsavedChanges ? (
                <Badge>{t("editor.preview.needsRestart")}</Badge>
              ) : null}
              {isModeSwitching ? <Badge>{t("editor.usage.title")}</Badge> : null}
              {mode === "providers" ? (
                <Badge>{`${enabledProviderCount} 已启用`}</Badge>
              ) : null}
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <Button
                variant="ghost"
                size="sm"
                onClick={() => void reloadDocument()}
                disabled={isLoading || isModeSwitching}
              >
                {isLoading ? (
                  <LoaderCircleIcon size={14} className="animate-spin" />
                ) : (
                  <RefreshCcwIcon size={14} />
                )}
                {t("editor.controls.reload")}
              </Button>
              <Button
                variant="secondary"
                size="sm"
                onClick={() => void generatePreview()}
                disabled={isPreviewLoading || isModeSwitching}
              >
                {isPreviewLoading ? (
                  <LoaderCircleIcon size={14} className="animate-spin" />
                ) : (
                  <FileTextIcon size={14} />
                )}
                {t("editor.controls.preview")}
              </Button>
              <Button
                size="sm"
                onClick={() => void saveDocument()}
                disabled={isSaving || isModeSwitching}
              >
                {isSaving ? (
                  <LoaderCircleIcon size={14} className="animate-spin" />
                ) : (
                  <HardDriveDownloadIcon size={14} />
                )}
                {t("editor.controls.save")}
              </Button>
              <Button
                variant={
                  savedRequiresRestart && !hasUnsavedChanges
                    ? "primary"
                    : "secondary"
                }
                size="sm"
                onClick={() => void restartService()}
                disabled={isRestarting || isModeSwitching}
              >
                {isRestarting ? (
                  <LoaderCircleIcon size={14} className="animate-spin" />
                ) : (
                  <RotateCcwIcon size={14} />
                )}
                {savedRequiresRestart && !hasUnsavedChanges
                  ? t("editor.controls.restartWithEffect")
                  : t("editor.controls.restart")}
              </Button>
            </div>
          </div>
          <div className="mt-2.5 text-xs text-[var(--muted-foreground)]">
            {t("editor.currentFocusPrefix")}
            {mode === "source"
              ? t("editor.sourceFocus")
              : getModeLabel(mode, translatedModeMenuEntries)}
          </div>
          {statusMessage ? (
            <div className="mt-2.5 rounded-[0.75rem] border border-[#8fd0c6]/24 bg-[#8fd0c6]/10 px-3 py-2.5 text-sm">
              {statusMessage}
            </div>
          ) : null}
          {error ? (
            <div className="mt-2.5 rounded-[0.75rem] border border-[#f59e7d]/24 bg-[#f59e7d]/10 px-3 py-2.5 text-sm">
              {error}
            </div>
          ) : null}
        </div>
      </SettingsSection>

      {impactDocument && shouldShowImpactPanel ? (
        <RuntimeImpactPanel
          document={impactDocument}
          serviceRunning={Boolean(serviceStatus?.running)}
          onRestart={() => {
            void restartService();
          }}
        />
      ) : null}

      <SettingsSection
        title={t("editor.panels.editorTitle")}
        description={t("editor.panels.editorDescription")}
      >
        <div className="grid gap-3 lg:grid-cols-[16rem_minmax(0,1fr)] xl:grid-cols-[17rem_minmax(0,1fr)]">
          <div className="min-w-0 space-y-2.5 lg:sticky lg:top-[8.5rem] lg:self-start">
            <ControlPanel
              title={t("editor.panels.modeTitle")}
              description={t("editor.panels.modeDescription")}
            >
              <div className="grid gap-2">
                {translatedModeMenuEntries.map((entry) => (
                  <MenuButton
                    key={entry.mode}
                    active={mode === entry.mode}
                    badge={getModeBadge(entry.mode)}
                    description={entry.description}
                    disabled={isModeSwitching}
                    icon={entry.icon}
                    label={entry.label}
                    onClick={() => {
                      void switchMode(entry.mode);
                    }}
                  />
                ))}
              </div>
            </ControlPanel>

            <ControlPanel
              title={t("editor.panels.summaryTitle")}
              description={t("editor.panels.summaryDescription")}
            >
              <div className="flex flex-wrap gap-2">
                <SummaryPill label="lines" value={`${draftLineCount}`} />
                <SummaryPill label="providers" value={`${providers.length}`} />
                <SummaryPill
                  label="groups"
                  value={`${providerGroups.length}`}
                />
                <SummaryPill
                  label="proxy"
                  value={summarizeRuntimeProxyConfig(proxyConfig)}
                />
                <SummaryPill label="routes" value={`${routes.length}`} />
                <SummaryPill
                  label="limits"
                  value={`${apiKeyLimits.length + pathLimits.length}`}
                />
                <SummaryPill
                  label="rm"
                  value={resourceManagerConfig.enabled ? "on" : "off"}
                />
                <SummaryPill
                  label="pq"
                  value={
                    providerQueueConfig.enabled
                      ? `${providerQueueProviders.length}`
                      : "off"
                  }
                />
                <SummaryPill
                  label="conc"
                  value={concurrencyConfig.enabled ? "on" : "off"}
                />
                <SummaryPill label="retry" value={`${retryRules.length}`} />
                <SummaryPill
                  label="monitor"
                  value={monitorConfig.enabled ? "on" : "off"}
                />
                <SummaryPill
                  label="ws"
                  value={websocketConfig.enabled ? "on" : "off"}
                />
                <SummaryPill
                  label="cb"
                  value={circuitBreakerConfig.failureThreshold || "--"}
                />
                <SummaryPill
                  label="xform"
                  value={`${requestTransformerModifiers.length + responseTransformerModifiers.length}`}
                />
                <SummaryPill
                  label="preview"
                  value={previewDocument ? "已生成" : "未生成"}
                />
              </div>
              <div className="mt-2.5 rounded-[0.75rem] border border-[var(--border)] bg-[var(--surface-solid)] px-3 py-2.5 text-sm leading-6 text-[var(--muted-foreground)]">
                {getModeSummary(mode)}
              </div>
            </ControlPanel>
          </div>

          <div className="min-w-0 space-y-3">
            {mode === "providers" ? (
              <Suspense
                fallback={
                  <ConfigEditorLoadingCard
                    label={t("runtimeConfig.editor.modes.providers.label")}
                  />
                }
              >
                <RuntimeProviderDomainEditor
                  defaultProvider={defaultProvider}
                  onDeleteProvider={handleDeleteProvider}
                  onSaveProvider={handleSaveProvider}
                  onSetDefaultProvider={handleSetDefaultProvider}
                  providers={providers}
                />
              </Suspense>
            ) : null}

            {mode === "providerGroups" ? (
              <Suspense
                fallback={
                  <ConfigEditorLoadingCard
                    label={t("runtimeConfig.editor.modes.providerGroups.label")}
                  />
                }
              >
                <RuntimeProviderGroupsDomainEditor
                  groups={providerGroups}
                  onDeleteGroup={handleDeleteProviderGroup}
                  onSaveGroup={handleSaveProviderGroup}
                  providers={providers}
                />
              </Suspense>
            ) : null}

            {mode === "networkProxy" ? (
              <Suspense
                fallback={
                  <ConfigEditorLoadingCard
                    label={t("runtimeConfig.editor.modes.networkProxy.label")}
                  />
                }
              >
                <RuntimeProxyDomainEditor
                  config={proxyConfig}
                  onChange={handleProxyConfigChange}
                />
              </Suspense>
            ) : null}

            {mode === "auth" ? (
              <Suspense
                fallback={
                  <ConfigEditorLoadingCard
                    label={t("runtimeConfig.editor.modes.auth.label")}
                  />
                }
              >
                <RuntimeAuthDomainEditor
                  authConfig={authConfig}
                  onChange={handleAuthChange}
                />
              </Suspense>
            ) : null}

            {mode === "routing" ? (
              <Suspense
                fallback={
                  <ConfigEditorLoadingCard
                    label={t("runtimeConfig.editor.modes.routing.label")}
                  />
                }
              >
                <RuntimeRoutingDomainEditor
                  availableGroups={providerGroups.map((group) => group.name)}
                  onChangeConfig={handleRoutingConfigChange}
                  onDeleteRoute={handleDeleteRoute}
                  onMoveRoute={handleMoveRoute}
                  onSaveRoute={handleSaveRoute}
                  routeConfig={routingConfig}
                  routes={routes}
                />
              </Suspense>
            ) : null}

            {mode === "rateLimit" ? (
              <Suspense
                fallback={
                  <ConfigEditorLoadingCard
                    label={t("runtimeConfig.editor.modes.rateLimit.label")}
                  />
                }
              >
                <RuntimeRateLimitDomainEditor
                  apiKeyLimits={apiKeyLimits}
                  onChangeConfig={handleRateLimitConfigChange}
                  onDeleteApiKeyLimit={handleDeleteApiKeyLimit}
                  onDeletePathLimit={handleDeletePathLimit}
                  onSaveApiKeyLimit={handleSaveApiKeyLimit}
                  onSavePathLimit={handleSavePathLimit}
                  pathLimits={pathLimits}
                  rateLimitConfig={rateLimitConfig}
                />
              </Suspense>
            ) : null}

            {mode === "resourceManager" ? (
              <Suspense
                fallback={
                  <ConfigEditorLoadingCard
                    label={t("runtimeConfig.editor.modes.resourceManager.label")}
                  />
                }
              >
                <RuntimeResourceManagerDomainEditor
                  config={resourceManagerConfig}
                  onChange={handleResourceManagerConfigChange}
                />
              </Suspense>
            ) : null}

            {mode === "providerQueue" ? (
              <Suspense
                fallback={
                  <ConfigEditorLoadingCard
                    label={t("runtimeConfig.editor.modes.providerQueue.label")}
                  />
                }
              >
                <RuntimeProviderQueueDomainEditor
                  config={providerQueueConfig}
                  onChangeConfig={handleProviderQueueConfigChange}
                  onDeleteProvider={handleDeleteProviderQueueProvider}
                  onSaveProvider={handleSaveProviderQueueProvider}
                  providers={providerQueueProviders}
                />
              </Suspense>
            ) : null}

            {mode === "concurrency" ? (
              <Suspense
                fallback={
                  <ConfigEditorLoadingCard
                    label={t("runtimeConfig.editor.modes.concurrency.label")}
                  />
                }
              >
                <RuntimeConcurrencyDomainEditor
                  config={concurrencyConfig}
                  onChange={handleConcurrencyConfigChange}
                  onDeleteProviderLimit={handleDeleteConcurrencyProviderLimit}
                  onSaveProviderLimit={handleSaveConcurrencyProviderLimit}
                  providerLimits={concurrencyProviderLimits}
                />
              </Suspense>
            ) : null}

            {mode === "retry" ? (
              <Suspense
                fallback={
                  <ConfigEditorLoadingCard
                    label={t("runtimeConfig.editor.modes.retry.label")}
                  />
                }
              >
                <RuntimeRetryDomainEditor
                  config={retryConfig}
                  onChangeConfig={handleRetryConfigChange}
                  onDeleteRule={handleDeleteRetryRule}
                  onMoveRule={handleMoveRetryRule}
                  onSaveRule={handleSaveRetryRule}
                  rules={retryRules}
                />
              </Suspense>
            ) : null}

            {mode === "monitor" ? (
              <Suspense
                fallback={
                  <ConfigEditorLoadingCard
                    label={t("runtimeConfig.editor.modes.monitor.label")}
                  />
                }
              >
                <RuntimeMonitorDomainEditor
                  config={monitorConfig}
                  onChange={handleMonitorConfigChange}
                />
              </Suspense>
            ) : null}

            {mode === "websocket" ? (
              <Suspense
                fallback={
                  <ConfigEditorLoadingCard
                    label={t("runtimeConfig.editor.modes.websocket.label")}
                  />
                }
              >
                <RuntimeWebsocketDomainEditor
                  config={websocketConfig}
                  onChange={handleWebsocketConfigChange}
                />
              </Suspense>
            ) : null}

            {mode === "circuitBreaker" ? (
              <Suspense
                fallback={
                  <ConfigEditorLoadingCard
                    label={t("runtimeConfig.editor.modes.circuitBreaker.label")}
                  />
                }
              >
                <RuntimeCircuitBreakerDomainEditor
                  config={circuitBreakerConfig}
                  onChange={handleCircuitBreakerConfigChange}
                />
              </Suspense>
            ) : null}

            {mode === "transformer" ? (
              <Suspense
                fallback={
                  <ConfigEditorLoadingCard
                    label={t("runtimeConfig.editor.modes.transformer.label")}
                  />
                }
              >
                <RuntimeTransformerDomainEditor
                  config={transformerConfig}
                  onChangeConfig={handleTransformerConfigChange}
                  onDeleteModifier={handleDeleteTransformerModifier}
                  onMoveModifier={handleMoveTransformerModifier}
                  onSaveModifier={handleSaveTransformerModifier}
                  requestModifiers={requestTransformerModifiers}
                  responseModifiers={responseTransformerModifiers}
                />
              </Suspense>
            ) : null}

            {mode === "source" ? (
              <>
                <div className="rounded-[0.9rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
                  <div className="flex flex-wrap items-center justify-between gap-3">
                    <div className="flex flex-wrap items-center gap-2">
                      <div className="text-base font-semibold text-[var(--foreground)]">
                        {t("editor.source.title")}
                      </div>
                      <SummaryPill label="lines" value={`${draftLineCount}`} />
                      <SummaryPill label="chars" value={`${draftRaw.length}`} />
                      <Badge>{t("editor.source.preserveComments")}</Badge>
                    </div>
                    <details className="rounded-[0.75rem] border border-[var(--border)] bg-[var(--surface-solid)] px-2.5 py-1.5">
                      <summary className="flex cursor-pointer list-none items-center gap-2 text-xs text-[var(--muted-foreground)]">
                        <InfoIcon
                          size={14}
                          className="text-[var(--accent-primary)]"
                        />
                        {t("editor.source.helpTitle")}
                      </summary>
                      <div className="mt-2.5 max-w-[30rem] text-sm leading-6 text-[var(--muted-foreground)]">
                        {t("editor.source.helpBody")}
                      </div>
                    </details>
                  </div>
                </div>

                <div className="rounded-[0.9rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
                  <textarea
                    className={cn(
                      editorControlClassName,
                      "min-h-[44rem] resize-y font-mono app-text-13 leading-6",
                    )}
                    spellCheck={false}
                    value={draftRaw}
                    onChange={(event) => setDraftRaw(event.target.value)}
                  />
                </div>
              </>
            ) : null}
          </div>
        </div>
      </SettingsSection>

      {previewDocument ? (
        <SettingsSection
          title={t("editor.preview.title")}
          description={t("editor.preview.description")}
        >
          <div className="rounded-[0.9rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
            <div className="mb-3 flex flex-wrap items-center justify-between gap-2.5">
              <div className="flex flex-wrap items-center gap-2">
                <SummaryPill label={t("editor.preview.added")} value={`${previewAdditions}`} />
                <SummaryPill label={t("editor.preview.removed")} value={`${previewRemovals}`} />
                <Badge>{isPreviewFresh ? t("editor.preview.latest") : t("editor.preview.expired")}</Badge>
                {previewDocument.restart_required ? (
                  <Badge>{t("editor.preview.needsRestart")}</Badge>
                ) : null}
              </div>
              <details className="rounded-[0.75rem] border border-[var(--border)] bg-[var(--surface-solid)] px-2.5 py-1.5">
                <summary className="flex cursor-pointer list-none items-center gap-2 text-xs text-[var(--muted-foreground)]">
                  <InfoIcon
                    size={14}
                    className="text-[var(--accent-primary)]"
                  />
                  {t("editor.preview.helpTitle")}
                </summary>
                <div className="mt-2.5 max-w-[30rem] text-sm leading-6 text-[var(--muted-foreground)]">
                  {isPreviewFresh
                    ? t("editor.preview.helpFresh")
                    : t("editor.preview.helpStale")}
                </div>
              </details>
            </div>
            <div className="max-h-[27rem] overflow-auto rounded-[0.75rem] border border-[var(--border)] bg-black/15">
              {previewDiff.map((line, index) => (
                <div
                  key={`${line.type}-${index}`}
                  className={cn(
                    "grid grid-cols-[3.5rem_3.5rem_1.5rem_minmax(0,1fr)] px-3 py-1.5 font-mono app-text-12",
                    line.type === "add"
                      ? "bg-[#8fd0c6]/10 text-[#d6fff6]"
                      : line.type === "remove"
                        ? "bg-[#f59e7d]/10 text-[#ffd9ce]"
                        : "text-[var(--foreground)]",
                  )}
                >
                  <div className="text-[var(--muted-foreground)]">
                    {line.beforeLine ?? ""}
                  </div>
                  <div className="text-[var(--muted-foreground)]">
                    {line.afterLine ?? ""}
                  </div>
                  <div>
                    {line.type === "add"
                      ? "+"
                      : line.type === "remove"
                        ? "-"
                        : " "}
                  </div>
                  <div className="whitespace-pre-wrap break-words">
                    {line.value || " "}
                  </div>
                </div>
              ))}
            </div>
          </div>
        </SettingsSection>
      ) : null}

      {hasUnsavedChanges ? (
        <div className="sticky bottom-3 z-20">
          <div className="rounded-[0.9rem] border border-[var(--accent-primary-border)] bg-[var(--accent-primary-soft)] px-3 py-2.5">
            <div className="flex flex-col gap-2.5 lg:flex-row lg:items-center lg:justify-between">
              <div className="flex flex-wrap items-center gap-2">
                <Badge>{t("editor.sticky.unsaved")}</Badge>
                <div className="text-sm text-[var(--muted-foreground)]">{t("editor.sticky.hint")}</div>
              </div>
              <div className="flex flex-wrap items-center gap-2">
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={() => void generatePreview()}
                  disabled={isPreviewLoading || isModeSwitching}
                >
                  {isPreviewLoading ? (
                    <LoaderCircleIcon size={14} className="animate-spin" />
                  ) : (
                    <FileTextIcon size={14} />
                  )}
                  {t("editor.sticky.previewButton")}
                </Button>
                <Button
                  variant={canSaveAndRestart ? "secondary" : "primary"}
                  size="sm"
                  onClick={() => void saveDocument()}
                  disabled={isSaving || isModeSwitching}
                >
                  {isSaving ? (
                    <LoaderCircleIcon size={14} className="animate-spin" />
                  ) : (
                    <HardDriveDownloadIcon size={14} />
                  )}
                  {t("editor.sticky.saveButton")}
                </Button>
                {canSaveAndRestart ? (
                  <Button
                    size="sm"
                    onClick={() => void saveAndRestartDocument()}
                    disabled={isSaving || isRestarting || isModeSwitching}
                  >
                    {isSaving || isRestarting ? (
                      <LoaderCircleIcon size={14} className="animate-spin" />
                    ) : (
                      <RotateCcwIcon size={14} />
                    )}
                    {t("editor.sticky.saveAndRestartButton")}
                  </Button>
                ) : null}
              </div>
            </div>
          </div>
        </div>
      ) : null}
    </div>
  );
}

function ConfigEditorLoadingCard({ label }: { label: string }) {
  return (
    <div className="rounded-[0.9rem] border border-[var(--border)] bg-[var(--surface-softer)] p-4">
      <div className="text-sm font-semibold text-[var(--foreground)]">
        正在加载 {label} 编辑器…
      </div>
      <div className="mt-2 text-sm leading-6 text-[var(--muted-foreground)]">
        当前只会按需加载正在查看的配置域，未打开的编辑器不会进入首批页面资源。
      </div>
    </div>
  );
}

function upsertNamedArrayRecord(
  root: unknown,
  key: string,
  previousName: string | null,
  nextName: string,
  record: Record<string, unknown>,
) {
  const rootRecord = isConfigRecord(root) ? root : {};
  const currentItems = Array.isArray(rootRecord[key]) ? rootRecord[key] : [];
  let replaced = false;

  const nextItems = currentItems
    .map((item) => {
      if (
        previousName &&
        isConfigRecord(item) &&
        typeof item.name === "string" &&
        item.name === previousName
      ) {
        replaced = true;
        return record;
      }
      return item;
    })
    .filter((item) => {
      if (
        previousName &&
        previousName !== nextName &&
        isConfigRecord(item) &&
        typeof item.name === "string" &&
        item.name === nextName
      ) {
        return false;
      }
      return true;
    });

  if (!replaced) {
    nextItems.push(record);
  }

  return setConfigValueAtPath(root, [key], nextItems);
}

function removeNamedArrayRecord(root: unknown, key: string, name: string) {
  const rootRecord = isConfigRecord(root) ? root : {};
  const currentItems = Array.isArray(rootRecord[key]) ? rootRecord[key] : [];
  return setConfigValueAtPath(
    root,
    [key],
    currentItems.filter(
      (item) =>
        !(
          isConfigRecord(item) &&
          typeof item.name === "string" &&
          item.name === name
        ),
    ),
  );
}
