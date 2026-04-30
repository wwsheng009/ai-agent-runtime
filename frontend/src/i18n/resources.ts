import { enUS } from "./resources/en-US";
import { zhCN } from "./resources/zh-CN";

export const defaultNS = "common" as const;

export const resources = {
  "zh-CN": zhCN,
  "en-US": enUS,
} as const;
