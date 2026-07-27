import { ArrowUpIcon, SquareIcon } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/select";
import { type Thread } from "@/data/mock";
import { cn } from "@/lib/utils";
import { useTranslation } from "react-i18next";

type MessageComposerProps = {
  density: "comfortable" | "compact";
  draft: string;
  hasSession: boolean;
  isNewThread?: boolean;
  isResponding: boolean;
  modelOptions: string[];
  onModelChange: (value: string) => void;
  onProviderChange: (value: string) => void;
  providerOptions: string[];
  runtimeModelsError: string | null;
  runtimeModelsLoading: boolean;
  selectedArtifactCount: number;
  selectedModel: string;
  selectedProvider: string;
  transport?: Thread["transport"];
  onDraftChange: (value: string) => void;
  onStop: () => void;
  onSubmit: () => void;
};

export function MessageComposer({
  density,
  draft,
  hasSession,
  isNewThread = false,
  isResponding,
  modelOptions,
  onModelChange,
  onProviderChange,
  providerOptions,
  runtimeModelsError,
  runtimeModelsLoading,
  selectedArtifactCount,
  selectedModel,
  selectedProvider,
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
  const providerSelectOptions = providerOptions.map((provider) => ({
    value: provider,
    label: provider,
  }));
  const modelSelectOptions = modelOptions.map((model) => ({
    value: model,
    label: model,
  }));
  const runtimeModelStatusLabel = runtimeModelsLoading
    ? t("composer.loadingModels")
    : !showModelPicker && !runtimeModelsError
      ? t("composer.runtimeDefaultModel")
      : null;
  const showStatusRow =
    transport === "error" || selectedArtifactCount > 0 || isResponding;

  return (
    <div className="rounded-[0.95rem] border border-[var(--border)] [background:var(--workspace-composer-bg)] shadow-[0_8px_24px_rgba(0,0,0,0.18)]">
      {showStatusRow ? (
        <div
          className={cn(
            "flex flex-wrap items-center gap-x-2 gap-y-1 border-b border-[var(--border)] px-3 app-text-10 uppercase tracking-[0.12em] text-[var(--muted-foreground)]",
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
            <span className="text-[var(--accent-secondary)]">
              {t("composer.responseActive")}
            </span>
          ) : null}
        </div>
      ) : null}

      <div>
        <textarea
          value={draft}
          onChange={(event) => onDraftChange(event.target.value)}
          onKeyDown={(event) => {
            if ((event.metaKey || event.ctrlKey) && event.key === "Enter") {
              event.preventDefault();
              if (isResponding) {
                onStop();
                return;
              }
              onSubmit();
            }
          }}
          placeholder={placeholder}
          className={cn(
            "app-chat-input w-full resize-none bg-transparent text-[var(--foreground)] outline-none",
            isNewThread
              ? "min-h-[7rem] px-3.5 py-3.5"
              : isCompact
                ? "min-h-[4.25rem] px-3 py-2.5"
                : "min-h-[5rem] px-3.5 py-3",
          )}
        />
        <div
          className={cn(
            "flex items-center justify-between gap-2 border-t border-[var(--border)] px-3",
            isCompact ? "py-1.5" : "py-2",
          )}
        >
          <div className="min-w-0 flex flex-wrap items-center gap-2 app-text-9 uppercase tracking-[0.12em] text-[var(--muted-foreground)]">
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
            {runtimeModelStatusLabel ? (
              <span className="truncate">{runtimeModelStatusLabel}</span>
            ) : null}
            {runtimeModelsError ? (
              <>
                <span className="size-1 shrink-0 rounded-full bg-[#d8a66d]/40" />
                <span className="truncate text-[#d8a66d]">{runtimeModelsError}</span>
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
                ? "size-8 shrink-0 border-[var(--accent-secondary-border)] bg-[var(--accent-secondary-soft)] p-0 text-[var(--foreground)] shadow-none hover:border-[var(--accent-secondary-border)] hover:bg-[var(--accent-secondary-soft)]"
                : "size-8 shrink-0 border-[var(--border)] bg-[var(--surface-soft)] p-0 text-[var(--foreground)] shadow-none hover:border-[var(--border-strong)] hover:bg-[var(--surface-soft-hover)]"
            }
            onClick={isResponding ? onStop : onSubmit}
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
