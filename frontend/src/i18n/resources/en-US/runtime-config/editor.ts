// 由 src/i18n/resources/en-US.ts 机械拆分而来（P0-6），仅搬迁不改语义。
import type { DeepStringShape } from "../../shape";
import type { zhRuntimeConfigEditor } from "../../zh-CN/runtime-config/editor";
import { enRuntimeConfigEditorShell } from "./editor-shell";
import { enRuntimeConfigEditorModes } from "./editor-modes";
import { enRuntimeConfigEditorProviders } from "./editor-providers";
import { enRuntimeConfigEditorAgentRouting } from "./editor-agent-routing";
import { enRuntimeConfigEditorRuntime } from "./editor-runtime";
import { enRuntimeConfigEditorNetwork } from "./editor-network";
import { enRuntimeConfigEditorTransformer } from "./editor-transformer";
import { enRuntimeConfigEditorFeedback } from "./editor-feedback";

export const enRuntimeConfigEditor = {
  ...enRuntimeConfigEditorShell,
  ...enRuntimeConfigEditorModes,
  ...enRuntimeConfigEditorProviders,
  ...enRuntimeConfigEditorAgentRouting,
  ...enRuntimeConfigEditorRuntime,
  ...enRuntimeConfigEditorNetwork,
  ...enRuntimeConfigEditorTransformer,
  ...enRuntimeConfigEditorFeedback,
} satisfies DeepStringShape<typeof zhRuntimeConfigEditor>;
