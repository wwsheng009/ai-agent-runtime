// 由 src/i18n/resources/en-US.ts 机械拆分而来（P0-6），仅搬迁不改语义。
import type { DeepStringShape } from "../../shape";
import type { zhRuntimeConfigPage } from "../../zh-CN/runtime-config/page";

export const enRuntimeConfigPage = {
    badge: "Runtime config",
    independentPage: "Independent page",
    title: "Backend config workspace",
    description:
      "Manage runtime backend configuration separately, with a dedicated entry for providers.",
    backToWorkspace: "Back to workspace",
    logs: "Logs",
    usage: "Usage",
    skills: "Skill market",
  } satisfies DeepStringShape<typeof zhRuntimeConfigPage>;
