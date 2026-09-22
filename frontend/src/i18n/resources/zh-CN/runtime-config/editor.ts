// 由 src/i18n/resources/zh-CN.ts 机械拆分而来（P0-6），仅搬迁不改语义。
import { zhRuntimeConfigEditorShell } from "./editor-shell";
import { zhRuntimeConfigEditorModes } from "./editor-modes";
import { zhRuntimeConfigEditorProviders } from "./editor-providers";
import { zhRuntimeConfigEditorAgentRouting } from "./editor-agent-routing";
import { zhRuntimeConfigEditorRuntime } from "./editor-runtime";
import { zhRuntimeConfigEditorNetwork } from "./editor-network";
import { zhRuntimeConfigEditorTransformer } from "./editor-transformer";
import { zhRuntimeConfigEditorFeedback } from "./editor-feedback";

export const zhRuntimeConfigEditor = {
  ...zhRuntimeConfigEditorShell,
  ...zhRuntimeConfigEditorModes,
  ...zhRuntimeConfigEditorProviders,
  ...zhRuntimeConfigEditorAgentRouting,
  ...zhRuntimeConfigEditorRuntime,
  ...zhRuntimeConfigEditorNetwork,
  ...zhRuntimeConfigEditorTransformer,
  ...zhRuntimeConfigEditorFeedback,
} as const;
