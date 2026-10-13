// 「扫码接入」入口：把入口按钮 + 创建弹窗 + 创建后的扫码登录弹窗收成一个
// 自包含组件，列表页只负责刷新（onChanged）。
//
// 适用通道来自 Schema（声明了 kind=qr 动作的通道）；无此类通道时整块不渲染，
// 避免出现「点了没反应」的按钮。

import { useState } from "react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import type {
  ConnectorActionSpec,
  ConnectorSchema,
  ConnectorView,
} from "@/types/runtime";

import { ConnectorActionDialog } from "./action-dialog";
import { ConnectorScanDialog } from "./scan-dialog";
import { findProviderSchema, findQRAction, scanProviders } from "./shared";

interface ConnectorScanEntryProps {
  schema: ConnectorSchema | null;
  existingConnectors: ConnectorView[];
  /** 创建或凭据回填后回调（父页刷新列表）。 */
  onChanged: () => void;
}

interface ScanTarget {
  connector: ConnectorView;
  action: ConnectorActionSpec;
}

export function ConnectorScanEntry({
  schema,
  existingConnectors,
  onChanged,
}: ConnectorScanEntryProps) {
  const { t } = useTranslation("connectors");
  const [scanOpen, setScanOpen] = useState(false);
  const [target, setTarget] = useState<ScanTarget | null>(null);

  if (scanProviders(schema).length === 0) {
    return null;
  }

  const handleCreated = (connector: ConnectorView) => {
    setScanOpen(false);
    onChanged();
    const action = findQRAction(findProviderSchema(schema, connector.provider));
    if (action) {
      setTarget({ connector, action });
    }
  };

  return (
    <>
      <Button variant="secondary" size="sm" onClick={() => setScanOpen(true)}>
        {t("page.scan")}
      </Button>
      <ConnectorScanDialog
        open={scanOpen}
        schema={schema}
        existingConnectors={existingConnectors}
        onCreated={handleCreated}
        onClose={() => setScanOpen(false)}
      />
      <ConnectorActionDialog
        open={target !== null}
        connector={target?.connector ?? null}
        action={target?.action ?? null}
        onRefilled={onChanged}
        onClose={() => setTarget(null)}
      />
    </>
  );
}
