// 由 components/workspace/workspace-shell.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import {
  type CSSProperties,
  type Dispatch,
  type RefObject,
  type SetStateAction,
  Suspense,
  useMemo,
} from "react";
import { type TFunction } from "i18next";
import { ArrowUpRightIcon, BotIcon, type LucideIcon } from "lucide-react";

import { MessageComposer } from "@/components/workspace/message-composer";
import { MessageList } from "@/components/workspace/message-list";
import { type SettingsSectionId } from "@/components/workspace/settings";
import { TrajectoryView } from "@/components/workspace/workspace-shell/lazy-surfaces";
import { type WorkspaceShellProps } from "@/components/workspace/workspace-shell/types";
import { WorkspaceShellTopbar } from "@/components/workspace/workspace-shell-topbar";
import { type WorkspaceDensity } from "@/core/settings";
import { type ComposerReferenceGroup } from "@/lib/composer-menu";
import { artifactReferenceGroup } from "@/lib/composer-references";
import { cn } from "@/lib/utils";

type WorkspaceMainSectionProps = Pick<
  WorkspaceShellProps,
  | "backtrackError"
  | "backtrackNavigationActive"
  | "backtrackNotice"
  | "backtrackPendingMessageId"
  | "backtrackSelectedMessageId"
  | "canBacktrack"
  | "composerAttachments"
  | "draft"
  | "isResponding"
  | "modelOptions"
  | "onBacktrackToMessage"
  | "onDraftChange"
  | "onModelChange"
  | "onProviderChange"
  | "onReasoningEffortChange"
  | "onSelectBacktrackNavigationMessage"
  | "onStopResponding"
  | "onSubmit"
  | "phase"
  | "providerOptions"
  | "reasoningEffortDefault"
  | "reasoningEffortError"
  | "reasoningEffortOptions"
  | "runtimeModelsError"
  | "runtimeModelsLoading"
  | "selectedModel"
  | "selectedProvider"
  | "selectedReasoningEffort"
  | "selectedThread"
  | "trajectoryStore"
> & {
  artifactRailOpen: boolean;
  composerOverlayRef: RefObject<HTMLDivElement | null>;
  density: WorkspaceDensity;
  handleOpenArtifact: (artifactId: string) => void;
  isCompact: boolean;
  isNewThread: boolean;
  liveTeamCount: number;
  messageListStyle: CSSProperties | undefined;
  newThreadSuggestions: {
    key: string;
    icon: LucideIcon;
    title: string;
    description: string;
    prompt: string;
  }[];
  openSettings: (section?: SettingsSectionId) => void;
  setArtifactRailManualOpen: Dispatch<SetStateAction<boolean>>;
  setMobileSidebarOpen: Dispatch<SetStateAction<boolean>>;
  setViewMode: Dispatch<SetStateAction<"chat" | "trajectory">>;
  t: TFunction<"workspace">;
  threadStatusLabel: string;
  threadSubtitle: string;
  transportLabel: string;
  viewMode: "chat" | "trajectory";
};

