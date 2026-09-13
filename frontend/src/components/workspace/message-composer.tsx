import { ArrowUpIcon, SquareIcon } from "lucide-react";
import { useEffect, useLayoutEffect, useRef } from "react";

import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/select";
import { type Thread } from "@/data/mock";
import { applyComposerTextareaLayout } from "@/lib/composer-textarea";
import { cn } from "@/lib/utils";
import { useTranslation } from "react-i18next";

type MessageComposerProps = {
  density: "comfortable" | "compact";
  draft: string;
  /** 会话身份；变化（切换会话/新建线程落地）时输入框回焦。 */
  focusKey?: string;
  hasSession: boolean;
  isNewThread?: boolean;
  isResponding: boolean;
  modelOptions: string[];
  onModelChange: (value: string) => void;
  onProviderChange: (value: string) => void;
  onReasoningEffortChange: (value: string) => void;
  providerOptions: string[];
  reasoningEffortDefault: string;
  reasoningEffortError: string | null;
  reasoningEffortOptions: string[];
  runtimeModelsError: string | null;
  runtimeModelsLoading: boolean;
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
  density,
  draft,
  focusKey,
  hasSession,
  isNewThread = false,
  isResponding,
  modelOptions,
  onModelChange,
  onProviderChange,
  onReasoningEffortChange,
  providerOptions,
  reasoningEffortDefault,
  reasoningEffortError,
  reasoningEffortOptions,
  runtimeModelsError,
  runtimeModelsLoading,
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
  const showProviderPicker = providerOptions.length > 1;
  const showModelPicker = modelOptions.length > 0;
  const showReasoningEffortPicker = reasoningEffortOptions.length > 0;
  const providerSelectOptions = providerOptions.map((provider) => ({
    value: provider,
    label: provider,
  }));
  const modelSelectOptions = modelOptions.map((model) => ({
    value: model,
    label: model,
  }));
  const reasoningEffortSelectOptions = [
    {
      value: "",
      label: reasoningEffortDefault
        ? t("composer.reasoningDefaultWithValue", {
            effort: reasoningEffortDefault,
          })
        : t("composer.reasoningDefault"),
    },
    ...reasoningEffortOptions.map((effort) => ({
      value: effort,
      label: effort,
    })),
  ];
  const runtimeModelStatusLabel = runtimeModelsLoading
    ? t("composer.loadingModels")
    : !showModelPicker && !runtimeModelsError
      ? t("composer.runtimeDefaultModel")
      : null;
  const showStatusRow =
    transport === "error" || selectedArtifactCount > 0 || isResponding;

  const textareaRef = useRef<HTMLTextAreaElement | null>(null);

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
    onSubmit();
    focusInput();
  }

  function handleStop() {
    onStop();
    focusInput();
  }

  return (
    <div className="rounded-panel-lg border border-border [background:var(--workspace-composer-bg)] shadow-[0_8px_24px_rgba(0,0,0,0.18)]">
      {showStatusRow ? (
        <div
          className={cn(
            "flex flex-wrap items-center gap-x-2 gap-y-1 border-b border-border px-3 app-text-10 uppercase tracking-[0.12em] text-muted-foreground",
            isCompact ? "py-1" : "py-1.5",
          )}
        >
          {transport === "error" ? (
            <span className="text-[#d8a66d]">{t("composer.transport.error")}</span>
          ) : null}
          {selectedArtifactCount > 0 ? (
            <span>{t("composer.filesCount", { count: selectedArtifactCount })}</span>
          ) : null}
          {isResponding ? (
            <span className="text-accent-secondary">
              {t("composer.responseActive")}
            </span>
          ) : null}
        </div>
      ) : null}

      <div>
        <textarea
          ref={textareaRef}
          value={draft}
          onChange={(event) => onDraftChange(event.target.value)}
          onKeyDown={(event) => {
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
            {showProviderPicker ? (
              <label className="inline-flex items-center gap-1.5">
                <span>{t("composer.provider")}</span>
                <Select
                  ariaLabel={t("composer.provider")}
                  value={selectedProvider}
                  onChange={onProviderChange}
                  options={providerSelectOptions}
                  disabled={runtimeModelsLoading || isResponding}
                  side="top"
                  triggerClassName="min-w-[7rem] max-w-[12rem] rounded-[0.6rem] px-2 py-1 text-base leading-none"
                  menuClassName="max-w-[14rem]"
                  optionClassName="text-base"
                />
              </label>
            ) : null}
            {showModelPicker ? (
              <label className="inline-flex items-center gap-1.5">
                <span>{t("composer.model")}</span>
                <Select
                  ariaLabel={t("composer.model")}
                  value={selectedModel}
                  onChange={onModelChange}
                  options={modelSelectOptions}
                  disabled={runtimeModelsLoading || isResponding}
                  side="top"
                  triggerClassName="min-w-[9rem] max-w-[16rem] rounded-[0.6rem] px-2 py-1 text-base leading-none"
                  menuClassName="max-w-[18rem]"
                  optionClassName="text-base"
                />
              </label>
            ) : null}
            {showReasoningEffortPicker ? (
              <label className="inline-flex items-center gap-1.5">
                <span>{t("composer.reasoning")}</span>
                <Select
                  ariaLabel={t("composer.reasoning")}
                  value={selectedReasoningEffort}
                  onChange={onReasoningEffortChange}
                  options={reasoningEffortSelectOptions}
                  disabled={runtimeModelsLoading || isResponding}
                  side="top"
                  triggerClassName="min-w-[6rem] max-w-[11rem] rounded-[0.6rem] px-2 py-1 text-base leading-none"
                  menuClassName="max-w-[12rem]"
                  optionClassName="text-base"
                />
              </label>
            ) : null}
            {runtimeModelStatusLabel ? (
              <span className="truncate">{runtimeModelStatusLabel}</span>
            ) : null}
            {runtimeModelsError ? (
              <>
                <span className="size-1 shrink-0 rounded-full bg-[#d8a66d]/40" />
                <span className="truncate text-[#d8a66d]">{runtimeModelsError}</span>
              </>
            ) : null}
            {reasoningEffortError ? (
              <>
                <span className="size-1 shrink-0 rounded-full bg-[#d8a66d]/40" />
                <span className="truncate text-[#d8a66d]">
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
            disabled={isResponding ? false : !draft.trim()}
          >
            {isResponding ? <SquareIcon size={14} /> : <ArrowUpIcon size={14} />}
            <span className="sr-only">{submitButtonLabel}</span>
          </Button>
        </div>
      </div>
    </div>
  );
}
