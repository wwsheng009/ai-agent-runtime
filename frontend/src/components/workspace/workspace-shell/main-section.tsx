// 由 components/workspace/workspace-shell.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { Suspense, useCallback, useMemo, useState } from "react";

import { setSessionProfile, type SessionProfileSwitchReport } from "@/api/runtime/profiles";
import { MessageComposer } from "@/components/workspace/message-composer";
import { ComposerContextUsageControl } from "@/components/workspace/composer-context-usage-control";
import { ComposerPermissionModeControl } from "@/components/workspace/composer-permission-mode-control";
import { MessageList } from "@/components/workspace/message-list";
import { TodoPanel } from "@/components/workspace/task-panel";
import { ComposerModelDialog } from "@/components/workspace/composer-model-dialog";
import { ComposerProfileDialog } from "@/components/workspace/composer-profile-dialog";
import { ComposerSkillDialog } from "@/components/workspace/composer-skill-dialog";
import { JobsPanel } from "@/components/workspace/jobs-panel";
import { SessionAgentsPanel } from "@/components/workspace/session-agents-panel";
import { agentDisplayName } from "@/components/workspace/session-agents-panel-shared";
import { FilePreviewDialog } from "@/components/workspace/file-preview-dialog";
import { SubagentSessionDialog } from "@/components/workspace/trajectory/subagent-session-dialog";
import type { SubagentSessionTarget } from "@/components/workspace/trajectory/subagent-session-target";
import {
  TrajectoryView,
  WorkspaceSkillsSurface,
} from "@/components/workspace/workspace-shell/lazy-surfaces";
import { type WorkspaceMainSectionProps } from "@/components/workspace/workspace-shell/main-section-props";
import { SessionInteractionDock } from "@/components/workspace/workspace-shell/session-interaction-dock";
import { NewThreadPlaceholder } from "@/components/workspace/workspace-shell/new-thread-placeholder";
import { WorkspaceViewTabBar } from "@/components/workspace/workspace-shell/view-tab-bar";
import { WorkspaceShellTopbar } from "@/components/workspace/workspace-shell-topbar";
import { useFilePreview } from "@/hooks/workspace/use-file-preview";
import { useBackgroundJobs } from "@/hooks/workspace/use-background-jobs";
import { useComposerFileReferences } from "@/hooks/workspace/composer/use-composer-file-references";
import { type ComposerMenuState } from "@/hooks/workspace/composer/use-composer-menu";
import { useComposerCommandSurface } from "@/hooks/workspace/composer/use-composer-command-surface";
import { createComposerSkillTurnRunner } from "@/hooks/workspace/composer/composer-skill-turn";
import { useRuntimeProfileCatalog } from "@/hooks/workspace/composer/use-runtime-profile-catalog";
import { useSessionAgents } from "@/hooks/use-session-agents";
import { useParkedTurnView } from "@/hooks/workspace/use-parked-turns";
import { type ComposerReferenceGroup } from "@/lib/composer-menu";
import { artifactReferenceGroup } from "@/lib/composer-references";
import { imageSubmitNoticeBanner } from "@/hooks/workspace/agent-chat-turn/image-prompt-turn";
import { cn } from "@/lib/utils";

