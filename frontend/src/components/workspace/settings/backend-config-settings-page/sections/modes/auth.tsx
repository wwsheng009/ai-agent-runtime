// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { Suspense } from "react";
import { type AuthDomain } from "../../domains/auth";
import { RuntimeAuthDomainEditor } from "../../lazy-editors";
import { ConfigEditorLoadingCard } from "../../primitives";
import { type ConfigEditorCore } from "../../use-config-core";


export function AuthModeSection({ core, domain }: { core: ConfigEditorCore; domain: AuthDomain }) {
  const {
    authConfig,
    t,
  } = core;

  const {
    handleAuthChange,
  } = domain;

  return (
    <Suspense
      fallback={
        <ConfigEditorLoadingCard
          label={t("editor.modes.auth.label")}
        />
      }
    >
      <RuntimeAuthDomainEditor
        authConfig={authConfig}
        onChange={handleAuthChange}
      />
    </Suspense>
  );
}
