import { useTranslation } from "react-i18next";

import { Select } from "@/components/ui/select";
import { useSessionPermissionMode } from "@/hooks/workspace/use-session-permission-mode";
import { type RuntimePermissionModeOption } from "@/lib/runtime-api";

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

  return (
    <label
      className="inline-flex items-center gap-1.5"
      title={selectedDescription ?? undefined}
    >
      <span>{t("composer.permission.label")}</span>
      <Select
        ariaLabel={t("composer.permission.label")}
        value={mode}
        onChange={(value) => {
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
  );
}

function optionLabel(option: RuntimePermissionModeOption, translate: TranslateKey) {
  const key = modeTextKey(option.value, "label");
  const label = key ? translate(key) : option.label;
  return option.dangerous ? `${label} ⚠` : label;
}

function optionDescription(
  option: RuntimePermissionModeOption,
  translate: TranslateKey,
) {
  const key = modeTextKey(option.value, "description");
  return key ? translate(key) : (option.description ?? "");
}
