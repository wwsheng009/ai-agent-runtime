import { useState } from "react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/select";
import { useRuntimeApprovalExplainSettings } from "@/hooks/workspace/use-runtime-approval-explain";
import { saveRuntimeApprovalExplainSettings } from "@/lib/runtime-api";
import { cn } from "@/lib/utils";
import type { RuntimeApprovalExplainMode } from "@/types/runtime";

import { SettingsPanelCard } from "./settings-panel-card";
import { SettingsSection } from "./settings-section";

/**
 * 「审批解释模式」区块：读写 runtime 的进程级开关
 * （GET/PUT `/api/runtime/config/approval-explain`）。
 *
 * 该开关决定审批「解释」按钮是否/何时真的调用模型，改动对后续解释立即生效，
 * 但**不落盘**：runtime 重启后回到 AICLI_APPROVAL_EXPLAIN_MODE 或默认
 * on_demand。文案必须如实说明这一点，不能暗示会持久化。
 *
 * 卡片直接走专用端点，不参与本页配置文档的草稿 / 保存流程；模式清单由后端
 * `supported_modes` 下发，前端只负责渲染，不写死枚举。
 */
export function ApprovalExplainSettingsCard() {
  const { t } = useTranslation("settings");
  const {
    error,
    loading,
    refresh,
    snapshot,
  } = useRuntimeApprovalExplainSettings();
  const [draft, setDraft] = useState<RuntimeApprovalExplainMode | null>(null);
  const [saving, setSaving] = useState(false);
  const [status, setStatus] = useState<{
    tone: "success" | "error";
    text: string;
  } | null>(null);

  // 草稿只在用户主动切换后存在；未切换时始终回显后端当前值（读到前为空串）。
  const mode = draft ?? snapshot?.mode ?? "";
  const dirty = draft !== null && draft !== snapshot?.mode;

  function modeLabel(value: string) {
    switch (value) {
      case "off":
        return t("chat.approvalExplainModes.off.label");
      case "on_demand":
        return t("chat.approvalExplainModes.onDemand.label");
      case "pre_generate":
        return t("chat.approvalExplainModes.preGenerate.label");
      default:
        return value;
    }
  }

  function modeDescription(value: string) {
    switch (value) {
      case "off":
        return t("chat.approvalExplainModes.off.description");
      case "on_demand":
        return t("chat.approvalExplainModes.onDemand.description");
      case "pre_generate":
        return t("chat.approvalExplainModes.preGenerate.description");
      default:
        return null;
    }
  }

  const options = (snapshot?.supportedModes ?? []).map((value) => ({
    value,
    label: modeLabel(value),
  }));

  // 进程级临时开关：保存后对后续解释立即生效，但不落盘；出错只提示，不改草稿。
  async function handleSave() {
    if (!mode) {
      return;
    }
    setSaving(true);
    setStatus(null);
    try {
      const saved = await saveRuntimeApprovalExplainSettings({ mode });
      setDraft(null);
      setStatus({
        tone: "success",
        text: t("chat.approvalExplainSaved", {
          mode: modeLabel(saved.mode),
        }),
      });
      await refresh();
    } catch (saveError) {
      setStatus({
        tone: "error",
        text: t("chat.approvalExplainSaveFailed", {
          message:
            saveError instanceof Error ? saveError.message : String(saveError),
        }),
      });
    } finally {
      setSaving(false);
    }
  }

  return (
    <SettingsSection
      title={t("chat.approvalExplain")}
      description={t("chat.approvalExplainDescription")}
    >
      <SettingsPanelCard>
        <div className="flex flex-col gap-3 sm:flex-row sm:items-center">
          <Select
            ariaLabel={t("chat.approvalExplain")}
            value={mode}
            onChange={(value) => {
              setDraft(value as RuntimeApprovalExplainMode);
              setStatus(null);
            }}
            options={options}
            placeholder={
              loading
                ? t("chat.approvalExplainLoading")
                : t("chat.approvalExplainUnavailableShort")
            }
            disabled={loading || snapshot === null}
            className="sm:max-w-[14rem]"
            triggerClassName="w-full text-sm"
            optionClassName="text-sm"
          />
          <p className="text-sm leading-6 text-muted-foreground">
            {snapshot
              ? t("chat.approvalExplainCurrent", {
                  mode: modeLabel(snapshot.mode),
                })
              : loading
                ? t("chat.approvalExplainLoading")
                : null}
          </p>
          <Button
            className="sm:ml-auto"
            disabled={saving || loading || snapshot === null || !dirty}
            onClick={() => void handleSave()}
            size="sm"
            variant="secondary"
          >
            {saving
              ? t("chat.approvalExplainSaving")
              : t("chat.approvalExplainSave")}
          </Button>
        </div>
        {modeDescription(mode) ? (
          <p className="mt-3 text-xs leading-6 text-muted-foreground">
            {modeDescription(mode)}
          </p>
        ) : null}
        <p className="mt-3 text-xs leading-6 text-muted-foreground">
          {t("chat.approvalExplainPersistenceHint")}
        </p>
        {error ? (
          <div className="mt-3 space-y-1" role="alert">
            <p className="text-sm leading-6 text-accent-orange">
              {t("chat.approvalExplainUnavailable", { message: error })}
            </p>
            <p className="text-xs leading-6 text-muted-foreground">
              {t("chat.approvalExplainUnavailableHint")}
            </p>
          </div>
        ) : null}
        {status ? (
          <p
            className={cn(
              "mt-3 text-sm leading-6",
              status.tone === "success"
                ? "text-accent-teal"
                : "text-accent-orange",
            )}
            role={status.tone === "error" ? "alert" : "status"}
          >
            {status.text}
          </p>
        ) : null}
      </SettingsPanelCard>
    </SettingsSection>
  );
}
