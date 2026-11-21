// 由 components/workspace/workspace-shell.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { Suspense } from "react";

import { MessageComposer } from "@/components/workspace/message-composer";
import { ComposerContextUsageControl } from "@/components/workspace/composer-context-usage-control";
import { ComposerPermissionModeControl } from "@/components/workspace/composer-permission-mode-control";
import { ComposerProfileControl } from "@/components/workspace/composer-profile-control";
import { MessageList } from "@/components/workspace/message-list";
import { TodoPanel } from "@/components/workspace/task-panel";
import { JobsPanel } from "@/components/workspace/jobs-panel";
import { SessionAgentsPanel } from "@/components/workspace/session-agents-panel";
import { FilePreviewDialog } from "@/components/workspace/file-preview-dialog";
import { SubagentSessionDialog } from "@/components/workspace/trajectory/subagent-session-dialog";
import {
  TrajectoryView,
  WorkspaceSkillsSurface,
} from "@/components/workspace/workspace-shell/lazy-surfaces";
import { type WorkspaceMainSectionProps } from "@/components/workspace/workspace-shell/main-section-props";
import { WorkspaceMainSectionCommandDialogs } from "@/components/workspace/workspace-shell/main-section-command-dialogs";
import { SessionInteractionDock } from "@/components/workspace/workspace-shell/session-interaction-dock";
import { NewThreadPlaceholder } from "@/components/workspace/workspace-shell/new-thread-placeholder";
import { useWorkspaceMainSectionController } from "@/components/workspace/workspace-shell/use-main-section-controller";
import { WorkspaceViewTabBar } from "@/components/workspace/workspace-shell/view-tab-bar";
import { WorkspaceShellTopbar } from "@/components/workspace/workspace-shell-topbar";
import { cn } from "@/lib/utils";

