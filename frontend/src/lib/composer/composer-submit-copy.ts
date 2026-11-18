// P2-3 / P2-5：发送按钮文案与禁用原因的纯结算。
//
// - 不再把内部模型概念「回合」暴露给用户；
// - disabled 时必须说明原因（空输入 / 附件未完成），避免灰按钮没有解释；
// - 响应中按钮切换为「停止响应」，不再附带提交快捷键提示。

import type { TFunction } from "i18next";

export type ComposerSubmitCopyInput = {
  hasSession: boolean;
  isNewThread: boolean;
  isResponding: boolean;
  draftEmpty: boolean;
  unsettledAttachmentCount: number;
};

type Translate = TFunction<"workspace">;

export function resolveComposerSubmitCopy(
  input: ComposerSubmitCopyInput,
  t: Translate,
): { label: string; title: string } {
  const label = input.isResponding
    ? t("composer.submit.stopResponse")
    : input.isNewThread
      ? t("composer.submit.startNewThread")
      : input.hasSession
        ? t("composer.submit.sendTurn")
        : t("composer.submit.startThread");

  const blockedHint = input.isResponding
    ? null
    : input.unsettledAttachmentCount > 0
      ? t("composer.submit.disabledAttachments")
      : input.draftEmpty
        ? t("composer.submit.disabledEmpty")
        : null;

  const title = input.isResponding
    ? label
    : blockedHint
      ? `${label} · ${blockedHint} (${t("composer.shortcuts")})`
      : `${label} (${t("composer.shortcuts")})`;

  return { label, title };
}
