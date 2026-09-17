import { BotIcon } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { useAppSettings } from "@/core/settings";
import { useRuntimeAgentMaxSteps } from "@/hooks/workspace/use-runtime-agent-max-steps";
import { saveRuntimeAgentMaxSteps } from "@/lib/runtime-api";
import { cn } from "@/lib/utils";

import { editorControlClassName } from "./editor-control-class";
import { SettingsInfoCard } from "./settings-info-card";
import { SettingsPanelCard } from "./settings-panel-card";

const maxStepsFallbackLimit = 100;

function clampMaxSteps(value: string, limit: number) {
  const parsed = Number(value);
  if (!Number.isFinite(parsed)) {
    return 0;
  }

  return Math.min(limit, Math.max(0, Math.round(parsed)));
}

/**
 * 「服务端缺省最大步骤数」卡片：读写 runtime 配置里的 `agent.maxSteps`。
 *
 * 与工作区设置里的同名控件是**同一个后端键、两个作用域**：
 * - 这里 = 服务端缺省（请求未携带 `max_steps` 时生效，重启后仍保留）；
 * - 工作区设置 = 每轮请求值（本地即时生效，会覆盖服务端缺省）。
 *
 * 卡片直接走专用端点（GET/PUT `/api/runtime/config/agent/max-steps`），不参与
 * 本页配置文档的草稿 / 保存流程——落盘目标是 runtime 配置文件，而不是本页编辑
 * 的那份 config.yaml。
 */
export function AgentMaxStepsPolicyCard({ className }: { className?: string }) {
  const { t } = useTranslation("runtimeConfig");
  const { settings } = useAppSettings();
  const { error, loading, refresh, snapshot } = useRuntimeAgentMaxSteps();
  const [draft, setDraft] = useState("0");
  const [dirty, setDirty] = useState(false);
  const [saving, setSaving] = useState(false);
  const [saveStatus, setSaveStatus] = useState<{
    text: string;
    tone: "error" | "success";
  } | null>(null);

  const limit = snapshot?.limit ?? maxStepsFallbackLimit;

  // 读到权威值后回填输入框；用户已经改过草稿时不覆盖（避免把正在编辑的值冲掉）。
  useEffect(() => {
    if (!snapshot || dirty) {
      return;
    }
    setDraft(String(snapshot.maxSteps));
  }, [dirty, snapshot]);

  async function handleSave() {
    setSaving(true);
    setSaveStatus(null);
    try {
      const saved = await saveRuntimeAgentMaxSteps({
        max_steps: clampMaxSteps(draft, limit),
      });
      setDirty(false);
      setSaveStatus({
        tone: "success",
        text: t("editor.agentMaxSteps.saved", {
          count: saved.max_steps,
          path: saved.config_file,
        }),
      });
      await refresh();
    } catch (saveError) {
      setSaveStatus({
        tone: "error",
        text: t("editor.agentMaxSteps.saveFailed", {
          message:
            saveError instanceof Error ? saveError.message : String(saveError),
        }),
      });
    } finally {
      setSaving(false);
    }
  }

  return (
    <SettingsPanelCard
      className={className}
      icon={<BotIcon size={16} className="text-accent-primary" />}
      title={t("editor.agentMaxSteps.title")}
      description={t("editor.agentMaxSteps.description")}
    >
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center">
        <input
          type="number"
          min={0}
          max={limit}
          step={1}
          value={draft}
          disabled={loading || snapshot === null}
          onChange={(event) => {
            setDirty(true);
            setDraft(String(clampMaxSteps(event.target.value, limit)));
          }}
          className={cn(editorControlClassName, "sm:max-w-[10rem]")}
          aria-label={t("editor.agentMaxSteps.fieldLabel")}
        />
        <p className="text-sm leading-6 text-muted-foreground">
          {snapshot
            ? t("editor.agentMaxSteps.currentDefault", {
                count: snapshot.maxSteps,
              })
            : loading
              ? t("editor.agentMaxSteps.loading")
              : t("editor.agentMaxSteps.unknown")}{" "}
          {t("editor.agentMaxSteps.limitHint", { limit: String(limit) })}
        </p>
        <Button
          className="sm:ml-auto"
          disabled={saving || loading || snapshot === null}
          onClick={() => void handleSave()}
          size="sm"
          variant="secondary"
        >
          {saving
            ? t("editor.agentMaxSteps.saving")
            : t("editor.agentMaxSteps.save")}
        </Button>
      </div>

      {error ? (
        <p className="mt-3 text-sm leading-6 text-accent-orange" role="alert">
          {t("editor.agentMaxSteps.loadFailed", { message: error })}
        </p>
      ) : null}

      {saveStatus ? (
        <p
          className={cn(
            "mt-3 text-sm leading-6",
            saveStatus.tone === "success"
              ? "text-accent-teal"
              : "text-accent-orange",
          )}
          role={saveStatus.tone === "error" ? "alert" : "status"}
        >
          {saveStatus.text}
        </p>
      ) : null}

      <div className="mt-3 space-y-2">
        <SettingsInfoCard
          size="compact"
          description={
            snapshot
              ? t("editor.agentMaxSteps.configFile", {
                  path: snapshot.configFile,
                })
              : t("editor.agentMaxSteps.scopeNote")
          }
        />
        {snapshot && snapshot.layers.length > 0 ? (
          <SettingsInfoCard
            size="compact"
            description={t("editor.agentMaxSteps.layerSummary", {
              layers: snapshot.layers
                .map((layer) =>
                  [
                    layer.kind,
                    layer.path,
                    layer.read_only
                      ? t("editor.agentMaxSteps.layerReadOnly")
                      : layer.present
                        ? t("editor.agentMaxSteps.layerWritable")
                        : t("editor.agentMaxSteps.layerCandidate"),
                  ].join(" · "),
                )
                .join("  |  "),
            })}
          />
        ) : null}
        <SettingsInfoCard
          size="compact"
          description={t("editor.agentMaxSteps.workspaceValue", {
            count: settings.chat.maxSteps,
          })}
        />
      </div>
    </SettingsPanelCard>
  );
}
