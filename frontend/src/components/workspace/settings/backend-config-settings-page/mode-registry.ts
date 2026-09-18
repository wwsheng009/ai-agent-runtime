// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { ActivityIcon, BotIcon, FileTextIcon, GaugeIcon, type LucideIcon, RefreshCcwIcon, RouteIcon, ServerIcon, Settings2Icon, UsersIcon, WifiIcon } from "lucide-react";
import { type EditorMode } from "./types";


export const modeMenuEntries: Array<{
  descriptionKey: string;
  icon: LucideIcon;
  labelKey: string;
  mode: EditorMode;
}> = [
  {
    mode: "providers",
    labelKey: "editor.modes.providers.label",
    descriptionKey: "editor.modes.providers.description",
    icon: BotIcon,
  },
  {
    mode: "agentRouting",
    labelKey: "editor.modes.agentRouting.label",
    descriptionKey: "editor.modes.agentRouting.description",
    icon: UsersIcon,
  },
  {
    mode: "providerGroups",
    labelKey: "editor.modes.providerGroups.label",
    descriptionKey: "editor.modes.providerGroups.description",
    icon: RouteIcon,
  },
  {
    mode: "networkProxy",
    labelKey: "editor.modes.networkProxy.label",
    descriptionKey: "editor.modes.networkProxy.description",
    icon: RouteIcon,
  },
  {
    mode: "auth",
    labelKey: "editor.modes.auth.label",
    descriptionKey: "editor.modes.auth.description",
    icon: Settings2Icon,
  },
  {
    mode: "routing",
    labelKey: "editor.modes.routing.label",
    descriptionKey: "editor.modes.routing.description",
    icon: RouteIcon,
  },
  {
    mode: "rateLimit",
    labelKey: "editor.modes.rateLimit.label",
    descriptionKey: "editor.modes.rateLimit.description",
    icon: GaugeIcon,
  },
  {
    mode: "resourceManager",
    labelKey: "editor.modes.resourceManager.label",
    descriptionKey: "editor.modes.resourceManager.description",
    icon: RouteIcon,
  },
  {
    mode: "providerQueue",
    labelKey: "editor.modes.providerQueue.label",
    descriptionKey: "editor.modes.providerQueue.description",
    icon: GaugeIcon,
  },
  {
    mode: "concurrency",
    labelKey: "editor.modes.concurrency.label",
    descriptionKey: "editor.modes.concurrency.description",
    icon: GaugeIcon,
  },
  {
    mode: "retry",
    labelKey: "editor.modes.retry.label",
    descriptionKey: "editor.modes.retry.description",
    icon: RefreshCcwIcon,
  },
  {
    mode: "monitor",
    labelKey: "editor.modes.monitor.label",
    descriptionKey: "editor.modes.monitor.description",
    icon: ActivityIcon,
  },
  {
    mode: "websocket",
    labelKey: "editor.modes.websocket.label",
    descriptionKey: "editor.modes.websocket.description",
    icon: WifiIcon,
  },
  {
    mode: "circuitBreaker",
    labelKey: "editor.modes.circuitBreaker.label",
    descriptionKey: "editor.modes.circuitBreaker.description",
    icon: ActivityIcon,
  },
  {
    mode: "transformer",
    labelKey: "editor.modes.transformer.label",
    descriptionKey: "editor.modes.transformer.description",
    icon: Settings2Icon,
  },
  {
    mode: "mcp",
    labelKey: "editor.modes.mcp.label",
    descriptionKey: "editor.modes.mcp.description",
    icon: ServerIcon,
  },
  {
    mode: "source",
    labelKey: "editor.modes.source.label",
    descriptionKey: "editor.modes.source.description",
    icon: FileTextIcon,
  },
];
