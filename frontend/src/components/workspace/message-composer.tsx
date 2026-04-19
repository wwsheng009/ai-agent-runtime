import { useEffect, useRef, useState } from "react";

import {
  ArrowUpIcon,
  ChevronDownIcon,
  SquareIcon,
} from "lucide-react";

import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/select";
import { type Thread } from "@/data/mock";
import { cn } from "@/lib/utils";

type MessageComposerProps = {
  density: "comfortable" | "compact";
  draft: string;
  hasSession: boolean;
  isNewThread?: boolean;
  isResponding: boolean;
  modelOptions: string[];
  onModelChange: (value: string) => void;
  onProviderChange: (value: string) => void;
  prompts: Thread["prompts"];
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
  prompts,
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
  const isCompact = density === "compact";
  const promptMenuRef = useRef<HTMLDivElement | null>(null);
  const [promptMenuOpen, setPromptMenuOpen] = useState(false);
  const transportLabel =
    transport === "live" ? "live runtime" : transport === "error" ? "runtime error" : "seeded";
  const sessionStateLabel = hasSession ? "session attached" : "new session";
  const placeholder = isNewThread
    ? "Ask the workspace to inspect, build, review, or coordinate the next step..."
    : "Ask the workspace to research, change, verify, or coordinate the next step...";
  const submitButtonLabel = isResponding
    ? "Stop response"
    : isNewThread
      ? "Start new thread"
      : hasSession
        ? "Send turn"
        : "Start thread";
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
    ? "loading models"
    : runtimeModelsError
      ? "model catalog unavailable"
      : selectedModel
        ? `model ${selectedModel}`
        : "runtime default model";

  useEffect(() => {
    if (!promptMenuOpen) {
      return;
    }

    const handlePointerDown = (event: PointerEvent) => {
      const target = event.target;
      if (!(target instanceof Node)) {
        return;
      }

      if (!promptMenuRef.current?.contains(target)) {
        setPromptMenuOpen(false);
      }
    };

    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        setPromptMenuOpen(false);
      }
    };

    window.addEventListener("pointerdown", handlePointerDown);
    window.addEventListener("keydown", handleKeyDown);

    return () => {
      window.removeEventListener("pointerdown", handlePointerDown);
      window.removeEventListener("keydown", handleKeyDown);
    };
  }, [promptMenuOpen]);

  return (
    <div className="rounded-[0.95rem] border border-[var(--border)] [background:var(--workspace-composer-bg)] shadow-[0_8px_24px_rgba(0,0,0,0.18)]">
      <div
        className={cn(
          "relative border-b border-[var(--border)] px-3",
          isCompact ? "py-1" : "py-1.5",
        )}
      >
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1 app-text-10 uppercase tracking-[0.12em] text-[var(--muted-foreground)]">
          <span>{transportLabel}</span>
          <span className="size-1 rounded-full bg-[var(--border-strong)]" />
          <span>{selectedArtifactCount} files</span>
          <span className="size-1 rounded-full bg-[var(--border-strong)]" />
          <span>{sessionStateLabel}</span>
          {prompts.length > 0 ? (
            <>
              <span className="size-1 rounded-full bg-[var(--border-strong)]" />
              <div ref={promptMenuRef} className="relative">
                <button
                  type="button"
                  className={cn(
                    "inline-flex items-center gap-1 text-base uppercase tracking-[0.14em] transition",
                    promptMenuOpen
                      ? "text-[var(--foreground)]"
                      : "text-[var(--muted-foreground)] hover:text-[var(--foreground)]",
                  )}
                  onClick={() => setPromptMenuOpen((open) => !open)}
                  aria-expanded={promptMenuOpen}
                  aria-haspopup="menu"
                >
                  <span>prompt tips</span>
                  <span className="text-[var(--placeholder-foreground)]">
                    {prompts.length}
                  </span>
                  <ChevronDownIcon
                    size={12}
                    className={cn(
                      "transition-transform duration-150",
                      promptMenuOpen ? "rotate-180" : "rotate-0",
                    )}
                  />
                </button>

                {promptMenuOpen ? (
                  <div className="absolute left-0 top-full z-20 mt-2 w-80 max-w-[calc(100vw-4rem)] rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-overlay)] p-1.5 shadow-[0_10px_24px_rgba(0,0,0,0.24)]">
                    <div className="px-2 py-1 app-text-9 uppercase tracking-[0.12em] text-[var(--muted-foreground)]">
                      Prompt tips
                    </div>
                    <div className="flex max-h-64 flex-col overflow-y-auto">
                      {prompts.map((prompt) => (
                        <button
                          key={prompt}
                          type="button"
                          onClick={() => {
                            onDraftChange(prompt);
                            setPromptMenuOpen(false);
                          }}
                          className="rounded-[0.65rem] px-2.5 py-2 text-left text-base leading-6 text-[var(--muted-foreground)] transition hover:bg-[var(--surface-soft)] hover:text-[var(--foreground)]"
                        >
                          {prompt}
                        </button>
                      ))}
                    </div>
                  </div>
                ) : null}
              </div>
            </>
          ) : null}
          {isResponding ? (
            <>
              <span className="size-1 rounded-full bg-[var(--accent-secondary-border)]" />
              <span className="text-[var(--accent-secondary)]">response active</span>
            </>
          ) : null}
        </div>
      </div>

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
            isCompact
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
                <span>Provider</span>
                <Select
                  ariaLabel="Provider"
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
                <span>Model</span>
                <Select
                  ariaLabel="Model"
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
            <span className="truncate">{runtimeModelStatusLabel}</span>
            <span className="size-1 shrink-0 rounded-full bg-[var(--border-strong)]" />
            <span className="truncate">Ctrl/Cmd + Enter</span>
            <span className="size-1 shrink-0 rounded-full bg-[var(--border-strong)]" />
            <span className="truncate">{isResponding ? "stop" : "submit"}</span>
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
            title={submitButtonLabel}
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
