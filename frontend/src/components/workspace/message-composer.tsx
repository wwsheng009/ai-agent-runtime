import { PlusIcon } from "lucide-react";
import { useEffect, useLayoutEffect, useRef, useState, useId } from "react";

import {
  ComposerAttachmentRail,
  ComposerDropInvitation,
} from "@/components/workspace/composer-attachment-rail";
import { ComposerCommandResultNoticeBar } from "@/components/workspace/composer-command-result-notice";
import { ComposerMenu } from "@/components/workspace/composer-menu";
import { ComposerModelPanel } from "@/components/workspace/composer-model-panel";
import { ComposerStatusRow } from "@/components/workspace/composer-status-row";
import { ComposerSurfaceProvider } from "@/components/workspace/composer-surface-context";
import { useComposerSurface } from "@/components/workspace/composer-surface-store";
import { ComposerTrailingActions } from "@/components/workspace/composer-trailing-actions";
import { type MessageComposerProps } from "@/components/workspace/message-composer-types";
import { useComposerMenu } from "@/hooks/workspace/composer/use-composer-menu";
import { useComposerMenuArbitration } from "@/hooks/workspace/composer/use-composer-menu-arbitration";
import { type ComposerCommandDefinition } from "@/lib/composer-commands";
import { type ComposerReferenceGroup } from "@/lib/composer-menu";
import { resolveComposerSubmitCopy } from "@/lib/composer/composer-submit-copy";
import { applyComposerTextareaLayout } from "@/lib/composer-textarea";
import { cn } from "@/lib/utils";
import { useTranslation } from "react-i18next";

// 稳定引用：默认空值不参与 memo 失效（否则每次渲染都重建菜单快照）。
const NO_COMMANDS: readonly ComposerCommandDefinition[] = [];
const NO_REFERENCE_GROUPS: readonly ComposerReferenceGroup[] = [];

export function MessageComposer(props: MessageComposerProps) {
  return (
    <ComposerSurfaceProvider>
      <MessageComposerSurface {...props} />
    </ComposerSurfaceProvider>
  );
}

