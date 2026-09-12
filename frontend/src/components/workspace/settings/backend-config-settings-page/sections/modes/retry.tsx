// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { Suspense } from "react";
import { type RetryDomain } from "../../domains/retry";
import { RuntimeRetryDomainEditor } from "../../lazy-editors";
import { ConfigEditorLoadingCard } from "../../primitives";
import { type ConfigEditorCore } from "../../use-config-core";


export function RetryModeSection({ core, domain }: { core: ConfigEditorCore; domain: RetryDomain }) {
  const {
    retryConfig,
    retryRules,
    t,
  } = core;

  const {
    handleDeleteRetryRule,
    handleMoveRetryRule,
    handleRetryConfigChange,
    handleSaveRetryRule,
  } = domain;

  return (
    <Suspense
      fallback={
        <ConfigEditorLoadingCard
          label={t("editor.modes.retry.label")}
        />
      }
    >
      <RuntimeRetryDomainEditor
        config={retryConfig}
        onChangeConfig={handleRetryConfigChange}
        onDeleteRule={handleDeleteRetryRule}
        onMoveRule={handleMoveRetryRule}
        onSaveRule={handleSaveRetryRule}
        rules={retryRules}
      />
    </Suspense>
  );
}
