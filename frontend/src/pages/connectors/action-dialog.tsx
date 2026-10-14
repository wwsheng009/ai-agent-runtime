// 通道动作对话框（微信扫码登录）：外层遮罩 + 面板壳，内容由 ConnectorLoginPanel
// 承载（与「新增连接器」内嵌扫码共用同一份状态机）。
//
// 回填保存中拦截关闭（遮罩点击经 onBusyChange 感知）。

import { useState } from "react";
import { useTranslation } from "react-i18next";

import { DialogOverlay, DialogPanel } from "@/components/ui/dialog-shell";
import type { ConnectorActionSpec, ConnectorView } from "@/types/runtime";

import { ConnectorLoginPanel } from "./login-panel";

interface ConnectorActionDialogProps {
  open: boolean;
  connector: ConnectorView | null;
  action: ConnectorActionSpec | null;
  /** 凭据回填成功后回调（父页刷新列表）。 */
  onRefilled?: () => void;
  onClose: () => void;
}

export function ConnectorActionDialog({
  open,
  connector,
  action,
  onRefilled,
  onClose,
}: ConnectorActionDialogProps) {
  const { t } = useTranslation("connectors");
  const [busy, setBusy] = useState(false);

  if (!open || !connector || !action) {
    return null;
  }

  return (
    <DialogOverlay onDismiss={busy ? () => undefined : onClose}>
      <DialogPanel
        role="dialog"
        aria-modal="true"
        aria-label={t("actionDialog.title", { name: connector.name })}
        className="max-w-md"
      >
        <ConnectorLoginPanel
          connectorId={connector.id}
          connectorName={connector.name}
          action={action}
          onRefilled={onRefilled}
          onClose={onClose}
          onBusyChange={setBusy}
          showHeader
        />
      </DialogPanel>
    </DialogOverlay>
  );
}
