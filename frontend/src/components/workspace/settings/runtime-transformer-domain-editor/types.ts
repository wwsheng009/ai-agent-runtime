// 由 components/workspace/settings/runtime-transformer-domain-editor.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import {
  type RuntimeTransformerConfigSummary,
  type RuntimeTransformerModifierSummary,
  type TransformerModifierScope,
} from "../runtime-transformer-domain-utils";
import { type TransformerModifierDraftInput } from "../runtime-transformer-domain-form-utils";

export type RuntimeTransformerDomainEditorProps = {
  config: RuntimeTransformerConfigSummary;
  onChangeConfig: (next: RuntimeTransformerConfigSummary) => void;
  onDeleteModifier: (scope: TransformerModifierScope, index: number) => void;
  onMoveModifier: (
    scope: TransformerModifierScope,
    index: number,
    direction: "up" | "down",
  ) => void;
  onSaveModifier: (
    scope: TransformerModifierScope,
    draft: TransformerModifierDraftInput,
    editingIndex: number | null,
  ) => string | null;
  requestModifiers: RuntimeTransformerModifierSummary[];
  responseModifiers: RuntimeTransformerModifierSummary[];
};
