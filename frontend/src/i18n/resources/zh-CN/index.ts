// 由 src/i18n/resources/zh-CN.ts 机械拆分而来（P0-6），仅搬迁不改语义。
import { zhCommon } from "./common";
import { zhLanding } from "./landing";
import { zhWorkspace } from "./workspace";
import { zhSettings } from "./settings";
import { zhLogs } from "./logs";
import { zhUsageAnalytics } from "./usage-analytics";
import { zhRuntimeConfig } from "./runtime-config";
import { zhSkills } from "./skills";

export const zhCN = {
  common: zhCommon,
  landing: zhLanding,
  workspace: zhWorkspace,
  runtimeConfig: zhRuntimeConfig,
  settings: zhSettings,
  logs: zhLogs,
  usageAnalytics: zhUsageAnalytics,
  skills: zhSkills,
} as const;
