// 由 components/workspace/workspace-shell.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import {
  type CSSProperties,
  type Dispatch,
  type RefObject,
  type SetStateAction,
  Suspense,
  useMemo,
  useState,
} from "react";
import { type TFunction } from "i18next";
import { ArrowUpRightIcon, BotIcon, type LucideIcon } from "lucide-react";

import { MessageComposer } from "@/components/workspace/message-composer";
import { MessageList } from "@/components/workspace/message-list";
import { PendingInteractionBar } from "@/components/workspace/pending-interaction-bar";
import { ComposerModelDialog } from "@/components/workspace/composer-model-dialog";
import { JobsPanel } from "@/components/workspace/jobs-panel";
import { SessionAgentsPanel } from "@/components/workspace/session-agents-panel";
import { agentDisplayName } from "@/components/workspace/session-agents-panel-shared";
import { type SettingsSectionId } from "@/components/workspace/settings";
import { FilePreviewDialog } from "@/components/workspace/file-preview-dialog";
import {
  TrajectoryView,
  WorkspaceSkillsSurface,
} from "@/components/workspace/workspace-shell/lazy-surfaces";
import {
  type WorkspaceShellProps,
  type WorkspaceViewMode,
} from "@/components/workspace/workspace-shell/types";
import { WorkspaceShellTopbar } from "@/components/workspace/workspace-shell-topbar";
import { type WorkspaceDensity } from "@/core/settings";
import { useFilePreview } from "@/hooks/workspace/use-file-preview";
import { useBackgroundJobs } from "@/hooks/workspace/use-background-jobs";
import { useComposerCommandSurface } from "@/hooks/workspace/composer/use-composer-command-surface";
import { useSessionAgents } from "@/hooks/use-session-agents";
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
  | "connectionStatus"
  | "draft"
  | "isResponding"
  | "modelOptions"
  | "onAnswerPendingQuestion"
  | "onBacktrackToMessage"
  | "onDraftChange"
  | "onModelChange"
  | "onProviderChange"
  | "onReasoningEffortChange"
  | "onRenameRuntimeSession"
  | "onResolvePendingApproval"
  | "onRetryConnection"
  | "onPlanDecision"
  | "onPlanNotesChange"
  | "onSelectBacktrackNavigationMessage"
  | "onStopResponding"
  | "onSubmit"
  | "pendingInteraction"
  | "phase"
  | "planActionPending"
  | "planNotesDraft"
  | "providerOptions"
  | "reasoningEffortDefault"
  | "reasoningEffortError"
  | "reasoningEffortOptions"
  | "runtimeModels"
  | "runtimeModelsError"
  | "runtimeModelsLoading"
  | "selectedModel"
  | "selectedProvider"
  | "selectedReasoningEffort"
  | "selectedThread"
  | "trajectoryStore"
> & {
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
  onToggleRightRail: () => void;
  rightRailOpen: boolean;
  setMobileSidebarOpen: Dispatch<SetStateAction<boolean>>;
  setViewMode: Dispatch<SetStateAction<WorkspaceViewMode>>;
  t: TFunction<"workspace">;
  threadStatusLabel: string;
  threadSubtitle: string;
  transportLabel: string;
  viewMode: WorkspaceViewMode;
};