export function WorkspaceMainSection({
  backtrackError,
  backtrackNavigationActive,
  backtrackNotice,
  backtrackPendingMessageId,
  backtrackSelectedMessageId,
  canBacktrack,
  composerAttachments,
  draft,
  isResponding,
  modelOptions,
  onBacktrackToMessage,
  onDraftChange,
  onModelChange,
  onProviderChange,
  onReasoningEffortChange,
  onSelectBacktrackNavigationMessage,
  onStopResponding,
  onSubmit,
  phase,
  providerOptions,
  reasoningEffortDefault,
  reasoningEffortError,
  reasoningEffortOptions,
  runtimeModelsError,
  runtimeModelsLoading,
  selectedModel,
  selectedProvider,
  selectedReasoningEffort,
  selectedThread,
  trajectoryStore,
  artifactRailOpen,
  composerOverlayRef,
  density,
  handleOpenArtifact,
  isCompact,
  isNewThread,
  liveTeamCount,
  messageListStyle,
  newThreadSuggestions,
  openSettings,
  setArtifactRailManualOpen,
  setMobileSidebarOpen,
  setViewMode,
  t,
  threadStatusLabel,
  threadSubtitle,
  transportLabel,
  viewMode,
}: WorkspaceMainSectionProps) {
  // P1-4 子片 3：`@` 引用候选（当前线程交付物；会话/子代理分组待数据源就绪）。
  const composerReferenceGroups = useMemo<ComposerReferenceGroup[]>(() => {
    const files = artifactReferenceGroup(
      selectedThread.artifacts,
      t("composer.references.files"),
    );
    return files ? [files] : [];
  }, [selectedThread.artifacts, t]);

  return (
    <section
      id="workspace-preview"
      className="relative flex h-full min-h-0 flex-col overflow-hidden [background:var(--workspace-main-bg)]"
    >
      <WorkspaceShellTopbar
        artifactRailOpen={artifactRailOpen}
        density={density}
        isNewThread={isNewThread}
        liveTeamCount={liveTeamCount}
        onOpenSidebar={() => setMobileSidebarOpen(true)}
        onOpenSettings={() => openSettings("appearance")}
        onToggleArtifactRail={() => setArtifactRailManualOpen((current) => !current)}
        selectedThread={selectedThread}
        threadSubtitle={threadSubtitle}
        threadStatusLabel={threadStatusLabel}
        transportLabel={transportLabel}
      />

      <div
        className={cn(
          "flex min-h-0 flex-1 justify-center overflow-hidden",
          isNewThread
            ? "items-center px-3 pb-4 pt-14 sm:px-4"
            : isCompact
              ? "pt-[2.95rem]"
              : "pt-[3.2rem]",
        )}
      >
        <div
          className={cn(
            "relative flex h-full min-h-0 w-full flex-col",
            isNewThread ? "max-w-[48rem]" : null,
          )}
        >
          {!isNewThread ? (
            <div className="flex min-h-0 flex-1 flex-col overflow-hidden">
              {trajectoryStore ? (
                <div
                  aria-label={t("panels.shell.viewTabs.ariaLabel")}
                  className="flex items-center gap-1 border-b border-border px-3 pt-2"
                  role="tablist"
                >
                  <button
                    aria-selected={viewMode === "chat"}
                    className={cn(
                      "rounded-t-md border border-b-0 px-3 py-1.5 app-text-12 transition",
                      viewMode === "chat"
                        ? "border-border bg-surface-softer text-foreground"
                        : "border-transparent text-muted-foreground hover:text-foreground",
                    )}
                    onClick={() => setViewMode("chat")}
                    role="tab"
                    type="button"
                  >
                    {t("panels.shell.viewTabs.chat")}
                  </button>
                  <button
                    aria-selected={viewMode === "trajectory"}
                    className={cn(
                      "rounded-t-md border border-b-0 px-3 py-1.5 app-text-12 transition",
                      viewMode === "trajectory"
                        ? "border-border bg-surface-softer text-foreground"
                        : "border-transparent text-muted-foreground hover:text-foreground",
                    )}
                    onClick={() => setViewMode("trajectory")}
                    role="tab"
                    type="button"
                  >
                    {t("panels.shell.viewTabs.trajectory")}
                  </button>
                </div>
              ) : null}
              {viewMode === "chat" || !trajectoryStore ? (
                <MessageList
                  artifacts={selectedThread.artifacts}
                  backtrackError={backtrackError}
                  backtrackNotice={backtrackNotice}
                  backtrackPendingMessageId={backtrackPendingMessageId}
                  backtrackNavigationActive={backtrackNavigationActive}
                  backtrackSelectedMessageId={backtrackSelectedMessageId}
                  canBacktrack={canBacktrack}
                  className={cn(
                    "h-full px-3 sm:px-4 lg:px-5",
                    isCompact ? "pt-3" : "pt-4",
                  )}
                  contentClassName={cn(
                    "max-w-[50rem]",
                    isCompact ? "gap-4" : "gap-6",
                  )}
                  isResponding={isResponding}
                  messages={selectedThread.messages}
                  onBacktrackToMessage={onBacktrackToMessage}
                  onSelectBacktrackNavigationMessage={onSelectBacktrackNavigationMessage}
                  onSelectArtifact={handleOpenArtifact}
                  phase={phase}
                  scrollMemoryKey={selectedThread.sessionId ?? selectedThread.id}
                  style={messageListStyle}
                />
              ) : (
                <Suspense fallback={null}>
                  <TrajectoryView
                    className="h-full"
                    isLive={isResponding}
                    sessionId={selectedThread.sessionId}
                    store={trajectoryStore}
                  />
                </Suspense>
              )}
            </div>
          ) : (
            <div className="mx-auto flex w-full max-w-[46rem] flex-1 flex-col justify-center pb-4">
              <div className="text-center">
                <div className="mx-auto grid size-11 place-items-center rounded-[1rem] border border-accent-primary-border bg-accent-primary-soft text-accent-primary shadow-[0_8px_24px_var(--accent-primary-shadow)]">
                  <BotIcon size={20} />
                </div>
                <h1 className="mt-3 text-[1.45rem] font-semibold tracking-[-0.03em] text-foreground sm:text-[1.7rem]">
                  {t("shell.newChatTitle")}
                </h1>
              </div>
              <div className="mt-5 grid grid-cols-2 gap-2 sm:gap-3">
                {newThreadSuggestions.map((suggestion) => {
                  const SuggestionIcon = suggestion.icon;

                  return (
                    <button
                      key={suggestion.key}
                      type="button"
                      onClick={() => onDraftChange(suggestion.prompt)}
                      className="group flex min-h-[5.5rem] items-start gap-3 rounded-panel border border-border bg-surface-softer px-3 py-3 text-left transition hover:border-border-strong hover:bg-surface-soft focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring sm:px-3.5"
                    >
                      <span className="mt-0.5 grid size-8 shrink-0 place-items-center rounded-field border border-border bg-surface-solid text-accent-secondary">
                        <SuggestionIcon size={15} />
                      </span>
                      <span className="min-w-0 flex-1">
                        <span className="flex items-center justify-between gap-2 text-sm font-semibold text-foreground">
                          {suggestion.title}
                          <ArrowUpRightIcon
                            size={13}
                            className="shrink-0 text-muted-foreground transition group-hover:-translate-y-0.5 group-hover:translate-x-0.5 group-hover:text-foreground"
                          />
                        </span>
                        <span className="mt-1 block text-xs leading-5 text-muted-foreground">
                          {suggestion.description}
                        </span>
                      </span>
                    </button>
                  );
                })}
              </div>
            </div>
          )}

          {!isNewThread ? (
            <div
              aria-hidden="true"
              className="pointer-events-none absolute inset-x-0 bottom-0 z-20 px-3 sm:px-4 lg:px-5"
            >
              <div className="mx-auto h-16 w-full max-w-[54rem] [background:var(--workspace-fade-overlay)] blur-lg" />
            </div>
          ) : null}

          <div
            ref={composerOverlayRef}
            className={cn(
              "pointer-events-none z-30 px-3 sm:px-4 lg:px-5",
              isNewThread
                ? "relative inset-auto mx-auto w-full max-w-[50rem] pb-0"
                : "absolute inset-x-0 bottom-0 pb-3",
            )}
          >
            {isNewThread || viewMode === "chat" || !trajectoryStore ? (
              <div className="pointer-events-auto mx-auto w-full max-w-[50rem]">
                <MessageComposer
                  attachments={composerAttachments}
                  density={density}
                  draft={draft}
                  focusKey={selectedThread.sessionId ?? selectedThread.id}
                  hasSession={Boolean(selectedThread.sessionId)}
                  isNewThread={isNewThread}
                  isResponding={isResponding}
                  modelOptions={modelOptions}
                  reasoningEffortDefault={reasoningEffortDefault}
                  reasoningEffortError={reasoningEffortError}
                  reasoningEffortOptions={reasoningEffortOptions}
                  referenceGroups={composerReferenceGroups}
                  selectedArtifactCount={selectedThread.artifacts.length}
                  onModelChange={onModelChange}
                  onProviderChange={onProviderChange}
                  onReasoningEffortChange={onReasoningEffortChange}
                  providerOptions={providerOptions}
                  runtimeModelsError={runtimeModelsError}
                  runtimeModelsLoading={runtimeModelsLoading}
                  selectedModel={selectedModel}
                  selectedProvider={selectedProvider}
                  selectedReasoningEffort={selectedReasoningEffort}
                  transport={selectedThread.transport}
                  onDraftChange={onDraftChange}
                  onStop={onStopResponding}
                  onSubmit={onSubmit}
                />
              </div>
            ) : null}
          </div>
        </div>
      </div>
    </section>
  );
}
