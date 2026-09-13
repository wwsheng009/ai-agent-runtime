// P0-2：原 893 行入口拆分后的容器，仅保留状态编排与组合，逻辑不变。

import { lazy, startTransition, Suspense, useState } from "react";
import { useTranslation } from "react-i18next";
import { useSearchParams } from "react-router-dom";

import { useAppSettings } from "@/core/settings";
import { useRuntimeLogs } from "@/hooks/use-runtime-logs";
import { LogsHeaderSection } from "@/pages/logs-page/logs-header";
import { LogsListPanel } from "@/pages/logs-page/logs-list-panel";
import {
  buildEntrySubtitle,
  detailRows,
  formatDetailValue,
  levelTone,
} from "@/pages/logs-page/format";
import { LogsPageDetailPanelFallback } from "@/pages/logs-page/primitives";
import type { LogsPageDetailLabels } from "@/pages/logs-page-detail-panel.i18n";
import {
  buildRuntimeLogsActiveChips,
  buildRuntimeLogIdentifierRows,
  buildRuntimeLogLevelStats,
  buildRuntimeLogsShareState,
  readRuntimeLogsUrlState,
  writeRuntimeLogsUrlState,
  type RuntimeLogLevelFilter,
  type RuntimeLogsActiveChip,
  type RuntimeLogsUrlState,
} from "@/pages/logs-page-shared";

const LogsPageDetailPanel = lazy(() =>
  import("@/pages/logs-page-detail-panel").then((module) => ({
    default: module.LogsPageDetailPanel,
  })),
);

