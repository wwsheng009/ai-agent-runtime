import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

import { useComposerSurface } from "@/components/workspace/composer-surface-store";
import { Select } from "@/components/ui/select";
import { useSessionPermissionMode } from "@/hooks/workspace/use-session-permission-mode";
import { type RuntimePermissionModeOption } from "@/lib/runtime-api";
import { cn } from "@/lib/utils";

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
  /** 模式快照上抛：宿主据此渲染危险态常驻警示条（不重复拉取接口）。 */
  onModeChange?: (snapshot: {
    sessionId: string;
    mode: string;
    dangerous: boolean;
  }) => void;
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
  onModeChange,
}: ComposerPermissionModeControlProps) {
  const { t } = useTranslation("workspace");
  const surface = useComposerSurface("permission");
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

  // 宿主警示条只消费模式快照，不重复请求接口；会话切换后的旧快照由宿主按 sessionId 忽略。
  const selectedDangerous = Boolean(
    options.find((option) => option.value === mode)?.dangerous,
  );
  useEffect(() => {
    if (!sessionId) {
      return;
    }
    onModeChange?.({ sessionId, mode, dangerous: selectedDangerous });
  }, [mode, onModeChange, selectedDangerous, sessionId]);

  if (!sessionId) {
    return null;
  }

  // i18next 的类型收窄对动态键不友好，这里收敛成一个普通查表函数。
  const translate: TranslateKey = (key) => t(key as never) as string;
  const selectOptions = options.map((option) => ({
    value: option.value,
    label: optionLabel(option, translate),
  }));
  const canSelectMode = selectOptions.length > 0 && mode.length > 0;
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
      <div
        className="flex min-w-0 shrink items-center gap-1.5"
        data-composer-permission-danger={isDangerous ? "true" : undefined}
        data-composer-permission-control
        title={selectedDescription ?? undefined}
      >
        {canSelectMode ? (
          <Select
            className="min-w-0 shrink"
            ariaLabel={t("composer.permission.label")}
            open={surface.open}
            onOpenChange={(next) => {
              if (next) {
                surface.openSelf();
              } else {
                surface.closeSelf();
              }
            }}
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
            disabled={pending || loading}
            side="top"
            triggerPrefix={t("composer.permission.label")}
            triggerClassName={cn(
              "h-8 w-[clamp(7rem,12vw,9rem)] min-w-0 max-w-full rounded-field px-2 py-0 text-sm leading-none normal-case tracking-normal",
              isDangerous &&
                "border-analytics-danger-border bg-analytics-danger-soft text-analytics-danger hover:border-analytics-danger-border hover:text-analytics-danger",
            )}
            triggerTestId="composer-permission-trigger"
            size="sm"
            menuClassName="max-w-[16rem]"
            optionClassName="text-base"
          />
        ) : (
          <>
            <span className="shrink-0 text-muted-foreground app-text-9 uppercase tracking-[0.08em]">
              {t("composer.permission.label")}
            </span>
            <span
              data-composer-permission-unavailable
              role={loading ? "status" : "note"}
              className="min-w-0 truncate text-muted-foreground"
            >
              {loading
                ? t("composer.permission.loading")
                : error
                  ? t("composer.permission.error")
                  : t("composer.permission.unavailable")}
            </span>
          </>
        )}
        <span
          data-composer-permission-mode={mode}
          data-composer-permission-pending={pending ? "true" : undefined}
          className="sr-only"
        >
          {selectedDescription ?? ""}
        </span>
        {canSelectMode && loading ? (
          <span data-composer-permission-busy className="min-w-0 truncate text-muted-foreground">
            {t("composer.permission.loading")}
          </span>
        ) : pending ? (
          <span data-composer-permission-busy className="min-w-0 truncate text-muted-foreground">
            {t("composer.permission.pending")}
          </span>
        ) : null}
        {error && canSelectMode ? (
          <span data-composer-permission-error className="min-w-0 truncate text-accent-gold">
            {t("composer.permission.error")}
          </span>
        ) : null}
      </div>
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
  return optionBaseLabel(option, translate);
}

function optionDescription(
  option: RuntimePermissionModeOption,
  translate: TranslateKey,
) {
  const key = modeTextKey(option.value, "description");
  return key ? translate(key) : (option.description ?? "");
}
