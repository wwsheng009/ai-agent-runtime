import { BotIcon, RouteIcon } from "lucide-react";
import { Link } from "react-router-dom";
import { Trans, useTranslation } from "react-i18next";

import { buttonVariants } from "@/components/ui/button-variants";
import { Select } from "@/components/ui/select";
import { useAppSettings, type ReasoningEffort } from "@/core/settings";
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
    return 10;
  }

  return Math.min(20, Math.max(1, Math.round(parsed)));
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
  const { settings, updateSection } = useAppSettings();
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
            icon={<BotIcon size={16} className="text-[var(--accent-primary)]" />}
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
            icon={<RouteIcon size={16} className="text-[var(--accent-secondary)]" />}
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
                    providerCount: providerOptions.length,
                    provider: selectedProvider || t("common.states.none"),
                    model: selectedModel || t("common.states.none"),
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
              <span className="text-[var(--foreground)]">
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
                <div className="text-base font-semibold text-[var(--foreground)]">
                  {option.label}
                </div>
                <p className="mt-1.5 text-base leading-6 text-[var(--muted-foreground)]">
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
              min={1}
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
            <p className="text-sm leading-6 text-[var(--muted-foreground)]">
              {t("chat.currentMaxSteps", { count: settings.chat.maxSteps })}{" "}
              {t("chat.maxStepsAdvice")}
            </p>
          </div>
        </SettingsPanelCard>
      </SettingsSection>
    </div>
  );
}