export function WorkspaceMainSection(props: WorkspaceMainSectionProps) {
  const {
    backtrackError,
    backtrackNavigationActive,
    backtrackNotice,
    backtrackPendingMessageId,
    backtrackSelectedMessageId,
    branchError,
    branchPendingMessageId,
    canBacktrack,
    composerAttachments,
    composerOverlayHeight,
    composerOverlayRef,
    connectionStatus,
    density,
    draft,
    earlierLoader,
    handleOpenArtifact,
    isCompact,
    isNewThread,
    isResponding,
    liveTeamCount,
    messageListStyle,
    modelOptions,
    newThreadSuggestions,
    onAnswerPendingQuestion,
    onBacktrackToMessage,
    onBranchFromMessage,
    onDraftChange,
    onModelChange,
    onPendingPermissionModeChange,
    onPendingPermissionModeConfirmChange,
    onPendingProfileChange,
    onPlanDecision,
    onPlanNotesChange,
    onProviderChange,
    onReasoningEffortChange,
    onRefreshSession,
    onResolvePendingApproval,
    onRetryConnection,
    onSelectBacktrackNavigationMessage,
    onSelectWorkspaceDirectory,
    onStopResponding,
    onSubmit,
    onToggleRightRail,
    openSettings,
    pendingInteraction,
    pendingPermissionMode,
    pendingProfileRef,
    phase,
    plan,
    planActionPending,
    planNotesDraft,
    planStatusLabel,
    providerOptions,
    reasoningEffortDefault,
    reasoningEffortError,
    reasoningEffortOptions,
    rightRailOpen,
    runtimeModelsError,
    runtimeModelsLoading,
    runtimeSkills,
    runtimeSkillsError,
    runtimeSkillsLoading,
    selectedModel,
    selectedProvider,
    selectedReasoningEffort,
    selectedThread,
    sessionRefreshing,
    setMobileSidebarOpen,
    setViewMode,
    streamStalled,
    t,
    threadStatusLabel,
    threadSubtitle,
    trajectoryEarlier,
    trajectoryStore,
    transportLabel,
    viewMode,
  } = props;
  const {
    agentBreadcrumb,
    agentTranscriptTarget,
    agentsPanelOpen,
    agentsSessionId,
    chatSurfaceVisible,
    composerCommandSurface,
    composerNotice,
    composerReferenceGroups,
    composerSkillGroups,
    dismissComposerNotice,
    filePreview,
    handleComposerMenuStateChange,
    handleProfileSwitch,
    jobs,
    jobsPanelOpen,
    jobsSessionId,
    parkedTurnView,
    runtimeProfileCatalog,
    sessionAgents,
    setAgentTranscriptTarget,
    setAgentsPanelOpen,
    setJobsPanelOpen,
    skillsSurfaceVisible,
    workspaceSelection,
  } = useWorkspaceMainSectionController(props);

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
        onOpenAgents={
          agentsSessionId ? () => setAgentsPanelOpen(true) : undefined
        }
        onOpenJobs={jobsSessionId ? () => setJobsPanelOpen(true) : undefined}
        onOpenSidebar={() => setMobileSidebarOpen(true)}
        onOpenSettings={() => openSettings("appearance")}
        onRefreshSession={onRefreshSession}
        onRetryConnection={onRetryConnection}
        onToggleRightRail={onToggleRightRail}
        rightRailOpen={rightRailOpen}
        sessionRefreshing={sessionRefreshing}
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
          onOpenTranscript={(target) => {
            // 下钻时先收起控制面弹层：两个弹层各自持有 Esc 生命周期
            // （useDialogLifecycle），叠加会出现「一次 Esc 关两层」的竞态；
            // 目录数据由 useSessionAgents 缓存，重开面板不会重复拉取。
            setAgentsPanelOpen(false);
            setAgentTranscriptTarget(target);
          }}
          open={agentsPanelOpen}
        />
      ) : null}

      <FilePreviewDialog
        composerInsetPx={composerOverlayHeight}
        preview={filePreview}
      />

      <SubagentSessionDialog
        onClose={() => setAgentTranscriptTarget(null)}
        target={agentTranscriptTarget}
      />

      <div
        className={cn(
          "flex min-h-0 flex-1 justify-center overflow-hidden",
          isNewThread
            ? "items-center pb-4 pt-14"
            : isCompact
              ? "pt-[2.95rem]"
              : "pt-[3.2rem]",
        )}
      >
        <div
          className={cn(
            // 批次 F2：本容器是宽度轴（--app-chat-content-width）的查询容器，
            // 让转录列 / 停靠卡 / 输入卡共享同一 64cqw 基准（不受滚动条宽度影响）。
            // 新会话不单独限宽或加水平内边距，确保其 composer 与会话使用相同宽度轴。
            "relative flex h-full min-h-0 w-full flex-col [container-type:inline-size]",
          )}
        >
          {!isNewThread ? (
            <div className="flex min-h-0 flex-1 flex-col overflow-hidden">
              <WorkspaceViewTabBar
                onSelectViewMode={setViewMode}
                t={t}
                trajectoryAvailable={Boolean(trajectoryStore)}
                viewMode={viewMode}
              />
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
                  branchError={branchError}
                  branchPendingMessageId={branchPendingMessageId}
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
                  earlierLoader={earlierLoader}
                  hasPendingApproval={Boolean(pendingInteraction)}
                  isResponding={isResponding}
                  messages={selectedThread.messages}
                  onBacktrackToMessage={onBacktrackToMessage}
                  onBranchFromMessage={onBranchFromMessage}
                  onPreviewFilePath={filePreview.open}
                  onRetryConnection={onRetryConnection}
                  onSelectBacktrackNavigationMessage={
                    onSelectBacktrackNavigationMessage
                  }
                  onSelectArtifact={handleOpenArtifact}
                  phase={phase}
                  scrollMemoryKey={
                    selectedThread.sessionId ?? selectedThread.id
                  }
                  streamStalled={streamStalled}
                  style={messageListStyle}
                  threadId={selectedThread.id}
                />
              ) : (
                <Suspense fallback={null}>
                  <TrajectoryView
                    className="h-full"
                    hasEarlier={trajectoryEarlier?.hasEarlier ?? false}
                    isLive={isResponding}
                    loadingEarlier={trajectoryEarlier?.loading ?? false}
                    onLoadEarlier={trajectoryEarlier?.onLoad}
                    sessionId={selectedThread.sessionId}
                    store={trajectoryStore}
                  />
                </Suspense>
              )}
            </div>
          ) : (
            <div className="flex min-h-0 flex-1 px-3 sm:px-4">
              <NewThreadPlaceholder
                onDraftChange={onDraftChange}
                suggestions={newThreadSuggestions}
                t={t}
              />
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
                {/* 任务面板（方案 §6.1）：浮在审批条与输入卡之上，不可见时自渲染为 null。 */}
                <TodoPanel
                  sessionId={selectedThread.sessionId}
                  snapshot={selectedThread.todoSnapshot}
                />
                {/* 批次 F2 + §4.6：停靠卡 = W − 32px（plan/托管状态行 + 待交互卡片，同一条宽度轴）。 */}
                <SessionInteractionDock
                  interaction={pendingInteraction ?? null}
                  onAnswerQuestion={onAnswerPendingQuestion}
                  onPlanDecision={onPlanDecision}
                  onPlanNotesChange={onPlanNotesChange}
                  onResolveApproval={onResolvePendingApproval}
                  parkedTurn={parkedTurnView}
                  plan={plan ?? null}
                  planActionPending={planActionPending}
                  planNotesDraft={planNotesDraft}
                  planStatusLabel={planStatusLabel}
                  sessionId={selectedThread.sessionId}
                />
                {/* 批次 F2：输入卡 = W + 32px（比转录列宽一档）。 */}
                <div className="pointer-events-auto mx-auto w-full max-w-[var(--app-chat-content-width-composer)]">
                  <MessageComposer
                    attachments={composerAttachments}
                    commands={composerCommandSurface.commands}
                    commandResultNotice={composerNotice}
                    contextUsageControl={
                      <ComposerContextUsageControl
                        isResponding={isResponding}
                        lastRuntimeEventType={
                          selectedThread.lastRuntimeEventType
                        }
                        runtimeEventCount={selectedThread.runtimeEventCount}
                        sessionId={selectedThread.sessionId}
                      />
                    }
                    density={density}
                    draft={draft}
                    focusKey={selectedThread.sessionId ?? selectedThread.id}
                    hasSession={Boolean(selectedThread.sessionId)}
                    isNewThread={isNewThread}
                    isResponding={isResponding}
                    modelOptions={modelOptions}
                    onMenuStateChange={handleComposerMenuStateChange}
                    reasoningEffortDefault={reasoningEffortDefault}
                    reasoningEffortError={reasoningEffortError}
                    reasoningEffortOptions={reasoningEffortOptions}
                    referenceGroups={composerReferenceGroups}
                    skillGroups={composerSkillGroups}
                    workspaceSelection={workspaceSelection}
                    onWorkspaceSelect={onSelectWorkspaceDirectory}
                    onModelChange={onModelChange}
                    onProviderChange={onProviderChange}
                    onReasoningEffortChange={onReasoningEffortChange}
                    permissionModeControl={
                      <ComposerPermissionModeControl
                        isNewThread={isNewThread}
                        lastRuntimeEventType={
                          selectedThread.lastRuntimeEventType
                        }
                        onPendingModeChange={onPendingPermissionModeChange}
                        onPendingModeConfirmChange={
                          onPendingPermissionModeConfirmChange
                        }
                        pendingMode={pendingPermissionMode}
                        runtimeEventCount={selectedThread.runtimeEventCount}
                        sessionId={selectedThread.sessionId}
                      />
                    }
                    profileControl={
                      <ComposerProfileControl
                        catalog={runtimeProfileCatalog.catalog}
                        catalogError={runtimeProfileCatalog.error}
                        isNewThread={isNewThread}
                        onSwitch={handleProfileSwitch}
                        onPendingProfileChange={onPendingProfileChange}
                        pendingProfileRef={pendingProfileRef}
                        profileName={selectedThread.profileName}
                        profileRef={selectedThread.profileRef}
                        sessionId={selectedThread.sessionId}
                      />
                    }
                    providerOptions={providerOptions}
                    runtimeModelsError={runtimeModelsError}
                    runtimeModelsLoading={runtimeModelsLoading}
                    selectedModel={selectedModel}
                    selectedProvider={selectedProvider}
                    selectedReasoningEffort={selectedReasoningEffort}
                    transport={selectedThread.transport}
                    onCommand={composerCommandSurface.onCommand}
                    onDismissCommandResult={dismissComposerNotice}
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
      <WorkspaceMainSectionCommandDialogs
        commandSurface={composerCommandSurface}
        onModelChange={onModelChange}
        profileCatalog={runtimeProfileCatalog}
        runtimeModelsError={runtimeModelsError}
        runtimeModelsLoading={runtimeModelsLoading}
        runtimeSkills={runtimeSkills}
        runtimeSkillsError={runtimeSkillsError}
        runtimeSkillsLoading={runtimeSkillsLoading}
        selectedModel={selectedModel}
        selectedProvider={selectedProvider}
      />
    </section>
  );
}
