// 由 src/i18n/resources/en-US.ts 机械拆分而来（P0-6），仅搬迁不改语义。
import type { DeepStringShape } from "../../shape";
import type { zhRuntimeConfig } from "../../zh-CN/runtime-config";
import { enRuntimeConfigEditor } from "./editor";
import { enRuntimeConfigMcp } from "./mcp";
import { enRuntimeConfigPage } from "./page";

export const enRuntimeConfig = {
  page: enRuntimeConfigPage,
  editor: enRuntimeConfigEditor,
  mcp: enRuntimeConfigMcp,
} satisfies DeepStringShape<typeof zhRuntimeConfig>;
