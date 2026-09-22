import { type TFunction } from "i18next";

import { Badge } from "@/components/ui/badge";
import { Select } from "@/components/ui/select";

import { editorControlClassName } from "../editor-control-class";
import {
  agentRoutingDifficulties,
  agentRoutingTaskTypes,
  providerModelOptions,
  providerReasoningEffortOptions,
  type AgentRoutingDifficulty,
  type RuntimeAgentRoutingConfigSummary,
  type RuntimeAgentRoutingTaskTypeEntry,
} from "../runtime-agent-routing-domain-utils";
import { type RuntimeProviderSummary } from "../runtime-provider-config-utils";

import { difficultyLabel, taskTypeOptions } from "./format";
import { LabeledCell } from "./primitives";

const rowGridClassName =
  "grid gap-3 px-3 py-2 lg:grid-cols-[8rem_minmax(10rem,1fr)_minmax(12rem,1.2fr)_10rem] lg:items-center";

export function RoutingTaskTypesSection({
  config,
  inherited,
  onAddTaskType,
  onRemoveTaskType,
  onRenameTaskType,
  onUpdateTaskTypeProfile,
  providerOptions,
  providers,
  t,
}: {
  config: RuntimeAgentRoutingConfigSummary;
  inherited: boolean;
  onAddTaskType: () => void;
  onRemoveTaskType: (key: string) => void;
  onRenameTaskType: (key: string, nextKey: string) => void;
  onUpdateTaskTypeProfile: (
    key: string,
    difficulty: AgentRoutingDifficulty,
    field: "provider" | "model" | "reasoningEffort",
    value: string,
  ) => void;
  providerOptions: { value: string; label: string }[];
  providers: RuntimeProviderSummary[];
  t: TFunction<"runtimeConfig">;
}) {
  const keyOptions = taskTypeOptions(t);
  const usedKeys = new Set(config.taskTypes.map((entry) => entry.key));
  const hasFreeKey = agentRoutingTaskTypes.some((key) => !usedKeys.has(key));

  return (
    <div className="overflow-hidden rounded-card border border-border bg-surface-softer">
      <div className="flex flex-wrap items-start justify-between gap-2 border-b border-border bg-surface-solid px-3 py-2">
        <div className="min-w-0">
          <div className="text-xs font-semibold text-foreground">
            {t("editor.agentRouting.taskTypes.title")}
          </div>
          <div className="mt-0.5 text-xs leading-5 text-muted-foreground">
            {t("editor.agentRouting.taskTypes.description")}
          </div>
        </div>
        <button
          type="button"
          disabled={inherited || !hasFreeKey}
          onClick={onAddTaskType}
          className="rounded-[0.55rem] bg-accent-primary-soft px-2.5 py-1 text-xs font-medium text-foreground transition hover:bg-surface-soft disabled:opacity-50"
        >
          {t("editor.agentRouting.taskTypes.add")}
        </button>
      </div>

      {config.taskTypes.length === 0 ? (
        <div className="px-3 py-3 text-xs text-muted-foreground">
          {t("editor.agentRouting.taskTypes.empty")}
        </div>
      ) : null}

      <div className="divide-y divide-border">
        {config.taskTypes.map((entry) => (
          <TaskTypeEntryRows
            entry={entry}
            inherited={inherited}
            key={entry.key}
            keyOptions={keyOptions}
            onRemoveTaskType={onRemoveTaskType}
            onRenameTaskType={onRenameTaskType}
            onUpdateTaskTypeProfile={onUpdateTaskTypeProfile}
            providerOptions={providerOptions}
            providers={providers}
            t={t}
          />
        ))}
      </div>
    </div>
  );
}

