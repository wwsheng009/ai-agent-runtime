import { useState } from "react";
import { useTranslation } from "react-i18next";

import { Select } from "@/components/ui/select";
import { useSessionPermissionMode } from "@/hooks/workspace/use-session-permission-mode";
import { type RuntimePermissionModeOption } from "@/lib/runtime-api";

import { PermissionModeConfirmDialog } from "./permission-mode-confirm-dialog";

// P1-x 会话权限选择器（composer 底部）：执行中亦可切换。
//
// 只做「渲染 + 回调」：模式真值由 useSessionPermissionMode 持有；
// 可选清单来自后端 supported_modes，前端仅负责按 value 映射文案，
// 未知 value 回落后端自带的 label，避免前后端枚举漂移时出现空选项。
type ComposerPermissionModeControlProps = {
  sessionId?: string;
  lastRuntimeEventType?: string;
  runtimeEventCount?: number;
};

// 文案键直接跟随后端 `supported_modes` 的 value（下划线式 canonical 值）。
// 只认已知枚举：未知 value 回落后端 label，避免拼错的值被当成新模式渲染。
const KNOWN_MODE_VALUES = new Set([
  "accept_edits",
  "bypass_permissions",
  "default",
  "plan",
]);

type TranslateKey = (key: string) => string;

function modeTextKey(value: string, leaf: "description" | "label") {
  return KNOWN_MODE_VALUES.has(value)
    ? `composer.permission.mode.${value}.${leaf}`
    : null;
}

export function ComposerPermissionModeControl({
  sessionId,
  lastRuntimeEventType,
  runtimeEventCount,
}: ComposerPermissionModeControlProps) {
  const { t } = useTranslation("workspace");
  const { error, loading, mode, options, pending, setPermissionMode } =
    useSessionPermissionMode({
      lastRuntimeEventType,
      runtimeEventCount,
      sessionId,
    });
  // 待确认的危险模式（含所属会话）；确认前不发起切换请求。
  const [pendingDangerous, setPendingDangerous] = useState<{
    mode: string;
    sessionId: string;
  } | null>(null);
  // 会话切换后旧确认自动失效（派生判断，避免在 effect 里 setState）。
  const pendingDangerousMode =
    pendingDangerous && pendingDangerous.sessionId === sessionId
      ? pendingDangerous.mode
      : null;

  if (!sessionId) {
    return null;
  }

  // i18next 的类型收窄对动态键不友好，这里收敛成一个普通查表函数。
  const translate: TranslateKey = (key) => t(key as never) as string;
  const selectOptions = options.map((option) => ({
    value: option.value,
    label: optionLabel(option, translate),
  }));
  const selectedOption = options.find((option) => option.value === mode);
  const selectedDescription = selectedOption
    ? optionDescription(selectedOption, translate)
    : null;
  const isDangerous = Boolean(selectedOption?.dangerous);
  const pendingDangerousOption = pendingDangerousMode
    ? options.find((option) => option.value === pendingDangerousMode)
    : undefined;

  return (
    <>
      <label
        className="inline-flex items-center gap-1.5"
        title={selectedDescription ?? undefined}
      >
        <span>{t("composer.permission.label")}</span>
        <Select
          ariaLabel={t("composer.permission.label")}
          value={mode}
          onChange={(value) => {
            const option = options.find((item) => item.value === value);
            if (option?.dangerous && sessionId) {
              // 危险模式先弹确认，确认后才带 confirm: true 调接口。
              setPendingDangerous({ mode: value, sessionId });
              return;
            }
            void setPermissionMode(value);
          }}
          options={selectOptions}
          disabled={pending || selectOptions.length === 0}
          side="top"
          triggerClassName={
            isDangerous
              ? "min-w-[8rem] max-w-[14rem] rounded-[0.6rem] border-accent-gold/60 px-2 py-1 text-base leading-none text-accent-gold"
              : "min-w-[8rem] max-w-[14rem] rounded-[0.6rem] px-2 py-1 text-base leading-none"
          }
          menuClassName="max-w-[16rem]"
          optionClassName="text-base"
        />
        <span
          data-composer-permission-mode={mode}
          data-composer-permission-pending={pending ? "true" : undefined}
          className="sr-only"
        >
          {selectedDescription ?? ""}
        </span>
        {loading || pending ? (
          <span data-composer-permission-busy className="text-muted-foreground">
            {t("composer.permission.pending")}
          </span>
        ) : null}
        {error ? (
          <span data-composer-permission-error className="text-accent-gold">
            {t("composer.permission.error")}
          </span>
        ) : null}
      </label>
      <PermissionModeConfirmDialog
        open={pendingDangerousMode !== null}
        modeLabel={
          pendingDangerousOption
            ? optionBaseLabel(pendingDangerousOption, translate)
            : ""
        }
        onCancel={() => {
          // 取消只关对话框：Select 的 value 来自模式快照，无需回滚。
          setPendingDangerous(null);
        }}
        onConfirm={() => {
          const modeToConfirm = pendingDangerousMode;
          setPendingDangerous(null);
          if (modeToConfirm) {
            void setPermissionMode(modeToConfirm, { confirm: true });
          }
        }}
      />
    </>
  );
}

function optionBaseLabel(
  option: RuntimePermissionModeOption,
  translate: TranslateKey,
) {
  const key = modeTextKey(option.value, "label");
  return key ? translate(key) : option.label;
}

function optionLabel(option: RuntimePermissionModeOption, translate: TranslateKey) {
  const label = optionBaseLabel(option, translate);
  return option.dangerous ? `${label} ⚠` : label;
}

function optionDescription(
  option: RuntimePermissionModeOption,
  translate: TranslateKey,
) {
  const key = modeTextKey(option.value, "description");
  return key ? translate(key) : (option.description ?? "");
}
