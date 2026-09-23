// 「路由」区块的写入动作区：config 层二次确认、错误/提示行、保存与重置。
//
// 语义纪律：
//   * 后端校验错误**原样展示**（不翻译、不改写），字段级定位由
//     resolveRoutingErrorEcho 交给档位表格渲染；
//   * config 层写入先经行内二次确认（§5.4），未确认前不发请求；
//   * 保存/重置在不可写层或写入中禁用（与层选择器同一口径）。

import { AlertTriangleIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { SESSION_DETAIL_SUBCARD_CLASS } from "@/components/workspace/session-detail-panel-shared";
import { cn } from "@/lib/utils";

/** 待确认的写入：kind 决定确认后走保存还是重置。 */
export type SessionDetailRoutingPendingWrite = { kind: "save" | "reset" };

export type SessionDetailRoutingActionsProps = {
  layerWritable: boolean;
  saving: boolean;
  pending: SessionDetailRoutingPendingWrite | null;
  /** 已解析的写入目标，用于二次确认文案里的路径回显。 */
  targetPath: string;
  saveError: string;
  notice: string;
  onSave: () => void;
  onReset: () => void;
  onConfirm: () => void;
  onCancelConfirm: () => void;
};

export function SessionDetailRoutingActions({
  layerWritable,
  saving,
  pending,
  targetPath,
  saveError,
  notice,
  onSave,
  onReset,
  onConfirm,
  onCancelConfirm,
}: SessionDetailRoutingActionsProps) {
  const { t } = useTranslation("workspace");

  return (
    <>
      {pending ? (
        <div
          className={cn(
            SESSION_DETAIL_SUBCARD_CLASS,
            "grid gap-1.5 border-analytics-warning-border bg-analytics-warning-soft",
          )}
          data-testid="routing-config-confirm"
        >
          <div className="app-text-11 font-medium text-analytics-warning">
            {t("panels.sessionDetail.routing.confirm.title")}
          </div>
          <p className="app-text-10 leading-4 break-all text-muted-foreground">
            {targetPath
              ? t("panels.sessionDetail.routing.confirm.body", { path: targetPath })
              : t("panels.sessionDetail.routing.confirm.bodyNoPath")}
          </p>
          <div className="flex gap-1.5">
            <Button
              className="h-6 px-2 app-text-11"
              data-testid="routing-config-confirm-ok"
              disabled={saving}
              onClick={onConfirm}
              size="sm"
              type="button"
            >
              {t("panels.sessionDetail.routing.actions.confirm")}
            </Button>
            <Button
              className="h-6 px-2 app-text-11"
              onClick={onCancelConfirm}
              size="sm"
              type="button"
              variant="ghost"
            >
              {t("panels.sessionDetail.routing.actions.cancel")}
            </Button>
          </div>
        </div>
      ) : null}

      {saveError ? (
        <div
          className="grid grid-cols-[auto_minmax(0,1fr)] items-start gap-1.5"
          data-testid="routing-error"
          role="alert"
        >
          <AlertTriangleIcon
            className="mt-0.5 shrink-0 text-analytics-danger"
            size={12}
          />
          <p className="min-w-0 break-words app-text-10 leading-4 text-analytics-danger">
            {saveError}
          </p>
        </div>
      ) : null}

      {notice ? (
        <p
          className="app-text-10 leading-4 text-muted-foreground"
          data-testid="routing-notice"
        >
          {notice}
        </p>
      ) : null}

      <div className="flex gap-1.5">
        <Button
          className="h-6 px-2 app-text-11"
          data-testid="routing-save"
          disabled={!layerWritable || saving}
          onClick={onSave}
          size="sm"
          type="button"
        >
          {t("panels.sessionDetail.routing.actions.save")}
        </Button>
        <Button
          className="h-6 px-2 app-text-11"
          data-testid="routing-reset"
          disabled={!layerWritable || saving}
          onClick={onReset}
          size="sm"
          type="button"
          variant="ghost"
        >
          {t("panels.sessionDetail.routing.actions.reset")}
        </Button>
      </div>
    </>
  );
}
