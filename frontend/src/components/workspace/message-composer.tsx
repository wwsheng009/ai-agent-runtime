import { ArrowUpIcon, PlusIcon, SquareIcon } from "lucide-react";
import { type ReactNode, useEffect, useLayoutEffect, useRef } from "react";

import {
  ComposerAttachmentRail,
  ComposerDropInvitation,
} from "@/components/workspace/composer-attachment-rail";
import {
  ComposerCommandResultNoticeBar,
  type ComposerCommandResultNotice,
} from "@/components/workspace/composer-command-result-notice";
import { ComposerMenu } from "@/components/workspace/composer-menu";
import { ComposerModelPanel } from "@/components/workspace/composer-model-panel";
import { ComposerStatusRow } from "@/components/workspace/composer-status-row";
import { Button } from "@/components/ui/button";
import { type Thread } from "@/data/mock";
import { type ComposerAttachmentsController } from "@/hooks/workspace/composer/use-composer-attachments";
import { useComposerMenu } from "@/hooks/workspace/composer/use-composer-menu";
import { type ComposerCommand, type ComposerCommandDefinition } from "@/lib/composer-commands";
import { type ComposerReferenceGroup } from "@/lib/composer-menu";
import { applyComposerTextareaLayout } from "@/lib/composer-textarea";
import { cn } from "@/lib/utils";
import { useTranslation } from "react-i18next";

// 稳定引用：默认空值不参与 memo 失效（否则每次渲染都重建菜单快照）。
const NO_COMMANDS: readonly ComposerCommandDefinition[] = [];
const NO_REFERENCE_GROUPS: readonly ComposerReferenceGroup[] = [];

type MessageComposerProps = {
  /** P1-4 子片 2：附件草稿轨（数据与上传状态由 owner hook 持有）。 */
  attachments: ComposerAttachmentsController;
  /** P1-4 子片 3：斜杠命令表（内置命令清单与执行器归 P2-7）。 */
  commands?: readonly ComposerCommandDefinition[];
  /** P2-7：命令执行结果通知（宿主已本地化；错误 alert、成功 status）。 */
  commandResultNotice?: ComposerCommandResultNotice | null;
  density: "comfortable" | "compact";
  draft: string;
  /** 会话身份；变化（切换会话/新建线程落地）时输入框回焦。 */
  focusKey?: string;
  hasSession: boolean;
  isNewThread?: boolean;
  isResponding: boolean;
  modelOptions: string[];
  onModelChange: (value: string) => void;
  /** P1-4 子片 3：`/` 命令派发；返回 true 表示已处理（否则给出未接入提示，不降级为 prompt）。 */
  onCommand?: (
    command: ComposerCommand,
    args: string,
    source: "pick" | "submit",
  ) => boolean | void;
  onProviderChange: (value: string) => void;
  onDismissCommandResult?: () => void;
  onReasoningEffortChange: (value: string) => void;
  /** P1-x：会话权限选择器（由宿主注入，composer 不直接耦合 runtime API）。 */
  permissionModeControl?: ReactNode;
  providerOptions: string[];
  reasoningEffortDefault: string;
  reasoningEffortError: string | null;
  reasoningEffortOptions: string[];
  runtimeModelsError: string | null;
  runtimeModelsLoading: boolean;
  /** P1-4 子片 3：`@` 引用候选分组（文件/会话/子代理）。 */
  referenceGroups?: readonly ComposerReferenceGroup[];
  selectedArtifactCount: number;
  selectedModel: string;
  selectedProvider: string;
  selectedReasoningEffort: string;
  transport?: Thread["transport"];
  onDraftChange: (value: string) => void;
  onStop: () => void;
  onSubmit: () => void;
};

