// 由 src/i18n/resources/zh-CN.ts 机械拆分而来（P0-6），仅搬迁不改语义。
import { zhRuntimeConfigEditor } from "./editor";
import { zhRuntimeConfigMcp } from "./mcp";
import { zhRuntimeConfigPage } from "./page";

export const zhRuntimeConfig = {
  page: zhRuntimeConfigPage,
  editor: zhRuntimeConfigEditor,
  mcp: zhRuntimeConfigMcp,
} as const;
