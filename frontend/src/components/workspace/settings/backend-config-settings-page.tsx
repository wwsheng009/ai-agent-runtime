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

type Translator = any;

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
    labelKey: "editor.modes.providers.label",
    descriptionKey: "editor.modes.providers.description",
    icon: BotIcon,
  },
  {
    mode: "providerGroups",
    labelKey: "editor.modes.providerGroups.label",
    descriptionKey: "editor.modes.providerGroups.description",
    icon: RouteIcon,
  },
  {
    mode: "networkProxy",
    labelKey: "editor.modes.networkProxy.label",
    descriptionKey: "editor.modes.networkProxy.description",
    icon: RouteIcon,
  },
  {
    mode: "auth",
    labelKey: "editor.modes.auth.label",
    descriptionKey: "editor.modes.auth.description",
    icon: Settings2Icon,
  },
  {
    mode: "routing",
    labelKey: "editor.modes.routing.label",
    descriptionKey: "editor.modes.routing.description",
    icon: RouteIcon,
  },
  {
    mode: "rateLimit",
    labelKey: "editor.modes.rateLimit.label",
    descriptionKey: "editor.modes.rateLimit.description",
    icon: GaugeIcon,
  },
  {
    mode: "resourceManager",
    labelKey: "editor.modes.resourceManager.label",
    descriptionKey: "editor.modes.resourceManager.description",
    icon: RouteIcon,
  },
  {
    mode: "providerQueue",
    labelKey: "editor.modes.providerQueue.label",
    descriptionKey: "editor.modes.providerQueue.description",
    icon: GaugeIcon,
  },
  {
    mode: "concurrency",
    labelKey: "editor.modes.concurrency.label",
    descriptionKey: "editor.modes.concurrency.description",
    icon: GaugeIcon,
  },
  {
    mode: "retry",
    labelKey: "editor.modes.retry.label",
    descriptionKey: "editor.modes.retry.description",
    icon: RefreshCcwIcon,
  },
  {
    mode: "monitor",
    labelKey: "editor.modes.monitor.label",
    descriptionKey: "editor.modes.monitor.description",
    icon: ActivityIcon,
  },
  {
    mode: "websocket",
    labelKey: "editor.modes.websocket.label",
    descriptionKey: "editor.modes.websocket.description",
    icon: WifiIcon,
  },
  {
    mode: "circuitBreaker",
    labelKey: "editor.modes.circuitBreaker.label",
    descriptionKey: "editor.modes.circuitBreaker.description",
    icon: ActivityIcon,
  },
  {
    mode: "transformer",
    labelKey: "editor.modes.transformer.label",
    descriptionKey: "editor.modes.transformer.description",
    icon: Settings2Icon,
  },
  {
    mode: "source",
    labelKey: "editor.modes.source.label",
    descriptionKey: "editor.modes.source.description",
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

function formatRuntimeProxySummary(
  config: RuntimeProxyConfigSummary,
  t: Translator,
  tCommon: Translator,
) {
  if (!hasRuntimeProxyConfig(config)) {
    return t("editor.proxySummary.fallback");
  }

  const parts: string[] = [];
  if (config.http.trim()) {
    parts.push("HTTP");
  }
  if (config.https.trim()) {
    parts.push("HTTPS");
  }
  if (config.noProxy.trim()) {
    parts.push("NO_PROXY");
  }

  if (parts.length === 0) {
    return config.enabled
      ? tCommon("states.enabled")
      : tCommon("states.disabled");
  }

  return parts.join(" + ");
}

function formatTimestamp(value: string | undefined, locale: string) {
  if (!value) {
    return "";
  }
  return new Intl.DateTimeFormat(locale, {
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

function buildSaveStatusMessage(
  t: Translator,
  document: RuntimeConfigDocument,
) {
  const targetPath = document.path?.trim() || t("editor.validation.defaultPath");
  const impact = document.runtime_impact;
  const appliedCount = countImpactPaths(impact?.applied_paths);
  const hotReloadCount = countImpactPaths(impact?.hot_reload_paths);
  const restartCount = countImpactPaths(impact?.restart_required_paths);
  const inactiveCount = countImpactPaths(impact?.inactive_paths);

  const parts: string[] = [];
  if (appliedCount > 0) {
    parts.push(t("editor.saveStatus.applied", { count: appliedCount }));
  } else if (hotReloadCount > 0) {
    parts.push(t("editor.saveStatus.hotReload", { count: hotReloadCount }));
  }
  if (restartCount > 0) {
    parts.push(t("editor.saveStatus.restart", { count: restartCount }));
  }
  if (inactiveCount > 0) {
    parts.push(t("editor.saveStatus.inactive", { count: inactiveCount }));
  }

  if (parts.length === 0) {
    return t("editor.saveStatus.basic", { targetPath });
  }
  return t("editor.saveStatus.detailed", {
    details: parts.join(t("editor.saveStatus.separator")),
    targetPath,
  });
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
  const { t } = useTranslation("runtimeConfig");
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
        <Badge className={toneClassName}>
          {t("editor.counts.pathCount", { count: countImpactPaths(paths) })}
        </Badge>
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
              {t("editor.counts.hiddenCount", { count: hiddenCount })}
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
  const { t } = useTranslation("runtimeConfig");
  const impact = document.runtime_impact;
  if (!impact || countImpactPaths(impact.changed_paths) === 0) {
    return null;
  }

  const changedCount = countImpactPaths(impact.changed_paths);
  const hotReloadCount = countImpactPaths(impact.hot_reload_paths);
  const restartCount = countImpactPaths(impact.restart_required_paths);
  const inactiveCount = countImpactPaths(impact.inactive_paths);
  const appliedCount = countImpactPaths(impact.applied_paths);
  const previewWarning = t("editor.validation.previewWarning");
  const warnings = (document.warnings ?? []).filter(
    (warning) => warning !== previewWarning,
  );
  const isPreview = document.updated_at == null;

  return (
    <SettingsSection
      title={
        isPreview
          ? t("editor.impact.previewTitle")
          : t("editor.impact.runtimeTitle")
      }
      description={
        isPreview
          ? t("editor.impact.previewDescription")
          : t("editor.impact.runtimeDescription")
      }
    >
      <div className="rounded-[0.95rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
        <div className="flex flex-wrap items-center justify-between gap-2.5">
          <div className="flex flex-wrap items-center gap-2">
            <Badge>{isPreview ? t("editor.impact.previewBadge") : t("editor.impact.savedBadge")}</Badge>
            <Badge>{t("editor.impact.changedCount", { count: changedCount })}</Badge>
            <Badge>
              {document.restart_required
                ? t("editor.impact.needsRestart")
                : t("editor.impact.noRestart")}
            </Badge>
            {appliedCount > 0 ? (
              <Badge>{t("editor.impact.appliedCount", { count: appliedCount })}</Badge>
            ) : null}
          </div>
          {document.restart_required ? (
            <Button
              variant={serviceRunning ? "primary" : "secondary"}
              size="sm"
              onClick={onRestart}
            >
              <RotateCcwIcon size={14} />
              {t("editor.impact.restartToApply")}
            </Button>
          ) : null}
        </div>

        <div className="mt-3 grid gap-2.5 md:grid-cols-2 xl:grid-cols-4">
          <ImpactStat
            label={t("editor.impact.stats.changed")}
            value={`${changedCount}`}
            detail={t("editor.impact.details.changed")}
            accentClassName="text-[var(--foreground)]"
          />
          <ImpactStat
            label={t("editor.impact.stats.hotReload")}
            value={`${hotReloadCount}`}
            detail={
              appliedCount > 0
                ? t("editor.impact.details.hotReloadApplied")
                : t("editor.impact.details.hotReload")
            }
            accentClassName="text-[#8fd0c6]"
          />
          <ImpactStat
            label={t("editor.impact.stats.restart")}
            value={`${restartCount}`}
            detail={t("editor.impact.details.restart")}
            accentClassName="text-[#f59e7d]"
          />
          <ImpactStat
            label={t("editor.impact.stats.inactive")}
            value={`${inactiveCount}`}
            detail={t("editor.impact.details.inactive")}
            accentClassName="text-[#e7d58c]"
          />
        </div>

        <div className="mt-3 grid gap-3 xl:grid-cols-2">
          <ImpactPathList
            title={t("editor.impact.paths.applied")}
            paths={impact.applied_paths}
            emptyText={t("editor.impact.paths.emptyApplied")}
            toneClassName="border-[#8fd0c6]/24 bg-[#8fd0c6]/10 text-[#d6fff6]"
          />
          <ImpactPathList
            title={t("editor.impact.paths.restart")}
            paths={impact.restart_required_paths}
            emptyText={t("editor.impact.paths.emptyRestart")}
            toneClassName="border-[#f59e7d]/24 bg-[#f59e7d]/10 text-[#ffd9ce]"
          />
          <ImpactPathList
            title={t("editor.impact.paths.hotReload")}
            paths={impact.hot_reload_paths}
            emptyText={t("editor.impact.paths.emptyHotReload")}
            toneClassName="border-[#8fd0c6]/24 bg-[#8fd0c6]/10 text-[#d6fff6]"
          />
          <ImpactPathList
            title={t("editor.impact.paths.inactive")}
            paths={impact.inactive_paths}
            emptyText={t("editor.impact.paths.emptyInactive")}
            toneClassName="border-[#e7d58c]/24 bg-[#e7d58c]/10 text-[#fff3c4]"
          />
        </div>

        {warnings.length > 0 ? (
          <div className="mt-3 rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-solid)] p-3">
            <div className="text-sm font-semibold text-[var(--foreground)]">
              {t("editor.impact.warningsTitle")}
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
  const { i18n, t } = useTranslation("runtimeConfig");
  const { t: tCommon } = useTranslation("common");
  const resolvedLocale = i18n.resolvedLanguage?.startsWith("zh")
    ? "zh-CN"
    : "en-US";
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
        description: t(entry.descriptionKey as never) as string,
        label: t(entry.labelKey as never) as string,
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
        return t("editor.counts.providers", { count: providers.length });
      case "providerGroups":
        return t("editor.counts.providerGroups", {
          count: providerGroups.length,
        });
      case "networkProxy":
        return formatRuntimeProxySummary(proxyConfig, t, tCommon);
      case "routing":
        return t("editor.counts.routes", { count: routes.length });
      case "rateLimit":
        return t("editor.counts.rules", {
          count: apiKeyLimits.length + pathLimits.length,
        });
      case "resourceManager":
        return resourceManagerConfig.enabled
          ? tCommon("states.enabled")
          : tCommon("states.disabled");
      case "providerQueue":
        return providerQueueConfig.enabled
          ? t("editor.counts.providerQueueProviders", {
              count: providerQueueProviders.length,
            })
          : tCommon("states.disabled");
      case "concurrency":
        return concurrencyConfig.enabled
          ? t("editor.counts.concurrencyProviders", {
              count: concurrencyProviderLimits.length,
            })
          : tCommon("states.disabled");
      case "retry":
        return retryConfig.enabled
          ? t("editor.counts.retryRules", { count: retryRules.length })
          : tCommon("states.disabled");
      case "monitor":
        return monitorConfig.enabled
          ? tCommon("states.enabled")
          : tCommon("states.disabled");
      case "websocket":
        return websocketConfig.enabled
          ? tCommon("states.enabled")
          : tCommon("states.disabled");
      case "circuitBreaker":
        return circuitBreakerConfig.failureThreshold || "--";
      case "transformer":
        return t("editor.counts.transformerModifiers", {
          count:
            requestTransformerModifiers.length +
            responseTransformerModifiers.length,
        });
      case "auth":
        return authConfig.accessAuthEnabled
          ? tCommon("states.enabled")
          : tCommon("states.disabled");
      case "source":
      default:
        return t("editor.counts.lines", { count: draftLineCount });
    }
  }

  function getModeSummary(modeValue: EditorMode) {
    switch (modeValue) {
      case "providers":
        return t("editor.summary.providers");
      case "providerGroups":
        return t("editor.summary.groups");
      case "networkProxy":
        return t("editor.summary.proxy");
      case "routing":
        return t("editor.summary.routes");
      case "rateLimit":
        return t("editor.summary.limits");
      case "resourceManager":
        return t("editor.summary.resourceManager");
      case "providerQueue":
        return t("editor.summary.providerQueue");
      case "concurrency":
        return t("editor.summary.concurrency");
      case "retry":
        return t("editor.summary.retry");
      case "monitor":
        return t("editor.summary.monitor");
      case "websocket":
        return t("editor.summary.websocket");
      case "circuitBreaker":
        return t("editor.summary.circuitBreaker");
      case "transformer":
        return t("editor.summary.transformer");
      case "auth":
        return t("editor.modes.auth.description");
      case "source":
      default:
        return t("editor.modes.source.label");
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
                : t("editor.messages.loadServiceStatusFailed"),
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
          loadError instanceof Error
            ? loadError.message
            : t("editor.messages.loadBackendConfigFailed"),
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
          ? t("editor.messages.syncedToSource")
          : t("editor.messages.syncedToStructured"),
      );
    } catch (switchError) {
      setError(
        switchError instanceof Error
          ? switchError.message
          : t("editor.messages.switchSyncFailed"),
      );
    } finally {
      setIsModeSwitching(false);
    }
  }

  async function reloadDocument() {
    if (
      hasUnsavedChanges &&
      !window.confirm(t("editor.messages.reloadConfirm"))
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
      setStatusMessage(t("editor.messages.reloadedFromDisk"));
    } catch (loadError) {
      setError(
        loadError instanceof Error
          ? loadError.message
          : t("editor.messages.reloadFailed"),
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
        setStatusMessage(buildSaveStatusMessage(t, nextDocument));
      }
      return nextDocument;
    } catch (saveError) {
      setError(
        saveError instanceof Error ? saveError.message : t("editor.messages.saveFailed"),
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
        previewError instanceof Error
          ? previewError.message
          : t("editor.messages.previewFailed"),
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
      !window.confirm(t("editor.messages.restartConfirm"))
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
            setStatusMessage(t("editor.messages.runtimeServerReconnected"));
            return true;
          }
        } catch {
          // ignore poll errors while service restarts
        }
      }
      setStatusMessage(t("editor.messages.restartRequested"));
      return true;
    } catch (restartError) {
      setServiceError(
        restartError instanceof Error
          ? restartError.message
          : t("editor.messages.restartFailed"),
      );
      return false;
    } finally {
      setIsRestarting(false);
    }
  }

  async function saveAndRestartDocument() {
    if (
      !window.confirm(t("editor.messages.saveAndRestartConfirm"))
    ) {
      return;
    }

    const nextDocument = await saveDocument({ suppressStatusMessage: true });
    if (!nextDocument) {
      return;
    }

    if (!nextDocument.restart_required) {
      setStatusMessage(buildSaveStatusMessage(t, nextDocument));
      return;
    }

    const restarted = await restartService({
      skipConfirm: true,
      refreshDocument: true,
    });
    if (restarted) {
      setStatusMessage(t("editor.messages.saveAndRestartDone"));
    }
  }

  function handleSetDefaultProvider(name: string) {
    setDraftParsed((current: unknown) =>
      setConfigValueAtPath(current, ["providers", "default_provider"], name),
    );
    setStatusMessage(t("editor.messages.defaultProviderSet", { name }));
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
        ? t("editor.messages.globalProxyUpdated", {
            summary: formatRuntimeProxySummary(nextProxyConfig, t, tCommon),
          })
        : t("editor.messages.globalProxyCleared"),
    );
  }

  function handleSaveProvider(
    draft: ProviderDraftInput,
    previousName: string | null,
  ) {
    const name = draft.name.trim();
    if (!name) {
      return t("editor.validation.providerNameRequired");
    }
    if (
      providers.some(
        (provider) => provider.name === name && provider.name !== previousName,
      )
    ) {
      return t("editor.validation.providerExists", { name });
    }

    const nextProvider = buildProviderRecordFromDraft(draft);
    if (!nextProvider.record) {
      return nextProvider.error ?? t("editor.validation.providerInvalid");
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
        ? t("editor.messages.providerUpdated", { name })
        : t("editor.messages.providerCreated", { name }),
    );
    return null;
  }

  function handleDeleteProvider(name: string) {
    const relatedGroups = providerGroups.filter((group) =>
      group.providers.some((provider) => provider.name === name),
    );
    const relatedHint =
      relatedGroups.length > 0
        ? t("editor.messages.relatedProviderGroups", {
            count: relatedGroups.length,
          })
        : "";
    if (!window.confirm(t("editor.messages.confirmDeleteProvider", { name, relatedHint }))) {
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
    setStatusMessage(t("editor.messages.providerDeleted", { name }));
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
      return t("editor.validation.providerGroupExists", { name });
    }

    const nextGroup = buildProviderGroupRecordFromDraft(draft);
    const groupRecord = nextGroup.record;
    if (!groupRecord) {
      return nextGroup.error ?? t("editor.validation.providerGroupInvalid");
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
        ? t("editor.messages.providerGroupUpdated", { name })
        : t("editor.messages.providerGroupCreated", { name }),
    );
    return null;
  }

  function handleDeleteProviderGroup(name: string) {
    if (!window.confirm(t("editor.messages.confirmDeleteProviderGroup", { name }))) {
      return;
    }
    setDraftParsed((current: unknown) =>
      removeNamedArrayRecord(current, "provider_groups", name),
    );
    setStatusMessage(t("editor.messages.providerGroupDeleted", { name }));
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
      return nextRoute.error ?? t("editor.validation.routeInvalid");
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
        ? t("editor.messages.routeCreated")
        : t("editor.messages.routeUpdated", { index: String(editingIndex + 1) }),
    );
    return null;
  }

  function handleDeleteRoute(index: number) {
    if (!window.confirm(t("editor.messages.confirmDeleteRoute", { index: String(index + 1) }))) {
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
    setStatusMessage(t("editor.messages.routeDeleted", { index: String(index + 1) }));
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
        ? t("editor.messages.routeMovedUp", { index: String(index + 1) })
        : t("editor.messages.routeMovedDown", { index: String(index + 1) }),
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
      return nextLimit.error ?? t("editor.validation.apiKeyLimitInvalid");
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
        ? t("editor.messages.apiKeyLimitCreated")
        : t("editor.messages.apiKeyLimitUpdated", { index: String(editingIndex + 1) }),
    );
    return null;
  }

  function handleDeleteApiKeyLimit(index: number) {
    if (
      !window.confirm(
        t("editor.messages.confirmDeleteApiKeyLimit", { index: String(index + 1) }),
      )
    ) {
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
    setStatusMessage(
      t("editor.messages.apiKeyLimitDeleted", { index: String(index + 1) }),
    );
  }

  function handleSavePathLimit(
    draft: RateLimitPathDraftInput,
    previousPath: string | null,
  ) {
    const nextPathLimit = buildRateLimitPathRecordFromDraft(draft);
    const limitPath = nextPathLimit.path;
    const limitRecord = nextPathLimit.record;
    if (!limitPath || !limitRecord) {
      return nextPathLimit.error ?? t("editor.validation.pathLimitInvalid");
    }
    if (
      pathLimits.some(
        (item) => item.path === limitPath && item.path !== previousPath,
      )
    ) {
      return t("editor.validation.pathLimitExists", { path: limitPath });
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
        ? t("editor.messages.pathLimitUpdated", { path: limitPath })
        : t("editor.messages.pathLimitCreated", { path: limitPath }),
    );
    return null;
  }

  function handleDeletePathLimit(path: string) {
    if (!window.confirm(t("editor.messages.confirmDeletePathLimit", { path }))) {
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
    setStatusMessage(t("editor.messages.pathLimitDeleted", { path }));
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
      return nextProvider.error ?? t("editor.validation.providerQueueInvalid");
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
        ? t("editor.messages.providerQueueUpdated", { provider: providerName })
        : t("editor.messages.providerQueueCreated", {
            provider: providerName,
          }),
    );
    return null;
  }

  function handleDeleteProviderQueueProvider(provider: string) {
    if (
      !window.confirm(
        t("editor.messages.confirmDeleteProviderQueueProvider", {
          provider,
        }),
      )
    ) {
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
    setStatusMessage(t("editor.messages.providerQueueDeleted", { provider }));
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
      return t("editor.validation.providerNameRequired");
    }
    if (
      concurrencyProviderLimits.some(
        (item) =>
          item.provider === provider && item.provider !== previousProvider,
      )
    ) {
      return t("editor.validation.concurrencyProviderExists", {
        provider,
      });
    }

    const limit = draft.limit.trim();
    if (!limit) {
      return t("editor.validation.concurrencyLimitRequired");
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
        ? t("editor.messages.concurrencyLimitUpdated", {
            provider,
          })
        : t("editor.messages.concurrencyLimitCreated", {
            provider,
          }),
    );
    return null;
  }

  function handleDeleteConcurrencyProviderLimit(provider: string) {
    if (
      !window.confirm(
        t("editor.messages.confirmDeleteConcurrencyLimit", { provider }),
      )
    ) {
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
    setStatusMessage(t("editor.messages.concurrencyLimitDeleted", { provider }));
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
      return t("editor.validation.retryRuleNameRequired");
    }
    if (
      retryRules.some(
        (rule) => rule.name === name && rule.index !== editingIndex,
      )
    ) {
      return t("editor.validation.retryRuleExists", { name });
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
        ? t("editor.messages.retryRuleCreated", { name })
        : t("editor.messages.retryRuleUpdated", { name }),
    );
    return null;
  }

  function handleDeleteRetryRule(index: number) {
    if (!window.confirm(t("editor.messages.confirmDeleteRetryRule", { index: String(index + 1) }))) {
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
    setStatusMessage(t("editor.messages.retryRuleDeleted", { index: String(index + 1) }));
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
        ? t("editor.messages.retryRuleMovedUp", { index: String(index + 1) })
        : t("editor.messages.retryRuleMovedDown", { index: String(index + 1) }),
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
      return nextModifier.error ?? t("editor.validation.transformerModifierInvalid");
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
        ? t("editor.messages.transformerModifierCreated", {
            scopeLabel: t(`editor.scopes.${scope}`),
          })
        : t("editor.messages.transformerModifierUpdated", {
            scopeLabel: t(`editor.scopes.${scope}`),
            index: String(editingIndex + 1),
          }),
    );
    return null;
  }

  function handleDeleteTransformerModifier(
    scope: TransformerModifierScope,
    index: number,
  ) {
    if (
      !window.confirm(
        t("editor.messages.confirmDeleteTransformerModifier", {
          index: String(index + 1),
          scopeLabel: t(`editor.scopes.${scope}`),
        }),
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
      t("editor.messages.transformerModifierDeleted", {
        scopeLabel: t(`editor.scopes.${scope}`),
        index: String(index + 1),
      }),
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
        ? t("editor.messages.transformerModifierMovedUp", {
            scopeLabel: t(`editor.scopes.${scope}`),
            index: String(index + 1),
          })
        : t("editor.messages.transformerModifierMovedDown", {
            scopeLabel: t(`editor.scopes.${scope}`),
            index: String(index + 1),
          }),
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
            label={t("editor.cards.provider")}
            value={`${providers.length}`}
            detail={
              defaultProvider
                ? t("editor.cards.providerDefault", {
                    defaultProvider,
                  })
                : t("editor.cards.providerEmpty")
            }
          />
          <StatCard
            icon={RouteIcon}
            label={t("editor.cards.providerGroup")}
            value={`${providerGroups.length}`}
            detail={
              providerGroups.length > 0
                ? t("editor.cards.providerGroupSummary", {
                    count: providerGroups.reduce(
                      (total, group) => total + group.providerCount,
                      0,
                    ),
                  })
                : t("editor.cards.providerGroupEmpty")
            }
          />
          <StatCard
            icon={Settings2Icon}
            label={t("editor.cards.auth")}
            value={authConfig.accessAuthEnabled ? tCommon("states.enabled") : tCommon("states.disabled")}
            detail={
              authConfig.adminAuthEnabled
                ? t("editor.cards.authAdminEnabled")
                : t("editor.cards.authAdminDisabled")
            }
          />
          <StatCard
            icon={RouteIcon}
            label={t("editor.cards.routing")}
            value={`${routes.length} 条`}
            detail={
              routingConfig.strategy
                ? t("editor.cards.routingStrategy", {
                    strategy: routingConfig.strategy,
                  })
                : t("editor.cards.routingEmpty")
            }
          />
          <StatCard
            icon={GaugeIcon}
            label={t("editor.cards.rateLimit")}
            value={
              rateLimitConfig.enabled
                ? tCommon("states.enabled")
                : tCommon("states.disabled")
            }
            detail={
              apiKeyLimits.length > 0 || pathLimits.length > 0
                ? t("editor.cards.rateLimitSummary", {
                    apiKeyCount: String(apiKeyLimits.length),
                    pathCount: String(pathLimits.length),
                  })
                : t("editor.cards.rateLimitEmpty")
            }
          />
          <StatCard
            icon={RouteIcon}
            label={t("editor.cards.resourceManager")}
            value={
              resourceManagerConfig.enabled
                ? tCommon("states.enabled")
                : tCommon("states.disabled")
            }
            detail={
              resourceManagerConfig.enabled
                ? t("editor.cards.resourceManagerSummary", {
                    groupAlgorithm:
                      resourceManagerConfig.defaultGroupAlgorithm || "--",
                    providerAlgorithm:
                      resourceManagerConfig.defaultProviderAlgorithm || "--",
                    keyAlgorithm:
                      resourceManagerConfig.defaultKeyAlgorithm || "--",
                  })
                : t("editor.cards.resourceManagerEmpty")
            }
          />
          <StatCard
            icon={GaugeIcon}
            label={t("editor.cards.providerQueue")}
            value={
              providerQueueConfig.enabled
                ? tCommon("states.enabled")
                : tCommon("states.disabled")
            }
            detail={
              providerQueueProviders.length > 0
                ? t("editor.cards.providerQueueSummary", {
                    count: providerQueueProviders.length,
                    defaultMaxConcurrency:
                      providerQueueConfig.defaultMaxConcurrency || "--",
                  })
                : providerQueueConfig.waitHeartbeatEnabled
                  ? t("editor.cards.providerQueueHeartbeat", {
                      interval: providerQueueConfig.waitHeartbeatInterval || "--",
                    })
                  : t("editor.cards.providerQueueEmpty")
            }
          />
          <StatCard
            icon={GaugeIcon}
            label={t("editor.cards.concurrency")}
            value={
              concurrencyConfig.enabled
                ? tCommon("states.enabled")
                : tCommon("states.disabled")
            }
            detail={
              concurrencyProviderLimits.length > 0
                ? t("editor.cards.concurrencySummary", {
                    maxConcurrentRequests:
                      concurrencyConfig.maxConcurrentRequests || "--",
                    count: concurrencyProviderLimits.length,
                  })
                : concurrencyConfig.queueTimeout
                  ? t("editor.cards.concurrencyQueueTimeout", {
                      queueTimeout: concurrencyConfig.queueTimeout,
                    })
                  : t("editor.cards.concurrencyEmpty")
            }
          />
          <StatCard
            icon={RefreshCcwIcon}
            label={t("editor.cards.retry")}
            value={
              retryConfig.enabled
                ? tCommon("states.enabled")
                : tCommon("states.disabled")
            }
            detail={
              retryRules.length > 0
                ? t("editor.cards.retrySummary", {
                    count: retryRules.length,
                    defaultMaxRetries: retryConfig.defaultMaxRetries || "--",
                  })
                : t("editor.cards.retryEmpty")
            }
          />
          <StatCard
            icon={ActivityIcon}
            label={t("editor.cards.monitor")}
            value={
              monitorConfig.enabled
                ? tCommon("states.enabled")
                : tCommon("states.disabled")
            }
            detail={
              monitorConfig.metricsEnabled || monitorConfig.tracingEnabled
                ? t("editor.cards.monitorSummary", {
                    metricsState: monitorConfig.metricsEnabled
                      ? tCommon("states.enabled")
                      : tCommon("states.disabled"),
                    tracingState: monitorConfig.tracingEnabled
                      ? tCommon("states.enabled")
                      : tCommon("states.disabled"),
                  })
                : t("editor.cards.monitorEmpty")
            }
          />
          <StatCard
            icon={WifiIcon}
            label={t("editor.cards.websocket")}
            value={
              websocketConfig.enabled
                ? tCommon("states.enabled")
                : tCommon("states.disabled")
            }
            detail={
              websocketConfig.enabled
                ? t("editor.cards.websocketSummary", {
                    responsesState: websocketConfig.responsesIngressEnabled
                      ? tCommon("states.enabled")
                      : tCommon("states.disabled"),
                    realtimeState: websocketConfig.realtimeIngressEnabled
                      ? tCommon("states.enabled")
                      : tCommon("states.disabled"),
                  })
                : t("editor.cards.websocketEmpty")
            }
          />
          <StatCard
            icon={ActivityIcon}
            label={t("editor.cards.circuitBreaker")}
            value={circuitBreakerConfig.failureThreshold || "--"}
            detail={
              circuitBreakerConfig.openTimeout
                ? t("editor.cards.circuitBreakerSummary", {
                    openTimeout: circuitBreakerConfig.openTimeout,
                    failureRate: circuitBreakerConfig.failureRate || "--",
                  })
                : t("editor.cards.circuitBreakerEmpty")
            }
          />
          <StatCard
            icon={Settings2Icon}
            label={t("editor.cards.transformer")}
            value={
              transformerConfig.httpTransformStageEnabled
                ? tCommon("states.enabled")
                : tCommon("states.disabled")
            }
            detail={
              requestTransformerModifiers.length +
                responseTransformerModifiers.length >
              0
                ? t("editor.cards.transformerSummary", {
                    requestCount: String(requestTransformerModifiers.length),
                    responseCount: String(responseTransformerModifiers.length),
                  })
                : transformerConfig.highPerf
                  ? t("editor.cards.transformerHighPerf")
                  : t("editor.cards.transformerEmpty")
            }
          />
          <StatCard
            icon={HardDriveDownloadIcon}
            label={t("editor.cards.configFile")}
            value={document ? formatBytes(document.size_bytes) : "--"}
            detail={
              document?.updated_at
                ? t("editor.cards.configFileLoaded", {
                    path: document.path,
                    timestamp: formatTimestamp(
                      document.updated_at,
                      resolvedLocale,
                    ),
                  })
                : (document?.path ?? t("editor.cards.configFileEmpty"))
            }
          />
          <StatCard
            icon={RefreshCcwIcon}
            label={t("editor.cards.runtimeServer")}
            value={
              serviceStatus?.running
                ? tCommon("states.online")
                : tCommon("states.offline")
            }
            detail={
              serviceStatus?.listen_addr ||
              serviceError ||
              t("editor.cards.runtimeServerEmpty")
            }
          />
        </div>

        <div className="sticky top-2 z-20 mt-2.5 rounded-[0.95rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
          <div className="flex flex-wrap items-center justify-between gap-2.5">
            <div className="flex flex-wrap items-center gap-2">
              <Badge>
                {hasUnsavedChanges
                  ? tCommon("states.unsynced")
                  : tCommon("states.synced")}
              </Badge>
              <Badge>
                {mode === "source" ? t("editor.sourceFocus") : t("editor.structuredFocus")}
              </Badge>
              {previewDocument ? (
                <Badge>
                  {isPreviewFresh
                    ? t("editor.preview.latestWithCount", {
                        count: previewDiff.length,
                      })
                    : t("editor.preview.expired")}
                </Badge>
              ) : null}
              {previewRequiresRestart ? <Badge>{t("editor.preview.needsRestart")}</Badge> : null}
              {savedRequiresRestart && !hasUnsavedChanges ? (
                <Badge>{t("editor.preview.needsRestart")}</Badge>
              ) : null}
              {isModeSwitching ? <Badge>{t("editor.status.switchingMode")}</Badge> : null}
              {mode === "providers" ? (
                <Badge>
                  {t("editor.counts.enabledProviders", {
                    count: enabledProviderCount,
                  })}
                </Badge>
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
                <SummaryPill
                  label={t("editor.summary.lines")}
                  value={`${draftLineCount}`}
                />
                <SummaryPill
                  label={t("editor.summary.providers")}
                  value={`${providers.length}`}
                />
                <SummaryPill
                  label={t("editor.summary.groups")}
                  value={`${providerGroups.length}`}
                />
                <SummaryPill
                  label={t("editor.summary.proxy")}
                  value={formatRuntimeProxySummary(proxyConfig, t, tCommon)}
                />
                <SummaryPill
                  label={t("editor.summary.routes")}
                  value={`${routes.length}`}
                />
                <SummaryPill
                  label={t("editor.summary.limits")}
                  value={`${apiKeyLimits.length + pathLimits.length}`}
                />
                <SummaryPill
                  label={t("editor.summary.resourceManager")}
                  value={
                    resourceManagerConfig.enabled
                      ? tCommon("states.enabled")
                      : tCommon("states.disabled")
                  }
                />
                <SummaryPill
                  label={t("editor.summary.providerQueue")}
                  value={
                    providerQueueConfig.enabled
                      ? `${providerQueueProviders.length}`
                      : tCommon("states.disabled")
                  }
                />
                <SummaryPill
                  label={t("editor.summary.concurrency")}
                  value={
                    concurrencyConfig.enabled
                      ? tCommon("states.enabled")
                      : tCommon("states.disabled")
                  }
                />
                <SummaryPill
                  label={t("editor.summary.retry")}
                  value={`${retryRules.length}`}
                />
                <SummaryPill
                  label={t("editor.summary.monitor")}
                  value={
                    monitorConfig.enabled
                      ? tCommon("states.enabled")
                      : tCommon("states.disabled")
                  }
                />
                <SummaryPill
                  label={t("editor.summary.websocket")}
                  value={
                    websocketConfig.enabled
                      ? tCommon("states.enabled")
                      : tCommon("states.disabled")
                  }
                />
                <SummaryPill
                  label={t("editor.summary.circuitBreaker")}
                  value={circuitBreakerConfig.failureThreshold || "--"}
                />
                <SummaryPill
                  label={t("editor.summary.transformer")}
                  value={`${requestTransformerModifiers.length + responseTransformerModifiers.length}`}
                />
                <SummaryPill
                  label={t("editor.summary.preview")}
                  value={
                    previewDocument
                      ? tCommon("states.generated")
                      : tCommon("states.notGenerated")
                  }
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
                    label={t("editor.modes.providers.label")}
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
                    label={t("editor.modes.providerGroups.label")}
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
                    label={t("editor.modes.networkProxy.label")}
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
                    label={t("editor.modes.auth.label")}
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
                    label={t("editor.modes.routing.label")}
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
                    label={t("editor.modes.rateLimit.label")}
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
                    label={t("editor.modes.resourceManager.label")}
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
                    label={t("editor.modes.providerQueue.label")}
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
                    label={t("editor.modes.concurrency.label")}
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
                    label={t("editor.modes.retry.label")}
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
                    label={t("editor.modes.monitor.label")}
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
                    label={t("editor.modes.websocket.label")}
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
                    label={t("editor.modes.circuitBreaker.label")}
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
                    label={t("editor.modes.transformer.label")}
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
                      <SummaryPill
                        label={t("editor.source.lines")}
                        value={`${draftLineCount}`}
                      />
                      <SummaryPill
                        label={t("editor.source.chars")}
                        value={`${draftRaw.length}`}
                      />
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
                <SummaryPill
                  label={t("editor.preview.added")}
                  value={`${previewAdditions}`}
                />
                <SummaryPill
                  label={t("editor.preview.removed")}
                  value={`${previewRemovals}`}
                />
                <Badge>
                  {isPreviewFresh
                    ? t("editor.preview.latest")
                    : t("editor.preview.expired")}
                </Badge>
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
  const { t } = useTranslation("runtimeConfig");
  return (
    <div className="rounded-[0.9rem] border border-[var(--border)] bg-[var(--surface-softer)] p-4">
      <div className="text-sm font-semibold text-[var(--foreground)]">
        {t("editor.loadingCard.title", { label })}
      </div>
      <div className="mt-2 text-sm leading-6 text-[var(--muted-foreground)]">
        {t("editor.loadingCard.body")}
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