export function MessageComposer({
  attachments,
  commands = NO_COMMANDS,
  commandResultNotice = null,
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
  onProviderChange,
  onReasoningEffortChange,
  permissionModeControl = null,
  providerOptions,
  reasoningEffortDefault,
  reasoningEffortError,
  reasoningEffortOptions,
  runtimeModelsError,
  runtimeModelsLoading,
  referenceGroups = NO_REFERENCE_GROUPS,
  selectedArtifactCount,
  selectedModel,
  selectedProvider,
  selectedReasoningEffort,
  transport,
  onDraftChange,
  onStop,
  onSubmit,
}: MessageComposerProps) {
  const { t } = useTranslation("workspace");
  const isCompact = density === "compact";
  const placeholder = isNewThread
    ? t("composer.placeholder.newThread")
    : t("composer.placeholder.thread");
  const submitButtonLabel = isResponding
    ? t("composer.submit.stopResponse")
    : isNewThread
      ? t("composer.submit.startNewThread")
      : hasSession
        ? t("composer.submit.sendTurn")
        : t("composer.submit.startThread");
  const showModelPicker = modelOptions.length > 0;
  const runtimeModelStatusLabel = runtimeModelsLoading
    ? t("composer.loadingModels")
    : !showModelPicker && !runtimeModelsError
      ? t("composer.runtimeDefaultModel")
      : null;
  // 上传接口未就绪（§6.3 P2-1C）：附件只能停留在「待发送」，此时禁止提交，
  // 避免附件被静默丢弃。
  const hasPendingAttachments = attachments.attachments.length > 0;

  const textareaRef = useRef<HTMLTextAreaElement | null>(null);
  const fileInputRef = useRef<HTMLInputElement | null>(null);

  // P1-4 子片 3：`/` 命令、`@` 引用与 `+` 按钮共用同一份触发菜单。
  const menu = useComposerMenu({
    value: draft,
    commands,
    referenceGroups,
    hasAttachAction: true,
    attachLabel: t("composer.attachments.attach"),
    onValueChange: onDraftChange,
    onAttachRequest: () => fileInputRef.current?.click(),
    onCommand,
  });

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
    if (attachments.attachments.length > 0) {
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
    onSubmit();
    focusInput();
  }

  function handleStop() {
    onStop();
    focusInput();
  }

  return (
    <div className="relative rounded-panel-lg border border-border [background:var(--workspace-composer-bg)] shadow-[var(--shadow-lv1)]">
      <ComposerStatusRow
        hasCommandNotice={menu.notice !== null}
        isCommandLine={menu.commandLine}
        isCompact={isCompact}
        isResponding={isResponding}
        onAcknowledgeRejections={attachments.acknowledgeRejections}
        pendingAttachmentCount={attachments.attachments.length}
        rejectedAttachmentCount={attachments.rejectedCount}
        selectedArtifactCount={selectedArtifactCount}
        transport={transport}
      />

      <div>
        <ComposerAttachmentRail
          attachments={attachments.attachments}
          isCompact={isCompact}
          onRemove={attachments.removeAttachment}
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
            menu.handleValueChange(next, event.target.selectionStart ?? next.length);
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
            "app-chat-input w-full resize-none bg-transparent text-foreground outline-none",
            isNewThread
              ? "min-h-[7rem] px-3.5 py-3.5"
              : isCompact
                ? "min-h-[4.25rem] px-3 py-2.5"
                : "min-h-[5rem] px-3.5 py-3",
          )}
        />
        <div
          className={cn(
            "flex items-center justify-between gap-2 border-t border-border px-3",
            isCompact ? "py-1.5" : "py-2",
          )}
        >
          <div className="min-w-0 flex flex-wrap items-center gap-2 app-text-9 uppercase tracking-[0.12em] text-muted-foreground">
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
                menu.openFromButton();
              }}
              className="inline-flex shrink-0 items-center rounded-[0.6rem] border border-border bg-surface-soft px-2 py-1 text-muted-foreground transition-colors hover:border-border-strong hover:text-foreground"
            >
              <PlusIcon size={14} aria-hidden="true" />
              <span className="sr-only">
                {t("composer.menu.trigger")}
              </span>
            </button>
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
            {permissionModeControl}
            {runtimeModelStatusLabel ? (
              <span className="truncate">{runtimeModelStatusLabel}</span>
            ) : null}
            {runtimeModelsError ? (
              <>
                <span className="size-1 shrink-0 rounded-full bg-accent-gold/40" />
                <span className="truncate text-accent-gold">{runtimeModelsError}</span>
              </>
            ) : null}
            {reasoningEffortError ? (
              <>
                <span className="size-1 shrink-0 rounded-full bg-accent-gold/40" />
                <span className="truncate text-accent-gold">
                  {reasoningEffortError}
                </span>
              </>
            ) : null}
          </div>
          <Button
            variant="secondary"
            size="icon"
            aria-label={submitButtonLabel}
            title={`${submitButtonLabel} (${t("composer.shortcuts")})`}
            className={
              isResponding
                ? "size-8 shrink-0 border-accent-secondary-border bg-accent-secondary-soft p-0 text-foreground shadow-none hover:border-accent-secondary-border hover:bg-accent-secondary-soft"
                : "size-8 shrink-0 border-border bg-surface-soft p-0 text-foreground shadow-none hover:border-border-strong hover:bg-surface-soft-hover"
            }
            onClick={isResponding ? handleStop : handleSubmit}
            disabled={
              isResponding ? false : !draft.trim() || hasPendingAttachments
            }
          >
            {isResponding ? <SquareIcon size={14} /> : <ArrowUpIcon size={14} />}
            <span className="sr-only">{submitButtonLabel}</span>
          </Button>
        </div>
      </div>
      {menu.open ? (
        <ComposerMenu
          activeId={menu.activeId}
          onHover={menu.hoverItem}
          onSelect={menu.selectItem}
          snapshot={menu.snapshot}
        />
      ) : null}
      <ComposerDropInvitation visible={attachments.isDragOver} />
    </div>
  );
}
