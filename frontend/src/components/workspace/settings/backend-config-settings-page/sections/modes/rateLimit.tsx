// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { Suspense } from "react";
import { type RateLimitDomain } from "../../domains/rate-limit";
import { RuntimeRateLimitDomainEditor } from "../../lazy-editors";
import { ConfigEditorLoadingCard } from "../../primitives";
import { type ConfigEditorCore } from "../../use-config-core";


export function RateLimitModeSection({ core, domain }: { core: ConfigEditorCore; domain: RateLimitDomain }) {
  const {
    apiKeyLimits,
    pathLimits,
    rateLimitConfig,
    t,
  } = core;

  const {
    handleDeleteApiKeyLimit,
    handleDeletePathLimit,
    handleRateLimitConfigChange,
    handleSaveApiKeyLimit,
    handleSavePathLimit,
  } = domain;

  return (
    <Suspense
      fallback={
        <ConfigEditorLoadingCard
          label={t("editor.modes.rateLimit.label")}
        />
      }
    >
      <RuntimeRateLimitDomainEditor
        apiKeyLimits={apiKeyLimits}
        onChangeConfig={handleRateLimitConfigChange}
        onDeleteApiKeyLimit={handleDeleteApiKeyLimit}
        onDeletePathLimit={handleDeletePathLimit}
        onSaveApiKeyLimit={handleSaveApiKeyLimit}
        onSavePathLimit={handleSavePathLimit}
        pathLimits={pathLimits}
        rateLimitConfig={rateLimitConfig}
      />
    </Suspense>
  );
}