function TaskTypeEntryRows({
  entry,
  inherited,
  keyOptions,
  onRemoveTaskType,
  onRenameTaskType,
  onUpdateTaskTypeProfile,
  providerOptions,
  providers,
  t,
}: {
  entry: RuntimeAgentRoutingTaskTypeEntry;
  inherited: boolean;
  keyOptions: { value: string; label: string }[];
  onRemoveTaskType: (key: string) => void;
  onRenameTaskType: (key: string, nextKey: string) => void;
  onUpdateTaskTypeProfile: (
    key: string,
    difficulty: AgentRoutingDifficulty,
    field: "provider" | "model" | "reasoningEffort",
    value: string,
  ) => void;
  providerOptions: { value: string; label: string }[];
  providers: RuntimeProviderSummary[];
  t: TFunction<"runtimeConfig">;
}) {
  return (
    <div className="px-3 py-3">
      <div className="flex flex-wrap items-center gap-2">
        {entry.legacy ? (
          <span className="font-semibold text-foreground">{entry.key}</span>
        ) : (
          <div className="w-44">
            <Select
              ariaLabel={t("editor.agentRouting.taskTypes.keyLabel")}
              disabled={inherited}
              options={keyOptions}
              value={entry.key}
              onChange={(value) => onRenameTaskType(entry.key, value)}
            />
          </div>
        )}
        {entry.legacy ? (
          <Badge className="normal-case tracking-normal">
            {t("editor.agentRouting.taskTypes.legacyBadge")}
          </Badge>
        ) : null}
        {entry.legacy ? (
          <span className="text-xs text-muted-foreground">
            {t("editor.agentRouting.taskTypes.legacyHint")}
          </span>
        ) : null}
        <button
          type="button"
          disabled={inherited}
          onClick={() => onRemoveTaskType(entry.key)}
          className="ml-auto rounded-[0.55rem] px-2 py-1 text-xs font-medium text-muted-foreground transition hover:bg-surface-soft hover:text-foreground disabled:opacity-50"
        >
          {t("editor.agentRouting.taskTypes.remove")}
        </button>
      </div>

      <div className="mt-2 overflow-hidden rounded-[0.55rem] border border-border">
        <div className="hidden grid-cols-[8rem_minmax(10rem,1fr)_minmax(12rem,1.2fr)_10rem] gap-3 border-b border-border bg-surface-solid px-3 py-1.5 text-xs font-semibold text-muted-foreground lg:grid">
          <div>{t("editor.agentRouting.columns.difficulty")}</div>
          <div>{t("editor.agentRouting.columns.provider")}</div>
          <div>{t("editor.agentRouting.columns.model")}</div>
          <div>{t("editor.agentRouting.columns.reasoning")}</div>
        </div>
        <div className="divide-y divide-border">
          {agentRoutingDifficulties.map((difficulty) => {
            const profile = entry.levels[difficulty];
            const modelOptions = providerModelOptions(
              providers,
              profile.provider,
              profile.model,
            );
            const reasoningEffortOptions = providerReasoningEffortOptions(
              providers,
              profile.provider,
              profile.model,
              profile.reasoningEffort,
            );
            return (
              <div key={difficulty} className={rowGridClassName}>
                <div className="font-medium text-foreground">
                  {difficultyLabel(t, difficulty)}
                </div>
                <LabeledCell label={t("editor.agentRouting.columns.provider")}>
                  <Select
                    ariaLabel={`${entry.key} ${difficulty} ${t("editor.agentRouting.columns.provider")}`}
                    disabled={inherited}
                    options={[
                      {
                        value: "",
                        label: t("editor.agentRouting.inheritPlaceholder"),
                      },
                      ...providerOptions,
                    ]}
                    placeholder={t("editor.agentRouting.inheritPlaceholder")}
                    value={profile.provider}
                    onChange={(value) =>
                      onUpdateTaskTypeProfile(
                        entry.key,
                        difficulty,
                        "provider",
                        value,
                      )
                    }
                  />
                </LabeledCell>
                <LabeledCell label={t("editor.agentRouting.columns.model")}>
                  {modelOptions.length > 0 ? (
                    <Select
                      ariaLabel={`${entry.key} ${difficulty} ${t("editor.agentRouting.columns.model")}`}
                      disabled={inherited}
                      options={[
                        {
                          value: "",
                          label: t("editor.agentRouting.inheritPlaceholder"),
                        },
                        ...modelOptions,
                      ]}
                      placeholder={t("editor.agentRouting.inheritPlaceholder")}
                      value={profile.model}
                      onChange={(value) =>
                        onUpdateTaskTypeProfile(
                          entry.key,
                          difficulty,
                          "model",
                          value,
                        )
                      }
                    />
                  ) : (
                    <input
                      className={editorControlClassName}
                      disabled={inherited}
                      placeholder={t("editor.agentRouting.modelPlaceholder")}
                      value={profile.model}
                      onChange={(event) =>
                        onUpdateTaskTypeProfile(
                          entry.key,
                          difficulty,
                          "model",
                          event.target.value,
                        )
                      }
                    />
                  )}
                </LabeledCell>
                <LabeledCell label={t("editor.agentRouting.columns.reasoning")}>
                  <Select
                    ariaLabel={`${entry.key} ${difficulty} ${t("editor.agentRouting.columns.reasoning")}`}
                    disabled={inherited}
                    options={[
                      {
                        value: "",
                        label: t("editor.agentRouting.inheritPlaceholder"),
                      },
                      ...reasoningEffortOptions,
                    ]}
                    value={profile.reasoningEffort}
                    onChange={(value) =>
                      onUpdateTaskTypeProfile(
                        entry.key,
                        difficulty,
                        "reasoningEffort",
                        value,
                      )
                    }
                  />
                </LabeledCell>
              </div>
            );
          })}
        </div>
      </div>
    </div>
  );
}