function MessageComposerSurface({
  attachments,
  commands = NO_COMMANDS,
  commandResultNotice = null,
  contextUsageControl = null,
  density,
  draft,
  focusKey,
  hasSession,
  isNewThread = false,
  isResponding,
  modelOptions,
  onCommand,
  onDismissCommandResult,
  onModelChange,
  onMenuStateChange,
  onProviderChange,
  onReasoningEffortChange,
  permissionModeControl = null,
  profileControl = null,
  providerOptions,
  reasoningEffortDefault,
  reasoningEffortError,
  reasoningEffortOptions,
  runtimeModelsError,
  runtimeModelsLoading,
  referenceGroups = NO_REFERENCE_GROUPS,
  skillGroups = NO_REFERENCE_GROUPS,
  selectedModel,
  selectedProvider,
  selectedReasoningEffort,
  transport,
  workspaceSelection,
  onWorkspaceSelect,
  onDraftChange,
  onStop,
  onSubmit,
}: MessageComposerProps) {
  const { t } = useTranslation("workspace");
  // P1-1：输入菜单也纳入统一浮层仲裁（同一时刻只允许一个 expanded）。
  const menuSurface = useComposerSurface("menu");
  const isCompact = density === "compact";
  const placeholder = isNewThread
    ? t("composer.placeholder.newThread")
    : t("composer.placeholder.thread");
  // P2-3 / P2-5：发送按钮文案与 disabled 原因（纯结算，见 lib/composer）。
  const { label: submitButtonLabel, title: submitButtonTitle } =
    resolveComposerSubmitCopy(
      {
        hasSession,
        isNewThread,
        isResponding,
        draftEmpty: !draft.trim(),
        unsettledAttachmentCount: attachments.unsettledCount,
      },
      t,
    );
  const submitDisabled =
    !isResponding && (!draft.trim() || attachments.unsettledCount > 0);
  const submitDisabledHintId = useId();
  const submitDisabledHint =
    attachments.unsettledCount > 0
      ? t("composer.submit.disabledAttachments")
      : t("composer.submit.disabledEmpty");
  const showModelPicker = modelOptions.length > 0;
  const runtimeModelStatusLabel = runtimeModelsLoading
    ? t("composer.loadingModels")
    : !showModelPicker && !runtimeModelsError
      ? t("composer.runtimeDefaultModel")
      : null;
  const textareaRef = useRef<HTMLTextAreaElement | null>(null);
  const fileInputRef = useRef<HTMLInputElement | null>(null);
  // 提交被拦下的可见原因（见 handleSubmit）：静默 return false 会让按钮
  // 表现为「点了没反应」。按会话键记录而非布尔值：会话切换后提示自动失效，
  // 无需在 effect 里 setState（react-hooks/set-state-in-effect）。
  const [submitBlocked, setSubmitBlocked] = useState<{
    key: string;
    reason: "in-flight" | "attachments" | "no-session";
  } | null>(null);
  const submitBlockedHere =
    submitBlocked !== null && submitBlocked.key === (focusKey ?? "");

  // P1-4 子片 3：`/` 命令、`@` 引用与 `+` 按钮共用同一份触发菜单。
  const menu = useComposerMenu({
    value: draft,
    commands,
    referenceGroups,
    skillGroups,
    hasAttachAction: true,
    attachLabel: t("composer.attachments.attach"),
    workspaceSelection,
    onWorkspaceSelect,
    onValueChange: onDraftChange,
    onAttachRequest: () => fileInputRef.current?.click(),
    ...(onMenuStateChange ? { onMenuStateChange } : {}),
    onCommand,
  });
  useComposerMenuArbitration(menu, menuSurface);

  const commandNoticeText = menu.notice
    ? menu.notice.kind === "unknown-command"
      ? t("composer.commands.unknown", { name: menu.notice.name })
      : menu.notice.kind === "incomplete-command"
        ? t("composer.commands.incomplete")
        : t("composer.commands.noExecutor", { name: menu.notice.name })
    : null;

  // P1-4：草稿超过 14 行时封顶并在输入框内滚动（页面布局不被撑高）。
  useLayoutEffect(() => {
    const element = textareaRef.current;
    if (element) {
      applyComposerTextareaLayout(element);
    }
  }, [draft, density, isNewThread]);

  // 宽度变化会改变换行位置，需要按新排版重新度量高度。
  useEffect(() => {
    function handleResize() {
      const element = textareaRef.current;
      if (element) {
        applyComposerTextareaLayout(element);
      }
    }
    window.addEventListener("resize", handleResize);
    return () => window.removeEventListener("resize", handleResize);
  }, []);

  // P1-4：会话切换（focusKey 变化）与挂载后输入框回焦，保证切会话即可直接输入。
  useEffect(() => {
    textareaRef.current?.focus();
  }, [focusKey]);

  function focusInput() {
    textareaRef.current?.focus();
  }

  function handleSubmit() {
    // S5 门禁：附件仍在途/失败 → 明确提示并按既有「拦下必须可见」语义呈现，
    // 既不静默丢弃，也不把未上传项发出去。
    if (attachments.unsettledCount > 0) {
      setSubmitBlocked({ key: focusKey ?? "", reason: "attachments" });
      focusInput();
      return;
    }
    // 附件只挂在已落库会话上（submit_prompt.images 需要 session id）：
    // 新线程先发一条文字消息创建会话，这里如实拦下而不是丢弃图片。
    if (!hasSession && attachments.uploadedCount > 0) {
      setSubmitBlocked({ key: focusKey ?? "", reason: "no-session" });
      focusInput();
      return;
    }
    // 命令行永不静默降级为普通 prompt：未知/不完整命令阻塞提交并给出可见原因。
    const classification = menu.classifySubmit();
    if (
      classification.kind === "unknown-command" ||
      classification.kind === "incomplete-command"
    ) {
      menu.reportBlocked(classification);
      focusInput();
      return;
    }
    if (classification.kind === "command") {
      // P2-7：已认领的命令执行后清空命令行（结果回执由通知条承担，与菜单点选同语义）；
      // 未认领的命令保留草稿，便于用户修正命令名后重试。
      const handled = menu.dispatchCommand(
        classification.command,
        classification.args,
        "submit",
      );
      if (handled) {
        onDraftChange("");
      }
      focusInput();
      return;
    }
    // 只投递已上传成功的服务端路径；无附件时保持 onSubmit() 的旧调用形态。
    const images = attachments.uploadedPaths;
    const started = onSubmit(images.length > 0 ? { images } : undefined);
    if (started === false && draft.trim()) {
      // 提交闸门（同会话单飞）等前置条件把这次提交拦下了：给出可见原因，
      // 让用户知道「不是按钮坏了」。
      setSubmitBlocked({ key: focusKey ?? "", reason: "in-flight" });
    } else {
      setSubmitBlocked(null);
    }
    focusInput();
  }

  function handleStop() {
    onStop();
    focusInput();
  }

  return (
    <div
      className={cn(
        "relative isolate rounded-panel-lg border border-border [background:var(--surface-popover)] shadow-[var(--shadow-lv1)]",
        menu.open ? "z-50" : null,
      )}
    >
      <ComposerStatusRow
        hasCommandNotice={menu.notice !== null}
        isCommandLine={menu.commandLine}
        isCompact={isCompact}
        isResponding={isResponding}
        onAcknowledgeRejections={attachments.acknowledgeRejections}
        rejectedAttachmentCount={attachments.rejectedCount}
        transport={transport}
        unsettledAttachmentCount={attachments.unsettledCount}
        uploadedAttachmentCount={attachments.uploadedCount}
        uploadingAttachmentCount={attachments.uploadingCount}
      />

      <div>
        <ComposerAttachmentRail
          attachments={attachments.attachments}
          isCompact={isCompact}
          onRemove={attachments.removeAttachment}
          onRetry={attachments.retryAttachment}
        />
        {commandResultNotice ? (
          <ComposerCommandResultNoticeBar
            dismissLabel={t("composer.commands.dismiss")}
            notice={commandResultNotice}
            onDismiss={onDismissCommandResult}
          />
        ) : null}
        {menu.notice ? (
          <div
            role="alert"
            data-composer-command-notice={menu.notice.kind}
            className="flex items-start justify-between gap-2 px-3 pt-2 app-text-10 text-accent-gold"
          >
            <span>{commandNoticeText}</span>
            <button
              type="button"
              data-composer-command-notice-dismiss
              onClick={menu.dismissNotice}
              className="shrink-0 underline-offset-2 hover:underline"
            >
              {t("composer.commands.dismiss")}
            </button>
          </div>
        ) : null}
        {submitBlockedHere ? (
          <div
            role="alert"
            data-composer-submit-blocked={submitBlocked?.reason}
            className="flex items-start justify-between gap-2 px-3 pt-2 app-text-10 text-accent-gold"
          >
            <span>
              {submitBlocked?.reason === "attachments"
                ? t("composer.attachments.blockedUnsettled", {
                    count: attachments.unsettledCount,
                  })
                : submitBlocked?.reason === "no-session"
                  ? t("composer.attachments.needsSession")
                  : t("composer.submit.blockedInFlight")}
            </span>
            <button
              type="button"
              data-composer-submit-blocked-dismiss
              onClick={() => setSubmitBlocked(null)}
              className="shrink-0 underline-offset-2 hover:underline"
            >
              {t("composer.commands.dismiss")}
            </button>
          </div>
        ) : null}
        <input
          ref={fileInputRef}
          type="file"
          multiple
          tabIndex={-1}
          aria-hidden="true"
          data-composer-file-input
          className="hidden"
          onChange={(event) => {
            const files = event.target.files;
            if (files && files.length > 0) {
              attachments.addFiles(files);
            }
            // 允许同一次选择被再次触发（值不清空则 change 不重发）。
            event.target.value = "";
          }}
        />
        <div className="relative">
          <textarea
            ref={textareaRef}
            value={draft}
            role="combobox"
            aria-autocomplete="list"
            aria-expanded={menu.open}
            aria-controls={menu.open ? menu.listboxId : undefined}
            aria-activedescendant={menu.activeDescendantId ?? undefined}
            data-composer-command-line={menu.commandLine ? "true" : undefined}
            onChange={(event) => {
              const next = event.target.value;
              menu.handleValueChange(
                next,
                event.target.selectionStart ?? next.length,
              );
            }}
            onSelect={(event) => {
              menu.handleCaretChange(event.currentTarget.selectionStart ?? 0);
            }}
            onBlur={() => menu.close()}
            onPaste={(event) => {
              const files = event.clipboardData?.files;
              if (files && files.length > 0) {
                event.preventDefault();
                attachments.addFiles(files);
              }
            }}
            onKeyDown={(event) => {
              // 菜单打开时键盘所有权归菜单（Esc/↑/↓/Tab/Enter），否则走原生编辑行为。
              if (menu.handleKeyDown(event)) {
                return;
              }
              if ((event.metaKey || event.ctrlKey) && event.key === "Enter") {
                event.preventDefault();
                if (isResponding) {
                  handleStop();
                  return;
                }
                handleSubmit();
              }
            }}
            placeholder={placeholder}
            className={cn(
              "app-chat-input block w-full min-w-0 max-w-none resize-none bg-transparent text-foreground outline-none",
              isNewThread
                ? "min-h-[7rem] px-3.5 py-3.5 placeholder:text-transparent"
                : isCompact
                  ? "min-h-[4.25rem] px-3 py-2.5"
                  : "min-h-[5rem] px-3.5 py-3",
            )}
          />
          {isNewThread && draft.length === 0 ? (
            <span
              aria-hidden="true"
              data-composer-centered-placeholder
              className="app-chat-input pointer-events-none absolute inset-0 flex items-center px-3.5 py-3.5 text-muted-foreground"
            >
              {placeholder}
            </span>
          ) : null}
        </div>
        <div
          className={cn(
            "flex flex-wrap items-center justify-between gap-2 border-t border-border px-3",
            isCompact ? "py-1.5" : "py-2",
          )}
          data-composer-toolbar
        >
          <div className="min-w-0 flex flex-[1_1_32rem] flex-wrap items-center gap-2 app-text-9 uppercase tracking-[0.12em] text-muted-foreground">
            <div
              className="relative shrink-0"
              data-composer-menu-anchor
            >
              <button
                type="button"
                data-composer-menu-trigger
                aria-label={t("composer.menu.trigger")}
                aria-haspopup="listbox"
                aria-expanded={menu.open}
                title={t("composer.menu.trigger")}
                onClick={() => {
                  if (menu.open) {
                    menu.close();
                    return;
                  }
                  menuSurface.openSelf();
                  menu.openFromButton();
                }}
                className="inline-flex h-8 min-w-8 shrink-0 items-center justify-center rounded-field border border-border bg-surface-soft px-2 py-0 text-muted-foreground transition-colors hover:border-border-strong hover:text-foreground active:scale-[0.98] active:bg-surface-soft-hover"
              >
                <PlusIcon size={14} aria-hidden="true" />
                <span className="sr-only">{t("composer.menu.trigger")}</span>
              </button>
              {menu.open ? (
                <ComposerMenu
                  activeId={menu.activeId}
                  onBack={menu.backToRoot}
                  onHover={menu.hoverItem}
                  onSelect={menu.selectItem}
                  snapshot={menu.snapshot}
                />
              ) : null}
            </div>
            <div className="min-w-0 w-full max-w-[min(100%,28rem)] flex-[1_1_18rem]">
              <ComposerModelPanel
                disabled={runtimeModelsLoading || isResponding}
                disabledReason={
                  isResponding
                    ? t("composer.modelPanel.lockedWhileResponding")
                    : t("composer.loadingModels")
                }
                modelOptions={modelOptions}
                onModelChange={onModelChange}
                onProviderChange={onProviderChange}
                onReasoningEffortChange={onReasoningEffortChange}
                providerOptions={providerOptions}
                reasoningEffortDefault={reasoningEffortDefault}
                reasoningEffortOptions={reasoningEffortOptions}
                selectedModel={selectedModel}
                selectedProvider={selectedProvider}
                selectedReasoningEffort={selectedReasoningEffort}
              />
            </div>
            {permissionModeControl ? (
              <div className="min-w-0 flex-[0_1_10rem]">
                {permissionModeControl}
              </div>
            ) : null}
            {runtimeModelStatusLabel ? (
              <span className="min-w-0 max-w-[14rem] truncate">
                {runtimeModelStatusLabel}
              </span>
            ) : null}
            {runtimeModelsError ? (
              <>
                <span className="size-1 shrink-0 rounded-full bg-accent-gold/40" />
                <span className="min-w-0 max-w-[14rem] truncate text-accent-gold">
                  {runtimeModelsError}
                </span>
              </>
            ) : null}
            {reasoningEffortError ? (
              <>
                <span className="size-1 shrink-0 rounded-full bg-accent-gold/40" />
                <span className="min-w-0 max-w-[14rem] truncate text-accent-gold">
                  {reasoningEffortError}
                </span>
              </>
            ) : null}
          </div>
          <ComposerTrailingActions
            contextUsageControl={contextUsageControl}
            isResponding={isResponding}
            onSubmit={handleSubmit}
            onStop={handleStop}
            profileControl={profileControl}
            submitButtonLabel={submitButtonLabel}
            submitButtonTitle={submitButtonTitle}
            submitDisabled={submitDisabled}
            submitDisabledHint={submitDisabledHint}
            submitDisabledHintId={submitDisabledHintId}
          />
        </div>
      </div>
      {menu.open ? (
        <>
          <div
            aria-hidden="true"
            data-composer-menu-scrim
            onMouseDown={menu.close}
            className="pointer-events-auto fixed inset-0 z-30 [background:var(--composer-menu-scrim)]"
          />
        </>
      ) : null}
      <ComposerDropInvitation visible={attachments.isDragOver} />
    </div>
  );
}
