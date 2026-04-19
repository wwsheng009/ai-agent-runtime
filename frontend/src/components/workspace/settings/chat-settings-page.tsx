import { BotIcon, RouteIcon } from "lucide-react";
import { Link } from "react-router-dom";

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

const reasoningOptions: Array<{
  value: ReasoningEffort;
  label: string;
  description: string;
}> = [
  {
    value: "",
    label: "运行时默认",
    description: "把推理强度完全交给后端默认策略处理。",
  },
  {
    value: "minimal",
    label: "Minimal",
    description: "最省推理预算，适合简单追问和极短回合。",
  },
  {
    value: "low",
    label: "Low",
    description: "更快返回，适合普通问答与小改动。",
  },
  {
    value: "medium",
    label: "Medium",
    description: "兼顾速度和质量，适合绝大多数日常任务。",
  },
  {
    value: "high",
    label: "High",
    description: "更偏向复杂拆解和多步推理。",
  },
] as const;

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
  const { settings, updateSection } = useAppSettings();
  const providerSelectOptions = providerOptions.map((provider) => ({
    value: provider,
    label: provider,
  }));
  const modelSelectOptions = modelOptions.map((model) => ({
    value: model,
    label: model,
  }));

  return (
    <div className="space-y-6">
      <SettingsSection
        title="默认模型路由"
        description="这里修改的是工作区发送新回合时默认附带的 provider 和 model。"
      >
        <div className="grid gap-3 md:grid-cols-2">
          <SettingsFieldCard
            title="Provider"
            icon={<BotIcon size={16} className="text-[var(--accent-primary)]" />}
          >
            <Select
              ariaLabel="默认 Provider"
              value={selectedProvider}
              onChange={onProviderChange}
              options={providerSelectOptions}
              placeholder={
                runtimeModelsLoading ? "正在加载 provider..." : "暂无 provider"
              }
              disabled={runtimeModelsLoading || providerOptions.length === 0}
              className="w-full"
              triggerClassName="w-full text-sm"
              optionClassName="text-sm"
            />
          </SettingsFieldCard>

          <SettingsFieldCard
            title="Model"
            icon={<RouteIcon size={16} className="text-[var(--accent-secondary)]" />}
          >
            <Select
              ariaLabel="默认 Model"
              value={selectedModel}
              onChange={onModelChange}
              options={modelSelectOptions}
              placeholder={
                runtimeModelsLoading ? "正在加载模型..." : "当前 provider 没有可选模型"
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
                ? "运行时模型目录加载中。"
                : `当前已识别 ${providerOptions.length} 个 provider，当前默认会话路由到 ${selectedProvider || "runtime default"} / ${selectedModel || "runtime default"}。`
          }
        />

        <div className="flex flex-wrap items-center gap-2">
          <Link
            to="/runtime/config"
            className={cn(buttonVariants({ variant: "secondary", size: "sm" }))}
          >
            打开后端配置页
          </Link>
          <Link
            to="/runtime/config"
            className={cn(buttonVariants({ variant: "ghost", size: "sm" }))}
          >
            管理 Provider 列表
          </Link>
        </div>
      </SettingsSection>

      <SettingsSection
        title="执行模式"
        description="控制工作区聊天是否进入后端 ReAct 工具循环。"
      >
        <SettingsToggleCard
          checked={settings.chat.enableReact}
          onChange={(checked) =>
            updateSection("chat", {
              enableReact: checked,
            })
          }
          title="启用 ReAct 工具循环"
          description={
            <>
              开启后，请求会携带 <code>enable_react: true</code>，后端会把工具定义暴露给模型并进入工具调用循环。
              关闭后，仍可做 skill route 或直接 LLM fallback，但模型本身不会触发工具调用。
            </>
          }
        />

        <SettingsInfoCard
          size="compact"
          description={
            <>
              当前模式:{" "}
              <span className="text-[var(--foreground)]">
                {settings.chat.enableReact ? "ReAct 工具模式" : "路由 / 直连模式"}
              </span>
              。
            </>
          }
        />
      </SettingsSection>

      <SettingsSection
        title="推理强度"
        description="这些值会在发送到 `/api/agent/chat` 的请求里携带。"
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
        title="最大步骤数"
        description="用于限制单轮里最多允许的规划 / 路由 / 工具执行步数。"
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
              当前值为 <span className="text-[var(--foreground)]">{settings.chat.maxSteps}</span>。
              通常 8 到 12 足够覆盖常见工作区任务，复杂编排可以提高到 15 到 20。
            </p>
          </div>
        </SettingsPanelCard>
      </SettingsSection>
    </div>
  );
}