export function WorkspaceMainSection({
  backtrackError,
  backtrackNavigationActive,
  backtrackNotice,
  backtrackPendingMessageId,
  backtrackSelectedMessageId,
  canBacktrack,
  composerAttachments,
  connectionStatus,
  draft,
  isResponding,
  modelOptions,
  onBacktrackToMessage,
  onDraftChange,
  onModelChange,
  onProviderChange,
  onReasoningEffortChange,
  onRenameRuntimeSession,
  onSelectBacktrackNavigationMessage,
  onStopResponding,
  onSubmit,
  pendingInteraction,
  onResolvePendingApproval,
  onRetryConnection,
  onAnswerPendingQuestion,
  onPlanDecision,
  onPlanNotesChange,
  planActionPending,
  planNotesDraft,
  phase,
  providerOptions,
  reasoningEffortDefault,
  reasoningEffortError,
  reasoningEffortOptions,
  runtimeModels,
  runtimeModelsError,
  runtimeModelsLoading,
  selectedModel,
  selectedProvider,
  selectedReasoningEffort,
  selectedThread,
  trajectoryStore,
  composerOverlayRef,
  density,
  handleOpenArtifact,
  isCompact,
  isNewThread,
  liveTeamCount,
  messageListStyle,
  newThreadSuggestions,
  openSettings,
  onToggleRightRail,
  rightRailOpen,
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

  // P2-1A / P2-9：后台任务（Jobs）——shell owner 单例加载一次，
  // 顶栏常驻状态条与弹层共用同一份数据（计数天然一致，不做第二次拉取）。
  const jobsSessionId = selectedThread.sessionId?.trim() ?? "";
  const [jobsPanelOpen, setJobsPanelOpen] = useState(false);
  const jobs = useBackgroundJobs({
    enabled: Boolean(jobsSessionId),
    lastRuntimeEventType: selectedThread.lastRuntimeEventType,
    runtimeEventCount: selectedThread.runtimeEventCount,
    sessionId: jobsSessionId,
  });

  // P2-1A：子代理控制面（AgentControl 身份图）——顶栏 lineage 面包屑与弹层共用一次加载。
  const agentsSessionId = selectedThread.sessionId?.trim() ?? "";
  const sessionAgents = useSessionAgents({ sessionId: agentsSessionId });
  const [agentsPanelOpen, setAgentsPanelOpen] = useState(false);
  const agentBreadcrumb = useMemo(
    () =>
      sessionAgents.tree.lineage.map((agent) => ({
        label: agentDisplayName(agent),
        path: agent.agentPath,
      })),
    [sessionAgents.tree.lineage],
  );

  // P2-1A：工具行文件路径的运行时预览（fs/read-file，只读）；未命中关联产物时兜底。
  const filePreview = useFilePreview();

  // P2-7：composer `/` 命令面（清单 / `/model` 候选与弹窗 / 执行器 / 回执文案）。
  // `/export`、`/rename`、`/model` 分别复用轨迹导出、侧栏重命名与常驻座位选择器的
  // 同一实现；`/feedback` 写本地日志（log-only）。目录未就绪时菜单无候选、
  // 弹窗如实显示空/失败态，不伪造模型名。
  const composerCommandSurface = useComposerCommandSurface({
    runtimeModels,
    onModelChange,
    onRenameSession: onRenameRuntimeSession,
    sessionId: selectedThread.sessionId,
  });

  // P2-1B：中部视图页签（对话 / 技能 / 轨迹）。技能与轨迹页签都不承载输入框，
  // 只有对话面（含新会话；以及无轨迹 store 时回落对话面的会话）保留 composer。
  const skillsSurfaceVisible = !isNewThread && viewMode === "skills";
  const chatSurfaceVisible =
    !skillsSurfaceVisible &&
    (isNewThread || viewMode === "chat" || !trajectoryStore);
  const viewTabClass = (active: boolean) =>
    cn(
      "rounded-t-md border border-b-0 px-3 py-1.5 app-text-12 transition",
      active
        ? "border-border bg-surface-softer text-foreground"
        : "border-transparent text-muted-foreground hover:text-foreground",
    );

  return (
    <section
      id="workspace-preview"
      className="relative flex h-full min-h-0 flex-col overflow-hidden [background:var(--workspace-main-bg)]"
    >
      <WorkspaceShellTopbar
        agentBreadcrumb={agentBreadcrumb}
        agentDescendantCount={sessionAgents.tree.descendants.length}
        connectionStatus={connectionStatus}
        density={density}
        isNewThread={isNewThread}
        liveJobsCount={jobs.liveCount}
        liveTeamCount={liveTeamCount}
        onOpenAgents={agentsSessionId ? () => setAgentsPanelOpen(true) : undefined}
        onOpenJobs={jobsSessionId ? () => setJobsPanelOpen(true) : undefined}
        onOpenSidebar={() => setMobileSidebarOpen(true)}
        onOpenSettings={() => openSettings("appearance")}
        onRetryConnection={onRetryConnection}
        onToggleRightRail={onToggleRightRail}
        rightRailOpen={rightRailOpen}
        selectedThread={selectedThread}
        threadSubtitle={threadSubtitle}
        threadStatusLabel={threadStatusLabel}
        transportLabel={transportLabel}
      />

      {jobsPanelOpen && jobsSessionId ? (
        <JobsPanel
          controller={jobs}
          onClose={() => setJobsPanelOpen(false)}
          open={jobsPanelOpen}
        />
      ) : null}

      {agentsPanelOpen && agentsSessionId ? (
        <SessionAgentsPanel
          agents={sessionAgents}
          onClose={() => setAgentsPanelOpen(false)}
          open={agentsPanelOpen}
        />
      ) : null}

      <FilePreviewDialog preview={filePreview} />

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
            // 批次 F2：本容器是宽度轴（--app-chat-content-width）的查询容器，
            // 让转录列 / 停靠卡 / 输入卡共享同一 64cqw 基准（不受滚动条宽度影响）。
            "relative flex h-full min-h-0 w-full flex-col [container-type:inline-size]",
            isNewThread ? "max-w-[48rem]" : null,
          )}
        >
          {!isNewThread ? (
            <div className="flex min-h-0 flex-1 flex-col overflow-hidden">
              <div
                aria-label={t("panels.shell.viewTabs.ariaLabel")}
                className="flex items-center gap-1 border-b border-border px-3 pt-2"
                role="tablist"
              >
                <button
                  aria-selected={viewMode === "chat"}
                  className={viewTabClass(viewMode === "chat")}
                  data-testid="workspace-view-tab-chat"
                  onClick={() => setViewMode("chat")}
                  role="tab"
                  type="button"
                >
                  {t("panels.shell.viewTabs.chat")}
                </button>
                <button
                  aria-selected={viewMode === "skills"}
                  className={viewTabClass(viewMode === "skills")}
                  data-testid="workspace-view-tab-skills"
                  onClick={() => setViewMode("skills")}
                  role="tab"
                  type="button"
                >
                  {t("panels.shell.viewTabs.skills")}
                </button>
                {trajectoryStore ? (
                  <button
                    aria-selected={viewMode === "trajectory"}
                    className={viewTabClass(viewMode === "trajectory")}
                    data-testid="workspace-view-tab-trajectory"
                    onClick={() => setViewMode("trajectory")}
                    role="tab"
                    type="button"
                  >
                    {t("panels.shell.viewTabs.trajectory")}
                  </button>
                ) : null}
              </div>
              {skillsSurfaceVisible ? (
                <Suspense fallback={null}>
                  <WorkspaceSkillsSurface />
                </Suspense>
              ) : viewMode === "chat" || !trajectoryStore ? (
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
                  connectionStatus={connectionStatus}
                  contentClassName={cn(
                    // 列宽由 message-list 的宽度轴（W）提供，此处只覆盖行间距。
                    isCompact ? "gap-4" : "gap-6",
                  )}
                  isResponding={isResponding}
                  messages={selectedThread.messages}
                  onBacktrackToMessage={onBacktrackToMessage}
                  onPreviewFilePath={filePreview.open}
                  onRetryConnection={onRetryConnection}
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
              <div className="mx-auto h-16 w-full max-w-[var(--app-chat-content-width-composer)] [background:var(--workspace-fade-overlay)] blur-lg" />
            </div>
          ) : null}

          <div
            ref={composerOverlayRef}
            className={cn(
              "pointer-events-none z-30 px-3 sm:px-4 lg:px-5",
              isNewThread
                ? "relative inset-auto mx-auto w-full pb-0"
                : "absolute inset-x-0 bottom-0 pb-3",
            )}
          >
            {chatSurfaceVisible ? (
              <>
                {/* 批次 F2：停靠卡 = W − 32px（与转录列同一宽度轴）。 */}
                <div className="pointer-events-auto mx-auto w-full max-w-[var(--app-chat-content-width-dock)]">
                  <PendingInteractionBar
                    interaction={pendingInteraction ?? null}
                    onAnswerQuestion={(questionId, answer) =>
                      onAnswerPendingQuestion?.(questionId, answer)
                    }
                    onResolveApproval={(requestId, allow) =>
                      onResolvePendingApproval?.(requestId, allow)
                    }
                    onPlanDecision={onPlanDecision}
                    onPlanNotesChange={onPlanNotesChange}
                    planActionPending={planActionPending}
                    planNotesDraft={planNotesDraft}
                  />
                </div>
                {/* 批次 F2：输入卡 = W + 32px（比转录列宽一档）。 */}
                <div className="pointer-events-auto mx-auto w-full max-w-[var(--app-chat-content-width-composer)]">
                  <MessageComposer
                    attachments={composerAttachments}
                    commands={composerCommandSurface.commands}
                    commandResultNotice={composerCommandSurface.commandResult}
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
                    onCommand={composerCommandSurface.onCommand}
                    onDismissCommandResult={
                      composerCommandSurface.onDismissCommandResult
                    }
                    onDraftChange={onDraftChange}
                    onStop={onStopResponding}
                    onSubmit={onSubmit}
                  />
                </div>
              </>
            ) : null}
          </div>
        </div>
      </div>
      <ComposerModelDialog
        error={runtimeModelsError}
        groups={composerCommandSurface.modelGroups}
        loading={runtimeModelsLoading}
        onClose={composerCommandSurface.closeModelDialog}
        onSelect={(model) => {
          onModelChange(model);
          composerCommandSurface.closeModelDialog();
        }}
        open={composerCommandSurface.modelDialogOpen}
        selectedModel={selectedModel}
        selectedProvider={selectedProvider}
      />
    </section>
  );
}