export function LogsPage() {
  const { resolvedLocale } = useAppSettings();
  const { t } = useTranslation("logs");
  const { t: tCommon } = useTranslation("common");
  const [searchParams, setSearchParams] = useSearchParams();
  const urlState = readRuntimeLogsUrlState(searchParams);
  const {
    adminToken,
    connectionState,
    entries,
    error,
    filePath,
    loading,
    logFileExists,
    refreshing,
    refresh,
    selectedEntry,
    setAdminToken,
    streamError,
  } = useRuntimeLogs({
    follow: urlState.follow,
    level: urlState.level,
    onSelectedCursorChange: setSelectedCursor,
    query: urlState.query,
    selectedCursor: urlState.cursor,
  });
  const [copiedSection, setCopiedSection] = useState<string | null>(null);

  const follow = urlState.follow;
  const level = urlState.level;
  const query = urlState.query;
  const selectedCursor = urlState.cursor;
  const detailLabels: LogsPageDetailLabels = {
    copied: tCommon("actions.copied"),
    summary: t("copiedLog"),
    insights: t("insights"),
    insightsHelp: t("insightsHelp"),
    identifiers: t("identifiers"),
    metadata: t("metadata"),
    responsePreview: t("responsePreview"),
    extraFields: t("extraFields"),
    rawJson: t("rawJson"),
    clearSearch: tCommon("actions.clearSearch"),
    copyValue: tCommon("actions.copyValue"),
    copyMetadata: tCommon("actions.copyMetadata"),
    copyPreview: tCommon("actions.copyPreview"),
    copyFields: tCommon("actions.copyFields"),
    copyJson: tCommon("actions.copyJson"),
    cancelFilter: t("cancelFilter"),
    filterSameValue: t("filterSameValue"),
    copiedLog: t("copiedLog"),
    identifiersHelp: t("identifiersHelp"),
    cursorLabel: t("cursorLabel"),
    levelFallback: t("levelFallback"),
    requestPrefix: t("requestPrefix"),
    runtimeLogFallback: t("runtimeLogFallback"),
    statusPrefix: t("statusPrefix"),
    timestamp: t("timestamp"),
    level: t("level"),
    module: t("module"),
    caller: t("caller"),
    requestId: t("requestId"),
    traceId: t("traceId"),
    sessionId: t("sessionId"),
    provider: t("provider"),
    model: t("model"),
    method: t("method"),
    url: t("url"),
    responseStatus: t("responseStatus"),
    upstreamError: t("upstreamError"),
    cacheHit: t("cacheHit"),
    cacheHitValueHit: t("cacheHitValueHit"),
    cacheHitValueMiss: t("cacheHitValueMiss"),
    skillExposureMode: t("skillExposureMode"),
    finalFunctionCount: t("finalFunctionCount"),
    routedSkillCount: t("routedSkillCount"),
    candidateCount: t("candidateCount"),
    exposedFunctionCount: t("exposedFunctionCount"),
  };
  const metadataRows = detailRows(selectedEntry, detailLabels);
  const formattedMetadataRows = metadataRows.map(([label, value]) => ({
    label: label ?? "",
    value: formatDetailValue(value, tCommon("states.none")),
  }));
  const identifierRows = buildRuntimeLogIdentifierRows(selectedEntry, {
    identifiers: {
      request_id: t("identifierRequest"),
      trace_id: t("identifierTrace"),
      session_id: t("identifierSession"),
    },
  });
  const levelStats = buildRuntimeLogLevelStats(entries, {
    levelStats: {
      error: { label: t("levelError"), shortLabel: t("levelShortError") },
      warn: { label: t("levelWarn"), shortLabel: t("levelShortWarn") },
      info: { label: t("levelInfo"), shortLabel: t("levelShortInfo") },
      debug: { label: t("levelDebug"), shortLabel: t("levelShortDebug") },
      other: { label: t("levelOther"), shortLabel: t("levelShortOther") },
    },
  });
  const newestCursor = entries[0]?.cursor ?? null;
  const activeChips = buildRuntimeLogsActiveChips(urlState, newestCursor, {
    activeChips: {
      query: t("chipQuery"),
      level: t("chipLevel"),
      follow: t("chipFollow"),
      cursor: t("chipCursor"),
    },
    activeChipValues: {
      follow: t("activeChipOff"),
    },
  });
  const shareState = buildRuntimeLogsShareState(urlState, newestCursor);
  const rawJsonText = selectedEntry?.raw
    ? JSON.stringify(selectedEntry.raw, null, 2)
    : (selectedEntry?.raw_text ?? "");
  const metadataText = metadataRows
    .map(([label, value]) => `${label}: ${formatDetailValue(value, tCommon("states.none"))}`)
    .join("\n");
  const responsePreviewText = selectedEntry?.response_body_preview ?? "";
  const extraFieldsText = selectedEntry?.fields
    ? JSON.stringify(selectedEntry.fields, null, 2)
    : "";
  const selectedEntrySubtitle = selectedEntry
    ? buildEntrySubtitle(selectedEntry, detailLabels)
    : "";
  const selectedLevelTone = selectedEntry ? levelTone(selectedEntry.level) : "";
  const shareSearchParams = writeRuntimeLogsUrlState(
    searchParams,
    shareState,
  );
  const sharePath = shareSearchParams.toString()
    ? `/logs?${shareSearchParams.toString()}`
    : "/logs";
  const shareUrl =
    typeof window === "undefined"
      ? sharePath
      : `${window.location.origin}${sharePath}`;

  function updateUrlState(
    updater: (currentState: RuntimeLogsUrlState) => RuntimeLogsUrlState,
  ) {
    startTransition(() => {
      setSearchParams(
        (currentSearchParams) =>
          writeRuntimeLogsUrlState(
            currentSearchParams,
            updater(readRuntimeLogsUrlState(currentSearchParams)),
          ),
        { replace: true },
      );
    });
  }

  function setQuery(
    value: string | ((currentValue: string) => string),
  ) {
    updateUrlState((currentState) => ({
      ...currentState,
      cursor: null,
      query:
        typeof value === "function" ? value(currentState.query) : value,
    }));
  }

  function setLevel(value: RuntimeLogLevelFilter) {
    updateUrlState((currentState) => ({
      ...currentState,
      cursor: null,
      level: value,
    }));
  }

  function setFollow(
    value: boolean | ((currentValue: boolean) => boolean),
  ) {
    updateUrlState((currentState) => {
      const nextFollow =
        typeof value === "function" ? value(currentState.follow) : value;
      return {
        ...currentState,
        cursor: nextFollow ? newestCursor : currentState.cursor,
        follow: nextFollow,
      };
    });
  }

  function setSelectedCursor(
    value: number | null | ((currentValue: number | null) => number | null),
  ) {
    updateUrlState((currentState) => ({
      ...currentState,
      cursor:
        typeof value === "function" ? value(currentState.cursor) : value,
    }));
  }

  function toggleIdentifierQuery(value: string) {
    const normalizedValue = value.trim();
    if (!normalizedValue) {
      return;
    }

    setQuery((currentQuery) =>
      currentQuery.trim() === normalizedValue ? "" : normalizedValue,
    );
  }

  async function handleCopy(sectionKey: string, value: string) {
    if (!value.trim()) {
      return;
    }
    try {
      await navigator.clipboard.writeText(value);
      setCopiedSection(sectionKey);
      window.setTimeout(() => {
        setCopiedSection((currentSection) =>
          currentSection === sectionKey ? null : currentSection,
        );
      }, 1500);
    } catch {
      setCopiedSection(null);
    }
  }

  function clearActiveChip(chipKey: RuntimeLogsActiveChip["key"]) {
    switch (chipKey) {
      case "query":
        setQuery("");
        return;
      case "level":
        setLevel("");
        return;
      case "follow":
        setFollow(true);
        return;
      case "cursor":
        setSelectedCursor(newestCursor);
        return;
    }
  }

  function clearAllActiveState() {
    updateUrlState(() => ({
      query: "",
      level: "",
      follow: true,
      cursor: null,
    }));
  }

  return (
    <div className="min-h-screen [background:var(--workspace-shell-bg)] text-foreground lg:h-dvh lg:overflow-hidden">
      <div className="mx-auto flex min-h-screen w-full max-w-[1760px] flex-col gap-2 px-2.5 py-2.5 sm:px-3 lg:h-full lg:min-h-0 lg:px-3">
        <LogsHeaderSection
          activeChips={activeChips}
          adminToken={adminToken}
          connectionState={connectionState}
          copiedViewLink={copiedSection === "view_link"}
          error={error}
          filePath={filePath}
          follow={follow}
          level={level}
          logFileExists={logFileExists}
          onAdminTokenChange={setAdminToken}
          onClearAll={clearAllActiveState}
          onClearChip={clearActiveChip}
          onCopyViewLink={() => handleCopy("view_link", shareUrl)}
          onFollowChange={setFollow}
          onLevelChange={setLevel}
          onQueryChange={setQuery}
          onRefresh={refresh}
          query={query}
          refreshing={refreshing}
          streamError={streamError}
        />

        <section className="grid min-h-0 flex-1 gap-2 lg:min-h-0 lg:flex-1 xl:grid-cols-[21rem_minmax(0,1fr)] 2xl:grid-cols-[22rem_minmax(0,1fr)]">
          <LogsListPanel
            entries={entries}
            error={error}
            level={level}
            levelStats={levelStats}
            loading={loading}
            onSelectCursor={setSelectedCursor}
            onSelectLevel={setLevel}
            resolvedLocale={resolvedLocale}
            selectedCursor={selectedCursor}
          />

          <div className="surface-panel flex min-h-[22rem] flex-col overflow-hidden rounded-panel-lg lg:min-h-0">
            <div className="border-b border-border px-3.5 py-2">
              <div className="app-text-13 font-semibold tracking-[-0.02em]">{t("detailsTitle")}</div>
              <div className="mt-1 app-text-11 text-muted-foreground">
                {t("detailsDescription")}
              </div>
            </div>

            {!selectedEntry ? (
              <div className="flex flex-1 items-center justify-center px-6 text-center text-sm text-muted-foreground">
                {t("selectPrompt")}
              </div>
            ) : (
              <Suspense fallback={<LogsPageDetailPanelFallback message={t("detailsLoading")} />}>
                <LogsPageDetailPanel
                  copiedSection={copiedSection}
                  extraFieldsText={extraFieldsText}
                  identifierRows={identifierRows}
                  metadataRows={formattedMetadataRows}
                  metadataText={metadataText}
                  onClearQuery={() => setQuery("")}
                  onCopy={handleCopy}
                  onToggleIdentifierQuery={toggleIdentifierQuery}
                  query={query}
                  rawJsonText={rawJsonText}
                  responsePreviewText={responsePreviewText}
                  selectedEntry={selectedEntry}
                  selectedEntrySubtitle={selectedEntrySubtitle}
                  selectedLevelTone={selectedLevelTone}
                  labels={detailLabels}
                />
              </Suspense>
            )}
          </div>
        </section>
      </div>
    </div>
  );
}
