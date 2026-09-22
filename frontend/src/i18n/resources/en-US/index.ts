// 由 src/i18n/resources/en-US.ts 机械拆分而来（P0-6），仅搬迁不改语义。
import type { DeepStringShape } from "../shape";
import type { zhCN } from "../zh-CN";
import { enCommon } from "./common";
import { enLanding } from "./landing";
import { enWorkspace } from "./workspace";
import { enSettings } from "./settings";
import { enLogs } from "./logs";
import { enUsageAnalytics } from "./usage-analytics";
import { enRuntimeConfig } from "./runtime-config";
import { enSkills } from "./skills";

export const enUS = {
  common: enCommon,
  landing: enLanding,
  workspace: enWorkspace,
  runtimeConfig: enRuntimeConfig,
  settings: enSettings,
  logs: enLogs,
  usageAnalytics: enUsageAnalytics,
  skills: enSkills,
} satisfies DeepStringShape<typeof zhCN>;