export function WorkspaceMainSection({
  backtrackError,
  backtrackNavigationActive,
  backtrackNotice,
  backtrackPendingMessageId,
  backtrackSelectedMessageId,
  branchError,
  branchPendingMessageId,
  canBacktrack,
  composerAttachments,
  connectionStatus,
  draft,
  earlierLoader,
  imageSubmitFeedback,
  isResponding,
  modelOptions,
  onBacktrackToMessage,
  onBranchFromMessage,
  onDraftChange,
  onModelChange,
  onProviderChange,
  onReasoningEffortChange,
  onRenameRuntimeSession,
  onSelectBacktrackNavigationMessage,
  onStopResponding,
  onSubmit,
  parkedTurn,
  pendingInteraction,
  onResolvePendingApproval,
  onRefreshSession,
  onRetryConnection,
  onAnswerPendingQuestion,
  onPlanDecision,
  onPlanNotesChange,
  plan,
  planActionPending,
  planNotesDraft,
  planStatusLabel,
  phase,
  providerOptions,
  reasoningEffortDefault,
  reasoningEffortError,
  reasoningEffortOptions,
  runtimeModels,
  runtimeModelsError,
  runtimeModelsLoading,
  runtimeSkills,
  runtimeSkillsError,
  runtimeSkillsLoading,
  selectedModel,
  selectedProvider,
  selectedReasoningEffort,
  selectedThread,
  streamStalled,
  trajectoryStore,
  trajectoryEarlier,
  composerOverlayHeight,
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
  sessionRefreshing,
  setMobileSidebarOpen,
  setViewMode,
  t,
  threadStatusLabel,
  threadSubtitle,
  transportLabel,
  viewMode,
}: WorkspaceMainSectionProps) {
  // P0：`@` 引用候选 = 工作区文件（fs/roots 解析作用域 + fs/list 首屏小批量）+ 线程交付物兜底。
  // 菜单查询串只在 useComposerMenu 内部持有，这里通过 onMenuStateChange 回调同步出来。
  const [composerMenuState, setComposerMenuState] = useState<ComposerMenuState>({
    open: false,
    mode: "references",
    query: "",
  });
  const handleComposerMenuStateChange = useCallback((next: ComposerMenuState) => {
    setComposerMenuState((previous) =>
      previous.open === next.open && previous.mode === next.mode && previous.query === next.query
        ? previous
        : next,
    );
  }, []);
  const fileReferences = useComposerFileReferences({
    sessionId: selectedThread.sessionId,
    query: composerMenuState.query,
    enabled: composerMenuState.open && composerMenuState.mode === "references",
    labels: {
      group: t("composer.references.workspaceFiles"),
      loading: t("composer.references.workspaceFilesLoading"),
      empty: t("composer.references.workspaceFilesEmpty"),
      error: t("composer.references.workspaceFilesError"),
      truncated: t("composer.references.workspaceFilesTruncated"),
    },
  });

  // P1-4 子片 3：`@` 引用候选分组（工作区文件在前，线程交付物在后兜底）。
  const composerReferenceGroups = useMemo<ComposerReferenceGroup[]>(() => {
    const groups: ComposerReferenceGroup[] = [];
    if (fileReferences.group) {
      groups.push(fileReferences.group);
    }
    const files = artifactReferenceGroup(
      selectedThread.artifacts,
      t("composer.references.files"),
    );
    if (files) {
      groups.push(files);
    }
    return groups;
  }, [fileReferences.group, selectedThread.artifacts, t]);

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
  // §6.8 托管挂起：事件快照 + 任务投影（同一份 AgentControl 目录，挂起边沿补刷一次）。
  const parkedTurnView = useParkedTurnView(parkedTurn, sessionAgents.tree.descendants, sessionAgents.refresh);
  // G8：会话 agents 面板的只读下钻目标（复用子会话 transcript 对话框）。
  const [agentTranscriptTarget, setAgentTranscriptTarget] = useState<SubagentSessionTarget | null>(
    null,
  );
  const agentBreadcrumb = useMemo(
    () =>
      sessionAgents.tree.lineage.map((agent) => ({
        label: agentDisplayName(agent),
        path: agent.agentPath,
      })),
    [sessionAgents.tree.lineage],
  );

  // P2-1A：工具行文件路径的运行时预览（fs/read-file，只读）；未命中关联产物时兜底。
  const filePreview = useFilePreview({ sessionId: selectedThread.sessionId });

  // P2-7：composer `/` 命令面（清单 / `/model` 候选与弹窗 / 执行器 / 回执文案）；`/export`、
  // `/rename`、`/model` 复用轨迹导出、侧栏重命名与常驻座位选择器的同一实现，`/feedback`
  // 写本地日志（log-only）。目录未就绪时菜单无候选、弹窗如实显示空/失败态，不伪造模型名。
  // P2：`/skill` 回合化（构造器已抽到 composer-skill-turn.ts，行数门禁）。
  const handleRunSkillTurn = useMemo(
    () => createComposerSkillTurnRunner(onSubmit),
    [onSubmit],
  );

  // Batch 12：`/profile` 运行时目录（宿主数据源，一次拉取）+ 切换动作（单一处理器：
  // 命令行提交与弹窗点选都经命令执行器派发，回执文案一致）。
  const runtimeProfileCatalog = useRuntimeProfileCatalog();
  const handleProfileSwitch = useCallback(
    async (profileRef: string): Promise<SessionProfileSwitchReport> => {
      const targetSessionId = selectedThread.sessionId?.trim() ?? "";
      if (targetSessionId.length === 0) {
        // 新会话尚未登记：如实抛错（执行器转成回执），不把请求发给空 id。
        throw new Error("profile switch requires a registered session");
      }
      const result = await setSessionProfile(targetSessionId, profileRef);
      if (!result.ok) {
        throw new Error("profile switch was rejected by the runtime");
      }
      return result.report;
    },
    [selectedThread.sessionId],
  );
  const composerCommandSurface = useComposerCommandSurface({
    runtimeModels,
    runtimeSkills,
    runtimeProfiles: runtimeProfileCatalog.catalog,
    onProfileSwitch: handleProfileSwitch,
    onDraftChange,
    onModelChange,
    onRenameSession: onRenameRuntimeSession,
    sessionId: selectedThread.sessionId,
    onRunSkillTurn: handleRunSkillTurn,
  });

  // S5：带图发送（submit_prompt.images）回执与命令回执共用同一通知条，前者优先。
  const composerNotice =
    imageSubmitNoticeBanner(imageSubmitFeedback?.notice, t) ??
    composerCommandSurface.commandResult;
  const dismissComposerNotice = imageSubmitFeedback
    ? imageSubmitFeedback.dismissNotice
    : composerCommandSurface.onDismissCommandResult;

  // P2-1B：中部视图页签（对话 / 技能 / 轨迹）；技能与轨迹页签不承载输入框，只有对话面保留 composer。
  const skillsSurfaceVisible = !isNewThread && viewMode === "skills";
  const chatSurfaceVisible =
    !skillsSurfaceVisible &&
    (isNewThread || viewMode === "chat" || !trajectoryStore);

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

      <FilePreviewDialog composerInsetPx={composerOverlayHeight} preview={filePreview} />

      <SubagentSessionDialog
        onClose={() => setAgentTranscriptTarget(null)}
        target={agentTranscriptTarget}
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
            // 批次 F2：本容器是宽度轴（--app-chat-content-width）的查询容器，
            // 让转录列 / 停靠卡 / 输入卡共享同一 64cqw 基准（不受滚动条宽度影响）。
            "relative flex h-full min-h-0 w-full flex-col [container-type:inline-size]",
            isNewThread ? "max-w-[48rem]" : null,
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
                  className={cn("h-full px-3 sm:px-4 lg:px-5", isCompact ? "pt-3" : "pt-4")}
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
                  onSelectBacktrackNavigationMessage={onSelectBacktrackNavigationMessage}
                  onSelectArtifact={handleOpenArtifact}
                  phase={phase}
                  scrollMemoryKey={selectedThread.sessionId ?? selectedThread.id}
                  streamStalled={streamStalled}
                  style={messageListStyle}
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
            <NewThreadPlaceholder
              onDraftChange={onDraftChange}
              suggestions={newThreadSuggestions}
              t={t}
            />
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
                {/* 批次 F2 + §4.6：停靠卡 = W − 32px（模式标识 + 待交互卡片，同一条宽度轴）。 */}
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
                        lastRuntimeEventType={selectedThread.lastRuntimeEventType}
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
                    selectedArtifactCount={selectedThread.artifacts.length}
                    onModelChange={onModelChange}
                    onProviderChange={onProviderChange}
                    onReasoningEffortChange={onReasoningEffortChange}
                    permissionModeControl={
                      <ComposerPermissionModeControl
                        lastRuntimeEventType={selectedThread.lastRuntimeEventType}
                        runtimeEventCount={selectedThread.runtimeEventCount}
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
      <ComposerSkillDialog
        error={runtimeSkillsError}
        loading={runtimeSkillsLoading}
        onClose={composerCommandSurface.closeSkillDialog}
        onSelect={composerCommandSurface.selectSkill}
        open={composerCommandSurface.skillDialogOpen}
        skills={runtimeSkills?.skills.map((s) => s.name) ?? []}
      />
      <ComposerProfileDialog
        candidates={composerCommandSurface.profileCandidates}
        error={runtimeProfileCatalog.error}
        loading={runtimeProfileCatalog.loading}
        onClose={composerCommandSurface.closeProfileDialog}
        onRetry={runtimeProfileCatalog.refresh}
        onSelect={composerCommandSurface.applyProfile}
        open={composerCommandSurface.profileDialogOpen}
      />
    </section>
  );
}
