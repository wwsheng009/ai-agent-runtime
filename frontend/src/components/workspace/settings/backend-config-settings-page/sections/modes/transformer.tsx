// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { Suspense } from "react";
import { type TransformerDomain } from "../../domains/transformer";
import { RuntimeTransformerDomainEditor } from "../../lazy-editors";
import { ConfigEditorLoadingCard } from "../../primitives";
import { type ConfigEditorCore } from "../../use-config-core";


export function TransformerModeSection({ core, domain }: { core: ConfigEditorCore; domain: TransformerDomain }) {
  const {
    requestTransformerModifiers,
    responseTransformerModifiers,
    t,
    transformerConfig,
  } = core;

  const {
    handleDeleteTransformerModifier,
    handleMoveTransformerModifier,
    handleSaveTransformerModifier,
    handleTransformerConfigChange,
  } = domain;

  return (
    <Suspense
      fallback={
        <ConfigEditorLoadingCard
          label={t("editor.modes.transformer.label")}
        />
      }
    >
      <RuntimeTransformerDomainEditor
        config={transformerConfig}
        onChangeConfig={handleTransformerConfigChange}
        onDeleteModifier={handleDeleteTransformerModifier}
        onMoveModifier={handleMoveTransformerModifier}
        onSaveModifier={handleSaveTransformerModifier}
        requestModifiers={requestTransformerModifiers}
        responseModifiers={responseTransformerModifiers}
      />
    </Suspense>
  );
}
