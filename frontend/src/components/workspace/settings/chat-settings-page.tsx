import { BotIcon, RouteIcon } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router-dom";
import { Trans, useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { buttonVariants } from "@/components/ui/button-variants";
import { Select } from "@/components/ui/select";
import { useAppSettings, type ReasoningEffort } from "@/core/settings";
import { useRuntimeAgentMaxSteps } from "@/hooks/workspace/use-runtime-agent-max-steps";
import { saveRuntimeAgentMaxSteps } from "@/lib/runtime-api";
import { cn } from "@/lib/utils";

import { SettingsChoiceCard } from "./settings-choice-card";
import { editorControlClassName } from "./editor-control-class";
import { SettingsFieldCard } from "./settings-field-card";
import { SettingsInfoCard } from "./settings-info-card";
import { SettingsPanelCard } from "./settings-panel-card";
import { SettingsSection } from "./settings-section";
import { SettingsToggleCard } from "./settings-toggle-card";

type ChatSettingsPageProps = {
  modelOptions: string[];
  onModelChange: (value: string) => void;
  onProviderChange: (value: string) => void;
  providerOptions: string[];
  runtimeModelsError: string | null;
  runtimeModelsLoading: boolean;
  selectedModel: string;
  selectedProvider: string;
};

function clampMaxSteps(value: string) {
  const parsed = Number(value);
  if (!Number.isFinite(parsed)) {
    return 0;
  }

  return Math.min(20, Math.max(0, Math.round(parsed)));
}

export function ChatSettingsPage({
  modelOptions,
  onModelChange,
  onProviderChange,
  providerOptions,
  runtimeModelsError,
  runtimeModelsLoading,
  selectedModel,
  selectedProvider,
}: ChatSettingsPageProps) {
  const { t } = useTranslation("settings");
  const { t: tCommon } = useTranslation("common");
  const { settings, updateSection } = useAppSettings();
  const {
    error: backendMaxStepsError,
    loading: backendMaxStepsLoading,
    snapshot: backendMaxSteps,
  } = useRuntimeAgentMaxSteps();
  const [savingMaxSteps, setSavingMaxSteps] = useState(false);
  const [maxStepsStatus, setMaxStepsStatus] = useState<{
    tone: "success" | "error";
    text: string;
  } | null>(null);

  // 本地设置改完立即对下一轮生效（请求带上 max_steps），点保存再把后端缺省值
  // （runtime 内存快照 + 配置文件里的 agent.maxSteps）一起改掉。
  async function handleSaveMaxSteps() {
    setSavingMaxSteps(true);
    setMaxStepsStatus(null);
    try {
      const saved = await saveRuntimeAgentMaxSteps({
        max_steps: settings.chat.maxSteps,
      });
      setMaxStepsStatus({
        tone: "success",
        text: t("chat.maxStepsSaved", {
          count: saved.max_steps,
          path: saved.config_file,
        }),
      });
    } catch (error) {
      setMaxStepsStatus({
        tone: "error",
        text: t("chat.maxStepsSaveFailed", {
          message: error instanceof Error ? error.message : String(error),
        }),
      });
    } finally {
      setSavingMaxSteps(false);
    }
  }

  const providerSelectOptions = providerOptions.map((provider) => ({
    value: provider,
    label: provider,
  }));
  const modelSelectOptions = modelOptions.map((model) => ({
    value: model,
    label: model,
  }));
  const reasoningOptions: Array<{
    value: ReasoningEffort;
    label: string;
    description: string;
  }> = [
    {
      value: "",
      label: t("chat.reasoningOptions.default.label"),
      description: t("chat.reasoningOptions.default.description"),
    },
    {
      value: "minimal",
      label: t("chat.reasoningOptions.minimal.label"),
      description: t("chat.reasoningOptions.minimal.description"),
    },
    {
      value: "low",
      label: t("chat.reasoningOptions.low.label"),
      description: t("chat.reasoningOptions.low.description"),
    },
    {
      value: "medium",
      label: t("chat.reasoningOptions.medium.label"),
      description: t("chat.reasoningOptions.medium.description"),
    },
    {
      value: "high",
      label: t("chat.reasoningOptions.high.label"),
      description: t("chat.reasoningOptions.high.description"),
    },
  ];

  return (
    <div className="space-y-6">
      <SettingsSection
        title={t("chat.title")}
        description={t("chat.description")}
      >
        <div className="grid gap-3 md:grid-cols-2">
          <SettingsFieldCard
            title={t("chat.defaultProvider")}
            icon={<BotIcon size={16} className="text-accent-primary" />}
          >
            <Select
              ariaLabel={t("chat.defaultProvider")}
              value={selectedProvider}
              onChange={onProviderChange}
              options={providerSelectOptions}
              placeholder={
                runtimeModelsLoading
                  ? t("chat.loadingProvider")
                  : t("chat.noProvider")
              }
              disabled={runtimeModelsLoading || providerOptions.length === 0}
              className="w-full"
              triggerClassName="w-full text-sm"
              optionClassName="text-sm"
            />
          </SettingsFieldCard>

          <SettingsFieldCard
            title={t("chat.defaultModel")}
            icon={<RouteIcon size={16} className="text-accent-secondary" />}
          >
            <Select
              ariaLabel={t("chat.defaultModel")}
              value={selectedModel}
              onChange={onModelChange}
              options={modelSelectOptions}
              placeholder={
                runtimeModelsLoading
                  ? t("chat.loadingModel")
                  : t("chat.noModel")
              }
              disabled={runtimeModelsLoading || modelOptions.length === 0}
              className="w-full"
              triggerClassName="w-full text-sm"
              optionClassName="text-sm"
            />
          </SettingsFieldCard>
        </div>

        <SettingsInfoCard
          size="compact"
          description={
            runtimeModelsError
              ? runtimeModelsError
              : runtimeModelsLoading
                ? t("chat.summaryLoading")
                : t("chat.summaryTemplate", {
                    providerCount: String(providerOptions.length),
                    provider: selectedProvider || tCommon("states.none"),
                    model: selectedModel || tCommon("states.none"),
                  })
          }
        />

        <div className="flex flex-wrap items-center gap-2">
          <Link
            to="/runtime/config"
            className={cn(buttonVariants({ variant: "secondary", size: "sm" }))}
          >
            {t("chat.openBackendConfig")}
          </Link>
          <Link
            to="/runtime/config"
            className={cn(buttonVariants({ variant: "ghost", size: "sm" }))}
          >
            {t("chat.manageProviders")}
          </Link>
        </div>
      </SettingsSection>

      <SettingsSection
        title={t("chat.executionMode")}
        description={t("chat.executionModeDescription")}
      >
        <SettingsToggleCard
          checked={settings.chat.enableReact}
          onChange={(checked) =>
            updateSection("chat", {
              enableReact: checked,
            })
          }
          title={t("chat.enableReact")}
          description={
            <Trans
              t={t}
              i18nKey="chat.enableReactDescription"
              components={{ code: <code /> }}
            />
          }
        />

        <SettingsInfoCard
          size="compact"
          description={
            <>
              {t("chat.currentMode")}:{" "}
              <span className="text-foreground">
                {settings.chat.enableReact
                  ? t("chat.reactMode")
                  : t("chat.routeDirectMode")}
              </span>
              。
            </>
          }
        />
      </SettingsSection>

      <SettingsSection
        title={t("chat.reasoning")}
        description={t("chat.reasoningDescription")}
      >
        <div className="grid gap-2.5 lg:grid-cols-2">
          {reasoningOptions.map((option) => {
            const active = settings.chat.reasoningEffort === option.value;

            return (
              <SettingsChoiceCard
                key={option.label}
                active={active}
                onClick={() =>
                  updateSection("chat", { reasoningEffort: option.value })
                }
              >
                <div className="text-base font-semibold text-foreground">
                  {option.label}
                </div>
                <p className="mt-1.5 text-base leading-6 text-muted-foreground">
                  {option.description}
                </p>
              </SettingsChoiceCard>
            );
          })}
        </div>
      </SettingsSection>

      <SettingsSection
        title={t("chat.maxSteps")}
        description={t("chat.maxStepsDescription")}
      >
        <SettingsPanelCard>
          <div className="flex flex-col gap-3 sm:flex-row sm:items-center">
            <input
              type="number"
              min={0}
              max={20}
              step={1}
              value={settings.chat.maxSteps}
              onChange={(event) =>
                updateSection("chat", {
                  maxSteps: clampMaxSteps(event.target.value),
                })
              }
              className={cn(editorControlClassName, "sm:max-w-[10rem]")}
            />
            <p className="text-sm leading-6 text-muted-foreground">
              {t("chat.currentMaxSteps", { count: settings.chat.maxSteps })}{" "}
              {t("chat.maxStepsAdvice")}
            </p>
            <Button
              className="sm:ml-auto"
              disabled={savingMaxSteps}
              onClick={() => void handleSaveMaxSteps()}
              size="sm"
              variant="secondary"
            >
              {savingMaxSteps
                ? t("chat.maxStepsSaving")
                : t("chat.maxStepsSave")}
            </Button>
          </div>
          <p className="mt-3 text-xs leading-6 text-muted-foreground">
            {t("chat.maxStepsSaveHint")}
          </p>
          {maxStepsStatus ? (
            <p
              className={cn(
                "mt-2 text-sm leading-6",
                maxStepsStatus.tone === "success"
                  ? "text-accent-teal"
                  : "text-accent-orange",
              )}
              role={maxStepsStatus.tone === "error" ? "alert" : "status"}
            >
              {maxStepsStatus.text}
            </p>
          ) : null}
          <div className="mt-3 flex flex-wrap items-center gap-x-2 gap-y-1">
            <p className="text-xs leading-6 text-muted-foreground">
              {backendMaxSteps
                ? t("chat.backendMaxStepsDefault", {
                    count: backendMaxSteps.maxSteps,
                    path: backendMaxSteps.configFile,
                  })
                : backendMaxStepsLoading
                  ? t("chat.backendMaxStepsLoading")
                  : t("chat.backendMaxStepsUnavailable", {
                      message: backendMaxStepsError ?? "",
                    })}
            </p>
            {backendMaxSteps ? (
              <Button
                size="sm"
                variant="ghost"
                onClick={() =>
                  updateSection("chat", {
                    maxSteps: clampMaxSteps(String(backendMaxSteps.maxSteps)),
                  })
                }
              >
                {t("chat.backendMaxStepsAdopt")}
              </Button>
            ) : null}
          </div>
          {backendMaxSteps && backendMaxSteps.maxSteps > 20 ? (
            <p className="mt-2 text-xs leading-6 text-accent-orange">
              {t("chat.backendMaxStepsClamped", {
                count: backendMaxSteps.maxSteps,
                applied: String(
                  clampMaxSteps(String(backendMaxSteps.maxSteps)),
                ),
              })}
            </p>
          ) : null}
          {backendMaxSteps &&
          backendMaxSteps.maxSteps > 0 &&
          settings.chat.maxSteps === 0 ? (
            <p className="mt-2 text-xs leading-6 text-accent-orange">
              {t("chat.backendMaxStepsShadowed", {
                count: backendMaxSteps.maxSteps,
              })}
            </p>
          ) : null}
        </SettingsPanelCard>
      </SettingsSection>
    </div>
  );
}
